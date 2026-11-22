package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Aave v3 borrow positions panel (owner request 2026-11-22: "when can it be
// liquidated?"). The owner holds collateral + debt on Aave v3 outside any
// bot, so there is no status.json to mirror: the dashboard reads the Pool
// itself over a public JSON-RPC endpoint (eth_call only, no key, no
// signature). Addresses live only in the host's config.yaml — this repo is
// public — and the API serves a shortened form, never the full address.

const (
	aaveDefaultChain  = "Base"
	aaveDefaultRPCURL = "https://base-rpc.publicnode.com"
	// Aave v3 Pool on Base.
	aaveDefaultPool = "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"

	aaveTTL      = 60 * time.Second
	aaveErrorTTL = 30 * time.Second
	aaveTimeout  = 10 * time.Second
)

var aaveAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// AaveConfig is the optional top-level `aave:` block. No block, no panel.
type AaveConfig struct {
	Chain    string              `yaml:"chain"`
	RPCURL   string              `yaml:"rpc_url"`
	Pool     string              `yaml:"pool"`
	Accounts []AaveAccountConfig `yaml:"accounts"`
}

type AaveAccountConfig struct {
	Label   string `yaml:"label"`
	Address string `yaml:"address"`
}

func (c *AaveConfig) normalize() error {
	if c.Chain == "" {
		c.Chain = aaveDefaultChain
	}
	if c.RPCURL == "" {
		c.RPCURL = aaveDefaultRPCURL
	}
	if !strings.HasPrefix(c.RPCURL, "https://") && !strings.HasPrefix(c.RPCURL, "http://") {
		return errors.New("aave.rpc_url must be an http(s) URL")
	}
	if c.Pool == "" {
		c.Pool = aaveDefaultPool
	}
	if !aaveAddressRe.MatchString(c.Pool) {
		return errors.New("aave.pool must be a 0x-prefixed 40-hex-digit address")
	}
	if len(c.Accounts) == 0 {
		return errors.New("aave.accounts is empty")
	}
	seen := map[string]bool{}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		a.Label = strings.TrimSpace(a.Label)
		if !aaveAddressRe.MatchString(a.Address) {
			return fmt.Errorf("aave.accounts[%d].address must be a 0x-prefixed 40-hex-digit address", i)
		}
		key := strings.ToLower(a.Address)
		if seen[key] {
			return fmt.Errorf("aave.accounts[%d]: duplicate address", i)
		}
		seen[key] = true
		if a.Label == "" {
			a.Label = aaveShortAddress(a.Address)
		}
	}
	return nil
}

func aaveShortAddress(address string) string {
	if len(address) < 10 {
		return address
	}
	return address[:6] + "…" + address[len(address)-4:]
}

// AaveAsset is one reserve the account supplies or borrows.
type AaveAsset struct {
	Symbol      string  `json:"symbol"`
	PriceUSD    float64 `json:"price_usd"`
	Supplied    float64 `json:"supplied"`
	SuppliedUSD float64 `json:"supplied_usd"`
	Borrowed    float64 `json:"borrowed"`
	BorrowedUSD float64 `json:"borrowed_usd"`
	// Collateral: the supply counts toward the health factor.
	Collateral bool `json:"collateral"`
	// APRs as fractions (the Pool's ray rates, not compounded).
	SupplyAPR float64 `json:"supply_apr"`
	BorrowAPR float64 `json:"borrow_apr"`
	// LiqPriceUSD is the price at which this collateral alone takes the
	// health factor to 1 with every other price unchanged; nil when the
	// account has no debt or this asset alone cannot get it there.
	LiqPriceUSD *float64 `json:"liq_price_usd,omitempty"`
}

