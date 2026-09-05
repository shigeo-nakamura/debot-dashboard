package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type holderTransport func(*http.Request) (*http.Response, error)

func (f holderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func holderClient(t *testing.T, balances, lighter string) *http.Client {
	t.Helper()
	return &http.Client{Transport: holderTransport(func(r *http.Request) (*http.Response, error) {
		var payload string
		if r.URL.Host == "api.hyperliquid.xyz" {
			if r.Method != "POST" || r.URL.Path != "/info" {
				t.Errorf("unexpected HL route: %s", r.URL.Path)
			}
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			switch req["type"] {
			case "spotClearinghouseState":
				payload = balances
			case "spotMetaAndAssetCtxs":
				// Sparse/out-of-order contexts reproduce the real API shape.
				payload = `[{"tokens":[{"name":"USDC","index":0},{"name":"UBTC","index":197},{"name":"UETH","index":221},{"name":"OTHER","index":222}],"universe":[{"tokens":[197,0],"name":"@142"},{"tokens":[221,0],"name":"@151"}]},[{"coin":"@151","markPx":"2000"},{"coin":"@999","markPx":"9"},{"coin":"@142","markPx":"100000"}]]`
			default:
				t.Errorf("non-read-only HL request: %v", req)
			}
		} else {
			if r.Method != "GET" || r.URL.Host != "mainnet.zklighter.elliot.ai" || r.URL.Path != "/api/v1/account" || r.URL.Query().Get("value") != "42" {
				t.Errorf("unexpected Lighter request: %s", r.URL)
			}
			payload = lighter
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}
}

const holderBalances = `{"balances":[{"coin":"USDC","token":0,"total":"100","hold":"5"},{"coin":"UBTC","token":197,"total":"0.01","hold":"0"}]}`
const holderLighter = `{"code":200,"accounts":[{"account_index":42,"collateral":"200","available_balance":"125","total_asset_value":"210","positions":[{"symbol":"ETH","sign":1,"position":"0.25","position_value":"500","unrealized_pnl":"10","liquidation_price":"1200"}]}]}`

func writeHolder(t *testing.T, path, payload string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestHolderActualEquityAndSimulationAreSeparate(t *testing.T) {
	dir := t.TempDir()
	payload, err := os.ReadFile("tests/fixtures/bull-holder-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture["ts"] = time.Now().Unix()
	fixture["private_key"] = "MUST_NOT_LEAK"
	data, _ := json.Marshal(fixture)
	path := filepath.Join(dir, "status.json")
	writeHolder(t, path, string(data))
	writeHolder(t, filepath.Join(dir, "ADD"), "2")
	target := TargetConfig{Service: "debot-bull-holder", BullHolder: &BullHolderConfig{StatusPath: path, HLAddress: "0x0000000000000000000000000000000000000001", LighterIndex: "42"}}
	got := fetchBullHolder(context.Background(), target, holderClient(t, holderBalances, holderLighter))
	b := got.Status.BullHolder
	if got.ServiceStatus != "active" || !got.Status.DryRun || b.Mode != "On" {
		t.Fatalf("bad status: %+v", got)
	}
	if b.TotalEquity == nil || *b.TotalEquity != 1310 {
		t.Fatalf("equity = %v; want HL 1100 + LT 210, without perp notional", b.TotalEquity)
	}
	if *b.HL.Available != 95 || *b.Lighter.USDC != 200 || *b.Lighter.Available != 125 {
		t.Fatal("balances confused with available/equity")
	}
	if !b.Pending["ADD"] || *b.PendingAdd != 2 || b.Pending["ARM"] {
		t.Fatal("pending intents incorrect")
	}
	if b.Legs["BTC"].SpotSize != 1 || b.HL.Holdings[0].Size != .01 {
		t.Fatal("simulated quantities overwrote actual balances")
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"MUST_NOT_LEAK", "0x0000000000000000000000000000000000000001", "account_index", "stop_order_id"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public status leaked %s", secret)
		}
	}
}
func TestHolderUnavailableAndStaleData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	cfg := TargetConfig{BullHolder: &BullHolderConfig{StatusPath: path, HLAddress: "x", LighterIndex: "42"}}
	client := holderClient(t, holderBalances, holderLighter)
	got := fetchBullHolder(context.Background(), cfg, client)
	if got.Error == "" || got.Status.TS != 0 || got.Status.BullHolder.TotalEquity == nil {
		t.Fatal("missing bot status must not hide actual account snapshot or claim liveness")
	}
	writeHolder(t, path, `{"bot":"bull_holder","ts":1,"dry_run":true,"mode":"Off","legs":{}}`)
	got = fetchBullHolder(context.Background(), cfg, client)
	if got.ServiceStatus != "stale" {
		t.Fatal("fresh API calls must not freshen stale bot state")
	}
	writeHolder(t, path, `{"bot":"wrong_bot","ts":1,"dry_run":true,"mode":"On"}`)
	got = fetchBullHolder(context.Background(), cfg, client)
	if got.Error == "" || got.Status.BullHolder.Mode != "" {
		t.Fatal("invalid payload accepted")
	}
}
func TestHolderPartialAccountsDontProduceMisleadingTotal(t *testing.T) {
	client := holderClient(t, holderBalances, strings.Replace(holderLighter, `"account_index":42`, `"account_index":43`, 1))
	got := fetchBullHolder(context.Background(), TargetConfig{BullHolder: &BullHolderConfig{StatusPath: filepath.Join(t.TempDir(), "status.json"), HLAddress: "x", LighterIndex: "42"}}, client)
	b := got.Status.BullHolder
	if b.HL.Equity == nil || b.Lighter.Equity != nil || b.TotalEquity != nil || b.Lighter.Error == "" {
		t.Fatal("partial/mismatched account data treated as complete")
	}
	for _, balances := range []string{`{}`, `{"balances":[{"coin":"USDC","token":0,"total":"NaN","hold":"0"}]}`} {
		if got := fetchHLSpot(context.Background(), holderClient(t, balances, holderLighter), "x"); got.Equity != nil || got.Error == "" {
			t.Fatal("invalid balance became zero")
		}
	}
	unknown := `{"balances":[{"coin":"OTHER","token":222,"total":"2","hold":"0"}]}`
	gotHL := fetchHLSpot(context.Background(), holderClient(t, unknown, holderLighter), "x")
	if gotHL.Equity != nil || gotHL.Error == "" || len(gotHL.Holdings) != 1 {
		t.Fatal("unpriced asset omitted from total without warning")
	}
}
func TestHolderEmptyAndInvalidADD(t *testing.T) {
	dir := t.TempDir()
	b := &BullHolderStatus{}
	for _, body := range []string{"", "3", "-1", "nonsense"} {
		writeHolder(t, filepath.Join(dir, "ADD"), body)
		readHolderIntents(dir, b)
		if body == "" && (b.PendingAdd == nil || *b.PendingAdd != 1) {
			t.Fatal("empty ADD means one tranche")
		}
		if body == "3" && (b.PendingAdd == nil || *b.PendingAdd != 3) {
			t.Fatal("count not read")
		}
		if body == "-1" || body == "nonsense" {
			if b.PendingAdd != nil || b.OperatorError == "" {
				t.Fatal("invalid ADD accepted")
			}
		}
	}
}
func TestHolderConfigSourceValidation(t *testing.T) {
	for _, target := range []TargetConfig{
		{BullHolder: &BullHolderConfig{StatusPath: "relative"}},
		{BullHolder: &BullHolderConfig{StatusPath: "/tmp/status", HLAddress: "bad"}},
		{BullHolder: &BullHolderConfig{StatusPath: "/tmp/status", LighterIndex: "-1"}},
		{BullHolder: &BullHolderConfig{StatusPath: "/tmp/status"}, S3Bucket: "bucket", S3Key: "key"},
	} {
		target.Service = "holder"
		target.Region = "eu-central-1"
		if normalizeConfig(&Config{Targets: []TargetConfig{target}}) == nil {
			t.Fatal("invalid config accepted")
		}
	}
	valid := Config{Region: "eu-central-1", Targets: []TargetConfig{{Service: "holder", BullHolder: &BullHolderConfig{StatusPath: "/tmp/status"}}}}
	if err := normalizeConfig(&valid); err != nil {
		t.Fatal(err)
	}
}
