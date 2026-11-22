package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	tPool     = "0x00000000000000000000000000000000000000a1"
	tProvider = "0x00000000000000000000000000000000000000a2"
	tOracle   = "0x00000000000000000000000000000000000000a3"
	tUSDC     = "0x00000000000000000000000000000000000000b1"
	tBTC      = "0x00000000000000000000000000000000000000b2"
	tWETH     = "0x00000000000000000000000000000000000000b3" // listed, unused
	tAUSDC    = "0x00000000000000000000000000000000000000c1"
	tDUSDC    = "0x00000000000000000000000000000000000000c2"
	tABTC     = "0x00000000000000000000000000000000000000c3"
	tDBTC     = "0x00000000000000000000000000000000000000c4"
	tUser1    = "0x1111111111111111111111111111111111111111"
	tUser2    = "0x2222222222222222222222222222222222222222"
)

func w(v *big.Int) string { return fmt.Sprintf("%064x", v) }
func wi(v int64) string   { return w(big.NewInt(v)) }
func wa(a string) string  { return strings.Repeat("0", 24) + strings.TrimPrefix(a, "0x") }
func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
func scaled(f float64, dec int) *big.Int {
	v, _ := new(big.Float).Mul(big.NewFloat(f), new(big.Float).SetInt(pow10(dec))).Int(nil)
	return v
}
func abiString(s string) string {
	b := hex.EncodeToString([]byte(s))
	return wi(32) + wi(int64(len(s))) + b + strings.Repeat("0", 64-len(b)%64)
}