type AaveAccountStatus struct {
	Label   string `json:"label"`
	Address string `json:"address"` // shortened
	// Account totals in USD (the Pool's base currency, 8 decimals).
	CollateralUSD      float64 `json:"collateral_usd"`
	DebtUSD            float64 `json:"debt_usd"`
	AvailableBorrowUSD float64 `json:"available_borrow_usd"`
	// Weighted liquidation threshold and LTV, fractions.
	LiqThreshold float64 `json:"liq_threshold"`
	LTV          float64 `json:"ltv"`
	// HealthFactor is nil when the account has no debt (the Pool returns
	// uint256 max); liquidatable below 1.
	HealthFactor *float64    `json:"health_factor,omitempty"`
	EModeID      int64       `json:"emode_id"`
	Assets       []AaveAsset `json:"assets"`
	// AtMs is when this reading was taken; Error is the last refresh's
	// failure (the previous reading is kept, so AtMs then lags).
	AtMs  int64  `json:"at_ms"`
	Error string `json:"error,omitempty"`
}

type AaveStatus struct {
	Chain    string              `json:"chain"`
	Accounts []AaveAccountStatus `json:"accounts"`
}

// --- cache ---

type aaveCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	failed    bool
	accounts  map[string]AaveAccountStatus // lower-case address → last good
	errs      map[string]string
	symbols   map[string]string // asset → symbol (never changes)
}

var aaveState = &aaveCache{}

func aaveCacheForTest() func() {
	prev := aaveState
	aaveState = &aaveCache{}
	return func() { aaveState = prev }
}

// fetchAaveStatus returns the panel's data, refreshing at most once per TTL.
// The lock is held across the refresh so a page load racing the poll makes
// one set of RPC calls.
func fetchAaveStatus(ctx context.Context, client *http.Client, cfg *AaveConfig, now time.Time) *AaveStatus {
	if cfg == nil {
		return nil
	}
	c := aaveState
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accounts == nil {
		c.accounts = map[string]AaveAccountStatus{}
		c.errs = map[string]string{}
		c.symbols = map[string]string{}
	}
	ttl := aaveTTL
	if c.failed {
		ttl = aaveErrorTTL
	}
	if c.fetchedAt.IsZero() || now.Sub(c.fetchedAt) >= ttl {
		c.fetchedAt = now
		fetchCtx, cancel := context.WithTimeout(ctx, aaveTimeout)
		readings, err := readAaveAccounts(fetchCtx, client, cfg, c.symbols)
		cancel()
		c.failed = err != nil
		for _, a := range cfg.Accounts {
			key := strings.ToLower(a.Address)
			if err != nil {
				c.errs[key] = err.Error()
				continue
			}
			r := readings[key]
			r.AtMs = now.UnixMilli()
			c.accounts[key] = r
			delete(c.errs, key)
		}
	}
	out := &AaveStatus{Chain: cfg.Chain, Accounts: make([]AaveAccountStatus, 0, len(cfg.Accounts))}
	for _, a := range cfg.Accounts {
		key := strings.ToLower(a.Address)
		r, ok := c.accounts[key]
		if !ok {
			r = AaveAccountStatus{Assets: []AaveAsset{}}
		}
		r.Label = a.Label
		r.Address = aaveShortAddress(a.Address)
		r.Error = c.errs[key]
		out.Accounts = append(out.Accounts, r)
	}
	return out
}

// --- JSON-RPC ---

const (
	selGetUserAccountData   = "bf92857c"
	selGetUserConfiguration = "4417a583"
	selGetUserEMode         = "eddf1b79"
	selGetReservesList      = "d1946dbc"
	selAddressesProvider    = "0542975c"
	selGetPriceOracle       = "fca513a8"
	selGetReserveData       = "35ea6a75"
	selGetAssetPrice        = "b3596f07"
	selBalanceOf            = "70a08231"
	selSymbol               = "95d89b41"
)

type aaveCall struct {
	to   string
	data string
}

func aaveAddrArg(address string) string {
	return strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(address, "0x"))
}

func aaveCallWithAddr(to, sel, address string) aaveCall {
	return aaveCall{to: to, data: "0x" + sel + aaveAddrArg(address)}
}

