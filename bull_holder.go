package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Public identifiers only. Never source the bot's credential environment.
type BullHolderConfig struct {
	StatusPath   string            `yaml:"status_path"`
	HLAddress    string            `yaml:"hyperliquid_address"`
	LighterIndex string            `yaml:"lighter_account_index"`
	Investment   *HolderInvestment `yaml:"investment"`
}

func (c BullHolderConfig) validate() error {
	if c.Investment != nil {
		if err := c.Investment.validate(); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(c.StatusPath) {
		return errors.New("bull_holder.status_path must be absolute")
	}
	if c.HLAddress != "" && !regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`).MatchString(c.HLAddress) {
		return errors.New("invalid Hyperliquid address")
	}
	if c.LighterIndex != "" {
		if _, err := strconv.ParseUint(c.LighterIndex, 10, 64); err != nil {
			return errors.New("invalid Lighter account index")
		}
	}
	return nil
}

// Explicit producer allowlist: never forward account identity or signing data.
type BullHolderStatus struct {
	ConfigFP          string                   `json:"config_fp"`
	Investment        *HolderInvestment        `json:"investment"`
	InvestmentError   string                   `json:"investment_error,omitempty"`
	UnrealizedPnL     *float64                 `json:"unrealized_pnl_usdc"`
	Mode              string                   `json:"mode"`
	ArmedAt           *int64                   `json:"armed_at"`
	ExitedAt          *int64                   `json:"exited_at"`
	ExitReason        *string                  `json:"exit_reason"`
	Halted            bool                     `json:"halted"`
	HaltReason        *string                  `json:"halt_reason"`
	KillSwitch        bool                     `json:"kill_switch"`
	TranchesDone      uint32                   `json:"tranches_done"`
	TranchesRemaining uint32                   `json:"tranches_remaining"`
	TrancheSpotUSD    float64                  `json:"tranche_spot_usd"`
	TranchePerpUSD    float64                  `json:"tranche_perp_usd"`
	LastTrancheDate   *string                  `json:"last_tranche_date"`
	Legs              map[string]BullHolderLeg `json:"legs"`
	Pending           map[string]bool          `json:"pending"`
	PendingAdd        *uint32                  `json:"pending_add"`
	OperatorError     string                   `json:"operator_error,omitempty"`
	HL                HolderAccount            `json:"hyperliquid"`
	Lighter           HolderAccount            `json:"lighter"`
	TotalEquity       *float64                 `json:"total_equity_usdc"`
}
type BullHolderLeg struct {
	SpotSize      float64  `json:"spot_size"`
	PerpSize      float64  `json:"perp_size"`
	PeakClose     float64  `json:"peak_close"`
	LastClose     *float64 `json:"last_close"`
	LastCloseDate *string  `json:"last_close_date"`
	ExitLevel     float64  `json:"exit_level"`
	StopLevel     *float64 `json:"stop_level"`
	StopSize      *float64 `json:"stop_size"`
}
type HolderAccount struct {
	UnrealizedPnL *float64 `json:"unrealized_pnl_usdc"`
	ObservedAt    *int64   `json:"observed_at"`
	Error         string   `json:"error,omitempty"`
	// HL spot value; Lighter venue equity includes perp PnL, NOT perp notional.
	Equity    *float64      `json:"equity_usdc"`
	USDC      *float64      `json:"usdc"`
	Available *float64      `json:"available_usdc"`
	Holdings  []HolderAsset `json:"holdings"`
}
type HolderAsset struct {
	CostBasis        *float64 `json:"cost_basis_usdc"`
	Symbol           string   `json:"symbol"`
	Size             float64  `json:"size"`
	Price            *float64 `json:"price_usdc"`
	Value            *float64 `json:"value_usdc"`
	UnrealizedPnL    *float64 `json:"unrealized_pnl_usdc"`
	LiquidationPrice *float64 `json:"liquidation_price"`
}

func number(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("missing or invalid numeric value")
	}
	return v, nil
}
func fetchBullHolder(ctx context.Context, target TargetConfig, client *http.Client) TargetStatus {
	r := TargetStatus{Name: target.Name, InstanceID: target.InstanceID, Service: target.Service, Region: target.Region, CheckedAt: time.Now(), ServiceStatus: "unknown"}
	b := &BullHolderStatus{}
	s := StatusData{BullHolder: b, Dex: "Hyperliquid spot + Lighter perp"}
	r.Status = &s
	payload, err := os.ReadFile(target.BullHolder.StatusPath)
	if err != nil {
		r.Error = "Bull-holder status unavailable"
	} else {
		var envelope struct {
			TS     int64  `json:"ts"`
			Bot    string `json:"bot"`
			DryRun *bool  `json:"dry_run"`
		}
		if json.Unmarshal(payload, &envelope) != nil || json.Unmarshal(payload, b) != nil || envelope.Bot != "bull_holder" || envelope.DryRun == nil || envelope.TS <= 0 || (b.Mode != "Off" && b.Mode != "On" && b.Mode != "Exited") {
			b = &BullHolderStatus{}
			s.BullHolder = b
			r.Error = "Invalid bull-holder status"
		} else {
			s.TS = envelope.TS
			s.DryRun = *envelope.DryRun
			s.UpdatedAt = time.Unix(s.TS, 0).UTC().Format(time.RFC3339)
			r.ServiceStatus = "active"
			if age := time.Now().Unix() - s.TS; age > s3StatusStaleSecs || age < -60 {
				r.ServiceStatus = "stale"
			}
		}
	}
	readHolderIntents(filepath.Dir(target.BullHolder.StatusPath), b)
	// Sentinel sampling can be newer than the last producer write, including
	// when the bot has stopped. Keep all header/fleet/detail consumers aligned.
	if b.Pending != nil || s.TS != 0 {
		kill := b.KillSwitch
		if b.Pending != nil {
			kill = b.Pending["KILL_SWITCH"]
		}
		b.KillSwitch = kill
		r.KillSwitchActive = &kill
		s.KillSwitchActive = kill
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); b.HL = fetchHLSpot(ctx, client, target.BullHolder.HLAddress) }()
	go func() { defer wg.Done(); b.Lighter = fetchLighterHolder(ctx, client, target.BullHolder.LighterIndex) }()
	wg.Wait()
	// Never trust derived account/budget values supplied by the producer.
	b.Investment, b.InvestmentError = verifiedHolderInvestment(target.BullHolder.Investment, b.ConfigFP, s.TS)
	b.UnrealizedPnL = sumHolderPnL(b.HL, b.Lighter)
	// Clear any value a future producer might supply before deriving the total.
	b.TotalEquity = nil
	if b.HL.Equity != nil && b.Lighter.Equity != nil {
		total := *b.HL.Equity + *b.Lighter.Equity
		b.TotalEquity = &total
	}
	return r
}
func readHolderIntents(dir string, b *BullHolderStatus) {
	b.Pending = nil
	b.PendingAdd = nil
	b.OperatorError = ""
	entries, err := os.ReadDir(dir)
	if err != nil {
		b.OperatorError = "Operator requests unavailable"
		return
	}
	b.Pending = map[string]bool{"ARM": false, "ADD": false, "DISARM": false, "RISK_ACK": false, "KILL_SWITCH": false}
	for _, entry := range entries {
		if _, ok := b.Pending[entry.Name()]; ok {
			b.Pending[entry.Name()] = true
		}
	}
	if b.Pending["ADD"] {
		path := filepath.Join(dir, "ADD")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64 {
			b.OperatorError = "ADD amount unavailable"
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			b.OperatorError = "ADD amount unavailable"
			return
		}
		v := strings.TrimSpace(string(data))
		if v == "" {
			v = "1"
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 {
			b.OperatorError = "Invalid pending ADD amount"
			return
		}
		count := uint32(n)
		b.PendingAdd = &count
	}
}
func holderJSON(ctx context.Context, client *http.Client, url string, body any, out any) error {
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return errors.New("request unavailable")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	// Do not expose URLs containing account identifiers or upstream bodies.
	if err != nil {
		return errors.New("account API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("account API HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return errors.New("invalid account API response")
	}
	return nil
}

const hlInfoURL = "https://api.hyperliquid.xyz/info"

func fetchHLSpot(ctx context.Context, client *http.Client, address string) HolderAccount {
	a := HolderAccount{}
	if address == "" {
		a.Error = "Hyperliquid account not configured"
		return a
	}
	var balances struct {
		Balances *[]struct {
			Coin     string `json:"coin"`
			Token    int    `json:"token"`
			Total    string `json:"total"`
			Hold     string `json:"hold"`
			EntryNtl string `json:"entryNtl"`
		} `json:"balances"`
	}
	if err := holderJSON(ctx, client, hlInfoURL, map[string]string{"type": "spotClearinghouseState", "user": address}, &balances); err != nil {
		a.Error = err.Error()
		return a
	}
	if balances.Balances == nil {
		a.Error = "Missing spot balances"
		return a
	}
	var raw []json.RawMessage
	if err := holderJSON(ctx, client, hlInfoURL, map[string]string{"type": "spotMetaAndAssetCtxs"}, &raw); err != nil {
		a.Error = err.Error()
		return a
	}
	var meta struct {
		Tokens []struct {
			Index int    `json:"index"`
			Name  string `json:"name"`
		} `json:"tokens"`
		Universe []struct {
			Tokens []int  `json:"tokens"`
			Name   string `json:"name"`
		} `json:"universe"`
	}
	var prices []struct {
		MarkPx string `json:"markPx"`
		Coin   string `json:"coin"`
	}
	if len(raw) != 2 || json.Unmarshal(raw[0], &meta) != nil || json.Unmarshal(raw[1], &prices) != nil {
		a.Error = "Invalid spot price metadata"
		return a
	}
	usdcID := -1
	names := map[int]string{}
	for _, t := range meta.Tokens {
		names[t.Index] = t.Name
		if t.Name == "USDC" {
			usdcID = t.Index
		}
	}
	if usdcID < 0 {
		a.Error = "USDC metadata unavailable"
		return a
	}
	marks := map[int]float64{usdcID: 1}
	// Asset contexts may include delisted/sparse markets and need not have
	// the same length/order as universe. Join by canonical pair name.
	marketPrices := map[string]string{}
	for _, price := range prices {
		if price.Coin != "" {
			marketPrices[price.Coin] = price.MarkPx
		}
	}
	for _, pair := range meta.Universe {
		if len(pair.Tokens) == 2 && pair.Tokens[1] == usdcID {
			if mark, err := number(marketPrices[pair.Name]); err == nil && mark > 0 {
				marks[pair.Tokens[0]] = mark
			}
		}
	}
	usdc, available, equity := 0.0, 0.0, 0.0
	complete := true
	for _, balance := range *balances.Balances {
		size, err := number(balance.Total)
		if err != nil || names[balance.Token] != balance.Coin {
			a.Error = "Invalid spot balance"
			return a
		}
		if balance.Token == usdcID {
			hold, err := number(balance.Hold)
			if err != nil {
				a.Error = "Invalid USDC hold"
				return a
			}
			usdc = size
			available = size - hold
			equity += size
			continue
		}
		if size == 0 {
			continue
		}
		h := HolderAsset{Symbol: balance.Coin, Size: size}
		if mark, ok := marks[balance.Token]; ok {
			value := size * mark
			h.Price = &mark
			h.Value = &value
			// Venue entry notional is an estimate of basis, not an audited
			// transfer/fee-aware ledger. Zero for a held token is unknown.
			if basis, err := number(balance.EntryNtl); err == nil && basis > 0 {
				h.CostBasis = &basis
				h.UnrealizedPnL = finiteHolderValue(value - basis)
			}
			equity += value
		} else {
			complete = false
		}
		a.Holdings = append(a.Holdings, h)
	}
	a.USDC = &usdc
	a.Available = &available
	if complete {
		a.Equity = &equity
	} else {
		a.Error = "Unpriced held tokens; total unavailable"
	}
	now := time.Now().Unix()
	a.ObservedAt = &now
	setHolderAccountPnL(&a)
	return a
}
func fetchLighterHolder(ctx context.Context, client *http.Client, index string) HolderAccount {
	a := HolderAccount{}
	if index == "" {
		a.Error = "Lighter account not configured"
		return a
	}
	var response struct {
		Code     int `json:"code"`
		Accounts []struct {
			Index      uint64 `json:"account_index"`
			Collateral string `json:"collateral"`
			Available  string `json:"available_balance"`
			Equity     string `json:"total_asset_value"`
			Positions  []struct {
				Symbol      string `json:"symbol"`
				Position    string `json:"position"`
				Sign        int    `json:"sign"`
				Value       string `json:"position_value"`
				PnL         string `json:"unrealized_pnl"`
				Liquidation string `json:"liquidation_price"`
			} `json:"positions"`
		} `json:"accounts"`
	}
	err := holderJSON(ctx, client, "https://mainnet.zklighter.elliot.ai/api/v1/account?by=index&value="+index, nil, &response)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	want, _ := strconv.ParseUint(index, 10, 64)
	if response.Code != 200 || len(response.Accounts) != 1 || response.Accounts[0].Index != want {
		a.Error = "Lighter account response mismatch"
		return a
	}
	account := response.Accounts[0]
	collateral, e1 := number(account.Collateral)
	available, e2 := number(account.Available)
	equity, e3 := number(account.Equity)
	if e1 != nil || e2 != nil || e3 != nil {
		a.Error = "Missing Lighter balances"
		return a
	}
	for _, p := range account.Positions {
		size, err := number(p.Position)
		if err != nil {
			a.Error = "Invalid perp position"
			return a
		}
		if size == 0 {
			continue
		}
		if p.Sign != 1 && p.Sign != -1 {
			a.Error = "Invalid perp direction"
			return a
		}
		h := HolderAsset{Symbol: p.Symbol, Size: math.Abs(size) * float64(p.Sign)}
		if value, err := number(p.Value); err == nil {
			value = math.Abs(value)
			h.Value = &value
			price := value / math.Abs(size)
			h.Price = &price
		}
		if pnl, err := number(p.PnL); err == nil {
			h.UnrealizedPnL = &pnl
		}
		if liquidation, err := number(p.Liquidation); err == nil && liquidation > 0 {
			h.LiquidationPrice = &liquidation
		}
		a.Holdings = append(a.Holdings, h)
	}
	a.USDC = &collateral
	a.Available = &available
	a.Equity = &equity
	now := time.Now().Unix()
	a.ObservedAt = &now
	setHolderAccountPnL(&a)
	return a
}