// fakeAave answers the Pool / oracle / token calls readAaveAccounts makes.
// user1: 4 BTC collateral at $60,000, 150,000 USDC debt, e-mode LT 85 %.
// user2: no positions.
func fakeAave(t *testing.T, fail *atomic.Bool, calls *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(rw, "down", http.StatusBadGateway)
			return
		}
		var reqs []struct {
			ID     int               `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
			t.Fatalf("batch decode: %v", err)
		}
		type resp struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Result  string `json:"result"`
		}
		var out []resp
		for _, q := range reqs {
			var c struct{ To, Data string }
			_ = json.Unmarshal(q.Params[0], &c)
			to := strings.ToLower(c.To)
			sel := c.Data[2:10]
			arg := ""
			if len(c.Data) >= 74 {
				arg = "0x" + c.Data[34:74]
			}
			res := ""
			switch {
			case to == tPool && sel == selGetReservesList:
				res = wi(32) + wi(3) + wa(tUSDC) + wa(tBTC) + wa(tWETH)
			case to == tPool && sel == selAddressesProvider:
				res = wa(tProvider)
			case to == tPool && sel == selGetUserAccountData && arg == tUser1:
				// HF = 240000·0.85 / 150000 = 1.36
				res = w(scaled(240000, 8)) + w(scaled(150000, 8)) + w(scaled(46800, 8)) + wi(8500) + wi(8200) + w(scaled(1.36, 18))
			case to == tPool && sel == selGetUserAccountData && arg == tUser2:
				max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
				res = wi(0) + wi(0) + wi(0) + wi(0) + wi(0) + w(max)
			case to == tPool && sel == selGetUserConfiguration && arg == tUser1:
				// USDC (index 0) borrowed → bit 0; BTC (index 1) collateral → bit 3.
				res = wi(1 | 1<<3)
			case to == tPool && sel == selGetUserConfiguration:
				res = wi(0)
			case to == tPool && sel == selGetUserEMode:
				if arg == tUser1 {
					res = wi(10)
				} else {
					res = wi(0)
				}
			case to == tProvider && sel == selGetPriceOracle:
				res = wa(tOracle)
			case to == tPool && sel == selGetReserveData:
				dec, a, d, rate := int64(6), tAUSDC, tDUSDC, scaled(0.06, 27)
				if arg == tBTC {
					dec, a, d, rate = 8, tABTC, tDBTC, scaled(0.006, 27)
				}
				conf := new(big.Int).Lsh(big.NewInt(dec), 48)
				res = w(conf) + wi(0) + w(scaled(0.05, 27)) + wi(0) + w(rate) + wi(0) + wi(0) + wi(0) + wa(a) + wi(0) + wa(d) + wi(0)
			case to == tOracle && sel == selGetAssetPrice:
				if arg == tBTC {
					res = w(scaled(60000, 8))
				} else {
					res = w(scaled(1, 8))
				}
			case sel == selSymbol && to == tUSDC:
				res = abiString("USDC")
			case sel == selSymbol && to == tBTC:
				res = abiString("cbBTC")
			case sel == selBalanceOf && to == tABTC && arg == tUser1:
				res = w(scaled(4, 8))
			case sel == selBalanceOf && to == tDUSDC && arg == tUser1:
				res = w(scaled(150000, 6))
			case sel == selBalanceOf:
				res = wi(0)
			default:
				t.Fatalf("unexpected call to %s sel %s", to, sel)
			}
			out = append(out, resp{JSONRPC: "2.0", ID: q.ID, Result: "0x" + res})
		}
		_ = json.NewEncoder(rw).Encode(out)
	}))
}

func testAaveConfig(url string) *AaveConfig {
	c := &AaveConfig{RPCURL: url, Pool: tPool, Accounts: []AaveAccountConfig{
		{Label: "main", Address: tUser1},
		{Address: tUser2},
	}}
	if err := c.normalize(); err != nil {
		panic(err)
	}
	return c
}

func TestAaveReadsPositionsAndLiquidationPrice(t *testing.T) {
	defer aaveCacheForTest()()
	var fail atomic.Bool
	var calls atomic.Int64
	srv := fakeAave(t, &fail, &calls)
	defer srv.Close()
	cfg := testAaveConfig(srv.URL)
	now := time.Unix(1_800_000_000, 0)

	s := fetchAaveStatus(context.Background(), srv.Client(), cfg, now)
	if s == nil || len(s.Accounts) != 2 {
		t.Fatalf("status: %+v", s)
	}
	a := s.Accounts[0]
	if a.Error != "" || a.Label != "main" || a.Address != "0x1111…1111" {
		t.Fatalf("account: %+v", a)
	}
	if a.HealthFactor == nil || math.Abs(*a.HealthFactor-1.36) > 1e-9 || a.EModeID != 10 || a.LiqThreshold != 0.85 {
		t.Fatalf("totals: %+v", a)
	}
	if calls.Load() != 3 {
		t.Fatalf("want 3 batched round trips, got %d", calls.Load())
	}
	var btc, usdc *AaveAsset
	for i := range a.Assets {
		switch a.Assets[i].Symbol {
		case "cbBTC":
			btc = &a.Assets[i]
		case "USDC":
			usdc = &a.Assets[i]
		}
	}
	if btc == nil || usdc == nil || len(a.Assets) != 2 {
		t.Fatalf("assets: %+v", a.Assets)
	}
	if btc.Supplied != 4 || !btc.Collateral || usdc.Borrowed != 150000 || usdc.Collateral {
		t.Fatalf("balances: %+v %+v", btc, usdc)
	}
	if math.Abs(usdc.BorrowAPR-0.06) > 1e-12 {
		t.Fatalf("borrow apr %v", usdc.BorrowAPR)
	}
	// 4 · P · 0.85 = 150000 → P = 44117.647…
	if btc.LiqPriceUSD == nil || math.Abs(*btc.LiqPriceUSD-150000/(4*0.85)) > 1e-6 {
		t.Fatalf("liq price %v", btc.LiqPriceUSD)
	}
	if usdc.LiqPriceUSD != nil {
		t.Fatalf("debt asset has no liquidation price")
	}
	b := s.Accounts[1]
	if b.HealthFactor != nil || b.Label != "0x2222…2222" || len(b.Assets) != 0 {
		t.Fatalf("no-debt account: %+v", b)
	}

	// Within the TTL: served from cache.
	fetchAaveStatus(context.Background(), srv.Client(), cfg, now.Add(30*time.Second))
	if calls.Load() != 3 {
		t.Fatalf("cache miss inside TTL: %d calls", calls.Load())
	}

	// RPC down after the TTL: last good reading kept, with the error and
	// its original timestamp.
	fail.Store(true)
	s = fetchAaveStatus(context.Background(), srv.Client(), cfg, now.Add(2*time.Minute))
	a = s.Accounts[0]
	if a.Error == "" || a.HealthFactor == nil || a.AtMs != now.UnixMilli() {
		t.Fatalf("stale reading: %+v", a)
	}
	if strings.Contains(a.Error, srv.URL) {
		t.Fatalf("error leaks the RPC URL: %q", a.Error)
	}
}

func TestAaveConfigValidation(t *testing.T) {
	for name, c := range map[string]AaveConfig{
		"empty":     {},
		"bad addr":  {Accounts: []AaveAccountConfig{{Address: "0x123"}}},
		"duplicate": {Accounts: []AaveAccountConfig{{Address: tUser1}, {Address: strings.ToUpper(tUser1[:2]) + tUser1[2:]}}},
		"bad pool":  {Pool: "pool", Accounts: []AaveAccountConfig{{Address: tUser1}}},
		"bad rpc":   {RPCURL: "ftp://x", Accounts: []AaveAccountConfig{{Address: tUser1}}},
	} {
		c := c
		if err := c.normalize(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	c := AaveConfig{Accounts: []AaveAccountConfig{{Address: tUser1}}}
	if err := c.normalize(); err != nil || c.Chain != "Base" || c.Pool != aaveDefaultPool || c.RPCURL != aaveDefaultRPCURL {
		t.Fatalf("defaults: %+v %v", c, err)
	}
}

func TestAaveLiqPriceTwoCollaterals(t *testing.T) {
	// Two collaterals at the same threshold: moving only one of them.
	hf := 1.7
	st := AaveAccountStatus{CollateralUSD: 200, DebtUSD: 100, LiqThreshold: 0.85, HealthFactor: &hf, Assets: []AaveAsset{
		{Symbol: "X", PriceUSD: 10, Supplied: 10, SuppliedUSD: 100, Collateral: true},
		{Symbol: "Y", PriceUSD: 1, Supplied: 100, SuppliedUSD: 100, Collateral: true},
	}}
	applyAaveLiqPrices(&st)
	// (85 + k·85) = 100 → k = 15/85
	want := 10 * 15.0 / 85.0
	if st.Assets[0].LiqPriceUSD == nil || math.Abs(*st.Assets[0].LiqPriceUSD-want) > 1e-9 {
		t.Fatalf("X liq %v want %v", st.Assets[0].LiqPriceUSD, want)
	}
	// Plenty of collateral elsewhere: X alone cannot liquidate.
	st2 := AaveAccountStatus{CollateralUSD: 200, DebtUSD: 50, LiqThreshold: 0.85, HealthFactor: &hf, Assets: []AaveAsset{
		{Symbol: "X", PriceUSD: 10, Supplied: 10, SuppliedUSD: 100, Collateral: true},
		{Symbol: "Y", PriceUSD: 1, Supplied: 100, SuppliedUSD: 100, Collateral: true},
	}}
	applyAaveLiqPrices(&st2)
	if st2.Assets[0].LiqPriceUSD != nil {
		t.Fatalf("X alone cannot liquidate, got %v", *st2.Assets[0].LiqPriceUSD)
	}
}