// aaveBatch runs eth_calls as one JSON-RPC batch and returns the hex results
// (without 0x) in call order.
func aaveBatch(ctx context.Context, client *http.Client, url string, calls []aaveCall) ([]string, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	type rpcReq struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}
	reqs := make([]rpcReq, len(calls))
	for i, c := range calls {
		reqs[i] = rpcReq{JSONRPC: "2.0", ID: i, Method: "eth_call", Params: []any{map[string]string{"to": c.to, "data": c.data}, "latest"}}
	}
	var resp []struct {
		ID     int     `json:"id"`
		Result *string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := holderJSON(ctx, client, url, reqs, &resp); err != nil {
		return nil, errors.New("Aave RPC unavailable")
	}
	out := make([]string, len(calls))
	got := make([]bool, len(calls))
	for _, r := range resp {
		if r.ID < 0 || r.ID >= len(calls) {
			continue
		}
		if r.Error != nil || r.Result == nil {
			return nil, errors.New("Aave RPC call failed")
		}
		out[r.ID] = strings.TrimPrefix(*r.Result, "0x")
		got[r.ID] = true
	}
	for _, ok := range got {
		if !ok {
			return nil, errors.New("Aave RPC response incomplete")
		}
	}
	return out, nil
}

func aaveWord(h string, i int) (*big.Int, error) {
	if len(h) < (i+1)*64 {
		return nil, errors.New("Aave RPC result too short")
	}
	v, ok := new(big.Int).SetString(h[i*64:(i+1)*64], 16)
	if !ok {
		return nil, errors.New("Aave RPC result not hex")
	}
	return v, nil
}

func aaveWordAddr(h string, i int) (string, error) {
	if len(h) < (i+1)*64 {
		return "", errors.New("Aave RPC result too short")
	}
	return "0x" + h[i*64+24:(i+1)*64], nil
}

// aaveScaled is v / 10^decimals as a float.
func aaveScaled(v *big.Int, decimals int) float64 {
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), new(big.Float).SetFloat64(math.Pow10(decimals))).Float64()
	return f
}

func aaveDecodeString(h string) string {
	// ABI dynamic string: offset, length, bytes.
	off, err := aaveWord(h, 0)
	if err != nil || !off.IsInt64() || off.Int64()%32 != 0 {
		return ""
	}
	base := int(off.Int64() / 32)
	n, err := aaveWord(h, base)
	if err != nil || !n.IsInt64() || n.Int64() > 64 {
		return ""
	}
	start := (base + 1) * 64
	end := start + int(n.Int64())*2
	if len(h) < end {
		return ""
	}
	b, err := hex.DecodeString(h[start:end])
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type aaveReserveInfo struct {
	asset        string
	decimals     int
	aToken       string
	variableDebt string
	supplyAPR    float64
	borrowAPR    float64
}

var aaveRay = new(big.Float).SetFloat64(1e27)

func aaveRayToFloat(v *big.Int) float64 {
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), aaveRay).Float64()
	return f
}

