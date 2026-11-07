package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testArcusAddr = "0x00000000000000000000000000000000000000a1"

func arcusAccountServer(t *testing.T, body *atomic.Value, calls *int32) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.URL.Path != "/v1/account" || r.URL.Query().Get("address") == "" || r.URL.Query().Get("accountIndex") == "" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	prevURL := arcusAPIBaseURL
	arcusAPIBaseURL = srv.URL
	restoreCache := arcusAccountCacheForTest()
	return func() {
		srv.Close()
		arcusAPIBaseURL = prevURL
		restoreCache()
	}
}

func arcusWalletTarget(name, addr string, idx *int) TargetConfig {
	return TargetConfig{Name: name, Service: name, ArcusVol: &ArcusVolConfig{StatusPath: "/x", AccountAddress: addr, AccountIndex: idx}}
}

func arcusStatus() *StatusData {
	return &StatusData{ArcusVol: &ArcusVolStatus{}}
}

func TestArcusAccountEquityOneReadPerWallet(t *testing.T) {
	var body atomic.Value
	body.Store(`{"accountIndex":0,"equity":"5286.25","freeCollateral":"5286.25"}`)
	var calls int32
	defer arcusAccountServer(t, &body, &calls)()
	now := time.Unix(1_800_000_000, 0)
	a, b := arcusStatus(), arcusStatus()
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("SPY", testArcusAddr, nil), a, now)
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("GLD", strings.ToUpper(testArcusAddr[:2])+testArcusAddr[2:], nil), b, now.Add(time.Second))
	if calls != 1 {
		t.Fatalf("two targets on one wallet within the TTL must make one venue read, got %d", calls)
	}
	if a.ArcusVol.AccountEquityUSD == nil || *a.ArcusVol.AccountEquityUSD != 5286.25 || !a.ArcusVol.AccountConfigured {
		t.Fatalf("equity not filled: %+v", a.ArcusVol)
	}
	if a.ArcusVol.AccountKey == "" || a.ArcusVol.AccountKey != b.ArcusVol.AccountKey {
		t.Fatalf("same wallet must share one account_key: %q vs %q", a.ArcusVol.AccountKey, b.ArcusVol.AccountKey)
	}
	one := 1
	if arcusAccountKey(testArcusAddr, 0) == arcusAccountKey(testArcusAddr, one) {
		t.Fatal("a different subaccount must be a different account")
	}
	// The address never reaches the API payload.
	out, _ := json.Marshal(a)
	if strings.Contains(strings.ToLower(string(out)), testArcusAddr[2:]) {
		t.Fatalf("address leaked into the status JSON: %s", out)
	}
	// After the TTL the wallet is read again.
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("SPY", testArcusAddr, nil), a, now.Add(arcusAccountTTL+time.Second))
	if calls != 2 {
		t.Fatalf("expected a refresh after the TTL, got %d calls", calls)
	}
}

func TestArcusAccountEquityErrorNeverZero(t *testing.T) {
	var body atomic.Value
	body.Store(`{"error":"this account has no activity yet"}`)
	var calls int32
	defer arcusAccountServer(t, &body, &calls)()
	now := time.Unix(1_800_000_000, 0)
	s := arcusStatus()
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("SPY", testArcusAddr, nil), s, now)
	if s.ArcusVol.AccountEquityUSD != nil || s.ArcusVol.AccountEquityError == "" {
		t.Fatalf("a read without equity must be an error, never $0: %+v", s.ArcusVol)
	}
	// A good reading, then a failure: the last good value is kept with its time.
	body.Store(`{"equity":"100.5"}`)
	s = arcusStatus()
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("SPY", testArcusAddr, nil), s, now.Add(arcusAccountErrorTTL+time.Second))
	if s.ArcusVol.AccountEquityUSD == nil || *s.ArcusVol.AccountEquityUSD != 100.5 {
		t.Fatalf("expected 100.5 after recovery: %+v", s.ArcusVol)
	}
	goodAt := s.ArcusVol.AccountEquityAtMs
	body.Store(`not json`)
	s = arcusStatus()
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("SPY", testArcusAddr, nil), s, now.Add(arcusAccountErrorTTL+arcusAccountTTL+2*time.Second))
	if s.ArcusVol.AccountEquityUSD == nil || *s.ArcusVol.AccountEquityUSD != 100.5 || s.ArcusVol.AccountEquityAtMs != goodAt || s.ArcusVol.AccountEquityError == "" {
		t.Fatalf("a failed refresh must keep the last good value + its time and report the error: %+v", s.ArcusVol)
	}
}