// readAaveAccounts reads every configured account in three batched round
// trips: (1) account data, user configuration bitmap, e-mode, reserves list
// and the addresses provider; (2) the price oracle and the ReserveData of
// every reserve an account uses; (3) balances, prices and symbols.
func readAaveAccounts(ctx context.Context, client *http.Client, cfg *AaveConfig, symbols map[string]string) (map[string]AaveAccountStatus, error) {
	pool := cfg.Pool
	calls := []aaveCall{{to: pool, data: "0x" + selGetReservesList}, {to: pool, data: "0x" + selAddressesProvider}}
	for _, a := range cfg.Accounts {
		calls = append(calls,
			aaveCallWithAddr(pool, selGetUserAccountData, a.Address),
			aaveCallWithAddr(pool, selGetUserConfiguration, a.Address),
			aaveCallWithAddr(pool, selGetUserEMode, a.Address),
		)
	}
	res, err := aaveBatch(ctx, client, cfg.RPCURL, calls)
	if err != nil {
		return nil, err
	}
	n, err := aaveWord(res[0], 1)
	if err != nil || !n.IsInt64() || n.Int64() > 256 {
		return nil, errors.New("Aave reserves list unreadable")
	}
	reserves := make([]string, n.Int64())
	for i := range reserves {
		if reserves[i], err = aaveWordAddr(res[0], 2+i); err != nil {
			return nil, err
		}
	}
	provider, err := aaveWordAddr(res[1], 0)
	if err != nil {
		return nil, err
	}

	type pending struct {
		status AaveAccountStatus
		used   []int // reserve indexes in use
		coll   map[int]bool
	}
	accts := make([]pending, len(cfg.Accounts))
	usedAny := map[int]bool{}
	for k := range cfg.Accounts {
		ad, cf, em := res[2+3*k], res[3+3*k], res[4+3*k]
		w := make([]*big.Int, 6)
		for i := range w {
			if w[i], err = aaveWord(ad, i); err != nil {
				return nil, err
			}
		}
		st := AaveAccountStatus{
			CollateralUSD:      aaveScaled(w[0], 8),
			DebtUSD:            aaveScaled(w[1], 8),
			AvailableBorrowUSD: aaveScaled(w[2], 8),
			LiqThreshold:       aaveScaled(w[3], 4),
			LTV:                aaveScaled(w[4], 4),
			Assets:             []AaveAsset{},
		}
		if w[1].Sign() > 0 {
			hf := aaveScaled(w[5], 18)
			st.HealthFactor = &hf
		}
		if e, err := aaveWord(em, 0); err == nil && e.IsInt64() {
			st.EModeID = e.Int64()
		}
		bitmap, err := aaveWord(cf, 0)
		if err != nil {
			return nil, err
		}
		p := pending{status: st, coll: map[int]bool{}}
		for i := range reserves {
			borrowing := bitmap.Bit(2*i) == 1
			collateral := bitmap.Bit(2*i+1) == 1
			if borrowing || collateral {
				p.used = append(p.used, i)
				p.coll[i] = collateral
				usedAny[i] = true
			}
		}
		accts[k] = p
	}

	// Round 2: oracle + reserve data.
	usedIdx := make([]int, 0, len(usedAny))
	for i := range usedAny {
		usedIdx = append(usedIdx, i)
	}
	sort.Ints(usedIdx)
	calls = []aaveCall{{to: provider, data: "0x" + selGetPriceOracle}}
	for _, i := range usedIdx {
		calls = append(calls, aaveCallWithAddr(pool, selGetReserveData, reserves[i]))
	}
	res, err = aaveBatch(ctx, client, cfg.RPCURL, calls)
	if err != nil {
		return nil, err
	}
	oracle, err := aaveWordAddr(res[0], 0)
	if err != nil {
		return nil, err
	}
	info := map[int]aaveReserveInfo{}
	for j, i := range usedIdx {
		h := res[1+j]
		conf, err := aaveWord(h, 0)
		if err != nil {
			return nil, err
		}
		liq, err := aaveWord(h, 2)
		if err != nil {
			return nil, err
		}
		vb, err := aaveWord(h, 4)
		if err != nil {
			return nil, err
		}
		aToken, err := aaveWordAddr(h, 8)
		if err != nil {
			return nil, err
		}
		vDebt, err := aaveWordAddr(h, 10)
		if err != nil {
			return nil, err
		}
		dec := new(big.Int).And(new(big.Int).Rsh(conf, 48), big.NewInt(0xff)).Int64()
		info[i] = aaveReserveInfo{asset: reserves[i], decimals: int(dec), aToken: aToken, variableDebt: vDebt,
			supplyAPR: aaveRayToFloat(liq), borrowAPR: aaveRayToFloat(vb)}
	}

	// Round 3: prices, symbols, balances.
	calls = nil
	priceAt := map[int]int{}
	symbolAt := map[int]int{}
	for _, i := range usedIdx {
		priceAt[i] = len(calls)
		calls = append(calls, aaveCallWithAddr(oracle, selGetAssetPrice, reserves[i]))
		if _, ok := symbols[strings.ToLower(reserves[i])]; !ok {
			symbolAt[i] = len(calls)
			calls = append(calls, aaveCall{to: reserves[i], data: "0x" + selSymbol})
		}
	}
	type balRef struct{ k, i, supply, debt int }
	var refs []balRef
	for k, a := range cfg.Accounts {
		for _, i := range accts[k].used {
			r := balRef{k: k, i: i, supply: len(calls)}
			calls = append(calls, aaveCallWithAddr(info[i].aToken, selBalanceOf, a.Address))
			r.debt = len(calls)
			calls = append(calls, aaveCallWithAddr(info[i].variableDebt, selBalanceOf, a.Address))
			refs = append(refs, r)
		}
	}
	res, err = aaveBatch(ctx, client, cfg.RPCURL, calls)
	if err != nil {
		return nil, err
	}
	prices := map[int]float64{}
	for _, i := range usedIdx {
		p, err := aaveWord(res[priceAt[i]], 0)
		if err != nil {
			return nil, err
		}
		prices[i] = aaveScaled(p, 8)
		if at, ok := symbolAt[i]; ok {
			if s := aaveDecodeString(res[at]); s != "" {
				symbols[strings.ToLower(reserves[i])] = s
			}
		}
	}
	for _, r := range refs {
		s, err := aaveWord(res[r.supply], 0)
		if err != nil {
			return nil, err
		}
		d, err := aaveWord(res[r.debt], 0)
		if err != nil {
			return nil, err
		}
		in := info[r.i]
		sym := symbols[strings.ToLower(in.asset)]
		if sym == "" {
			sym = aaveShortAddress(in.asset)
		}
		asset := AaveAsset{
			Symbol:     sym,
			PriceUSD:   prices[r.i],
			Supplied:   aaveScaled(s, in.decimals),
			Borrowed:   aaveScaled(d, in.decimals),
			Collateral: accts[r.k].coll[r.i],
			SupplyAPR:  in.supplyAPR,
			BorrowAPR:  in.borrowAPR,
		}
		asset.SuppliedUSD = asset.Supplied * asset.PriceUSD
		asset.BorrowedUSD = asset.Borrowed * asset.PriceUSD
		if asset.Supplied == 0 && asset.Borrowed == 0 {
			continue
		}
		accts[r.k].status.Assets = append(accts[r.k].status.Assets, asset)
	}

	out := map[string]AaveAccountStatus{}
	for k, a := range cfg.Accounts {
		st := accts[k].status
		applyAaveLiqPrices(&st)
		out[strings.ToLower(a.Address)] = st
	}
	return out, nil
}

// applyAaveLiqPrices sets each collateral asset's liquidation price: the
// price at which, all else unchanged, the health factor reaches 1.
//
// HF = W / D with W = Σ collateral_j · price_j · LT_j. The Pool reports W
// as collateral × the weighted LT; asset i's share of W is approximated by
// its share of the collateral value (exact with one collateral asset, or
// when every collateral has the same threshold, e.g. inside one e-mode).
// Moving price_i by a factor k gives HF = (W − w_i + k·w_i) / D' where D'
// also moves if the account borrows the same asset; HF = 1 solves for k.
func applyAaveLiqPrices(st *AaveAccountStatus) {
	if st.DebtUSD <= 0 || st.CollateralUSD <= 0 || st.HealthFactor == nil {
		return
	}
	weighted := st.CollateralUSD * st.LiqThreshold
	for j := range st.Assets {
		a := &st.Assets[j]
		if !a.Collateral || a.SuppliedUSD <= 0 || a.PriceUSD <= 0 {
			continue
		}
		wi := weighted * a.SuppliedUSD / st.CollateralUSD
		// (weighted − wi + k·wi) = (D − b_i + k·b_i)
		denom := wi - a.BorrowedUSD
		if denom <= 0 {
			continue
		}
		k := (st.DebtUSD - a.BorrowedUSD - (weighted - wi)) / denom
		if k <= 0 {
			continue // this asset alone cannot liquidate the account
		}
		p := a.PriceUSD * k
		a.LiqPriceUSD = &p
	}
}