func TestArcusAccountNotConfigured(t *testing.T) {
	var body atomic.Value
	body.Store(`{"equity":"1"}`)
	var calls int32
	defer arcusAccountServer(t, &body, &calls)()
	s := arcusStatus()
	tg := arcusWalletTarget("AAPL", "", nil)
	tg.ArcusVol.EquityCountedBy = "Arcus Hold Test (AAPL)"
	applyArcusAccountEquity(context.Background(), http.DefaultClient, tg, s, time.Now())
	if calls != 0 || s.ArcusVol.AccountConfigured || s.ArcusVol.AccountEquityUSD != nil {
		t.Fatalf("no address: no venue read, nothing counted: calls=%d %+v", calls, s.ArcusVol)
	}
	if s.ArcusVol.EquityCountedBy != "Arcus Hold Test (AAPL)" {
		t.Fatalf("equity_counted_by not served: %+v", s.ArcusVol)
	}
}

func TestArcusAccountConfigValidation(t *testing.T) {
	bad := 256
	zero := 0
	cases := []struct {
		cfg ArcusVolConfig
		ok  bool
	}{
		{ArcusVolConfig{StatusPath: "/x", AccountAddress: testArcusAddr}, true},
		{ArcusVolConfig{StatusPath: "/x", AccountAddress: testArcusAddr, AccountIndex: &zero}, true},
		{ArcusVolConfig{StatusPath: "/x", AccountAddress: "0x1234"}, false},
		{ArcusVolConfig{StatusPath: "/x", AccountAddress: testArcusAddr, AccountIndex: &bad}, false},
		{ArcusVolConfig{StatusPath: "/x", AccountIndex: &zero}, false},
		{ArcusVolConfig{StatusPath: "/x", AccountAddress: testArcusAddr, EquityCountedBy: "X"}, false},
	}
	for i, c := range cases {
		if err := c.cfg.validate(); (err == nil) != c.ok {
			t.Errorf("case %d: ok=%v err=%v", i, c.ok, err)
		}
	}
}

func TestEquityCountedByMustNameAnotherTarget(t *testing.T) {
	mk := func(by string) Config {
		return Config{Region: "ap-northeast-1", Targets: []TargetConfig{
			{Name: "Arcus Hold Test (AAPL)", Service: "debot-xvenue-hedge-arcus", S3Bucket: "b", S3Key: "k"},
			{Name: "Arcus presence (AAPL)", Service: "debot-arcus-vol-aapl", Bucket: "subsidy", ArcusVol: &ArcusVolConfig{StatusPath: "/x", EquityCountedBy: by}},
		}}
	}
	ok := mk("Arcus Hold Test (AAPL)")
	if err := normalizeConfig(&ok); err != nil {
		t.Fatalf("valid equity_counted_by refused: %v", err)
	}
	for _, by := range []string{"Arcus Hold Test (typo)", "Arcus presence (AAPL)"} {
		c := mk(by)
		if err := normalizeConfig(&c); err == nil {
			t.Fatalf("equity_counted_by %q must be refused", by)
		}
	}
}

func TestArcusAccountReadIsBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	prevURL := arcusAPIBaseURL
	arcusAPIBaseURL = srv.URL
	defer func() { arcusAPIBaseURL = prevURL }()
	defer arcusAccountCacheForTest()()
	start := time.Now()
	s := arcusStatus()
	applyArcusAccountEquity(context.Background(), http.DefaultClient, arcusWalletTarget("SPY", testArcusAddr, nil), s, time.Now())
	if took := time.Since(start); took > arcusAccountTimeout+2*time.Second {
		t.Fatalf("a stalled venue read must give up after the timeout, took %s", took)
	}
	if s.ArcusVol.AccountEquityUSD != nil || s.ArcusVol.AccountEquityError == "" {
		t.Fatalf("a timed-out read is an error, never a value: %+v", s.ArcusVol)
	}
}
