package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// llpMonitorService is the read-only Lighter LLP monitor (bot-strategy#1179):
// an hourly job on Tokyo that checks the LLP deposit, its LIT perp hedge and
// the rules R1-R6 frozen in its spec.json, and mirrors one status.json to S3.
// It is not a bot and does not speak the trading status contract — no `ts`,
// no pnl_total, no positions — so a target with this service name is decoded
// by decodeLLPMonitorPayload instead of decodeStatusPayload.
const llpMonitorService = "llp-monitor"

// LLPMonitorFired is one firing rule, as the monitor evaluated it.
type LLPMonitorFired struct {
	Rule  string `json:"rule"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// LLPMonitorPool is the LLP pool itself. SharePrice is TAV/shares, which the
// venue's own NAV (the daily share price the app shows) runs about 3 % above;
// the card never labels it NAV. ApyVenue is a percent, the returns are
// fractions. Every figure is a pointer: the monitor leaves a return out when
// the daily series is too short to compute it, and the whole block is null
// when the pool read failed.
type LLPMonitorPool struct {
	TVL         *float64 `json:"tvl"`
	SharePrice  *float64 `json:"share_price"`
	ApyVenue    *float64 `json:"apy_venue"`
	AsOf        *string  `json:"asof"`
	R1d         *float64 `json:"r1d"`
	R30         *float64 `json:"r30"`
	R30Ann      *float64 `json:"r30_ann"`
	Drawdown    *float64 `json:"drawdown"`
	Status      *int64   `json:"status"`
	OperatorFee *float64 `json:"operator_fee"`
}

// LLPMonitorTVLChange is the pool's TVL change over the monitor's own
// history; Full7d is false until that history spans a week.
type LLPMonitorTVLChange struct {
	Value  *float64 `json:"value"`
	Full7d bool     `json:"full_7d"`
}

// LLPMonitorLIT is the hedge instrument: the LIT perp's last price and the
// 7-day funding a short received, annualised as a fraction (+ = the short
// is paid).
type LLPMonitorLIT struct {
	Price             *float64 `json:"price"`
	FundingShort7dAnn *float64 `json:"funding_short_7d_ann"`
	FundingHours      *int64   `json:"funding_hours"`
}

// LLPMonitorPosition is the owner's own account: the LLP shares (valued at
// the venue NAV, and at TAV/shares for reference), the LIT short and its
// margin. LLPPnlUsd is the NAV value against the deposit; HedgePnlUsd is the
// staked LIT marked against the hedge entry plus the perp's unrealised PnL.
// Both are the monitor's estimates. Null as a whole when the owner account
// read failed or no position is configured. StakedLIT and
// HedgeEntryLITPrice are the spec's own position figures; the monitor does
// not publish them yet, and the card falls back to what it can derive.
type LLPMonitorPosition struct {
	StakedLIT          *float64 `json:"staked_lit"`
	HedgeEntryLITPrice *float64 `json:"hedge_entry_lit_price"`
	LLPUsdcDeposited   *float64 `json:"llp_usdc_deposited"`
	ShortLIT           *float64 `json:"short_lit"`
	LiqPrice           *float64 `json:"liq_price"`
	LLPShares          *float64 `json:"llp_shares"`
	LLPValueUsd        *float64 `json:"llp_value_usd"`
	LLPValueTAVUsd     *float64 `json:"llp_value_tav_usd"`
	PerpUpnl           *float64 `json:"perp_upnl"`
	Collateral         *float64 `json:"collateral"`
	LLPPnlUsd          *float64 `json:"llp_pnl_usd"`
	HedgePnlUsd        *float64 `json:"hedge_pnl_usd"`
	// Carry since entry (bot-strategy PR #1182): the funding the short
	// received (+ = received), the LIT staked now and the staking reward
	// accrued on it, and the monitor's total — LLP + hedge + funding +
	// staking, entry fees and slippage excluded. Absent from an older
	// monitor; the card then falls back to LLP + hedge.
	FundingReceived  *float64 `json:"funding_received"`
	StakedLITNow     *float64 `json:"staked_lit_now"`
	StakingShares    *float64 `json:"staking_shares"`
	StakingRewardLIT *float64 `json:"staking_reward_lit"`
	StakingRewardUsd *float64 `json:"staking_reward_usd"`
	TotalPnlUsd      *float64 `json:"total_pnl_usd"`
}

// LLPMonitorWeekly is the first reading of each ISO week, kept for the
// four-week readout (the monitor sends the last eight).
type LLPMonitorWeekly struct {
	Week              string              `json:"week"`
	T                 string              `json:"t"`
	SharePrice        *float64            `json:"share_price"`
	TVL               *float64            `json:"tvl"`
	FundingShort7dAnn *float64            `json:"funding_short_7d_ann"`
	LIT               *float64            `json:"lit"`
	Position          *LLPMonitorPosition `json:"position"`
}

// LLPMonitorStatus is the monitor's status.json, passed through to the card.
// Level is OK / WARN / ALERT (the worst firing rule). Errors are the feeds
// that failed this run; FailRuns counts consecutive failed runs per feed.
// Notify is the SNS outcome of a firing-set change, null otherwise.
// LiveProxy is the monitor's intraday TAV/shares check; opaque here.
type LLPMonitorStatus struct {
	T         string               `json:"t"`
	Level     string               `json:"level"`
	Fired     []LLPMonitorFired    `json:"fired"`
	Errors    []string             `json:"errors"`
	Notify    *string              `json:"notify"`
	LLP       *LLPMonitorPool      `json:"llp"`
	TVLChange *LLPMonitorTVLChange `json:"tvl_change"`
	LiveProxy json.RawMessage      `json:"live_proxy,omitempty"`
	LIT       *LLPMonitorLIT       `json:"lit"`
	Position  *LLPMonitorPosition  `json:"position"`
	FailRuns  map[string]int64     `json:"fail_runs"`
	Weekly    []LLPMonitorWeekly   `json:"weekly"`
	// PnlHistory is the monitor's hourly P&L series since entry,
	// {fields: [t, total, llp, hedge, funding, staking], rows: [[...]]}.
	// Passed through as-is: the card draws it and reads the column
	// order from `fields`.
	PnlHistory json.RawMessage `json:"pnl_history,omitempty"`
}

// decodeLLPMonitorPayload maps the monitor's status.json onto the generic
// StatusData the rest of the pipeline works with: `t` becomes the freshness
// timestamp (and updated_at), the document itself rides along under
// llp_monitor, and every trading field stays at its zero value. A missing or
// unparseable `t` is an error rather than a zero ts, which would read as
// "unknown" instead of saying the document is malformed.
func decodeLLPMonitorPayload(payload []byte) (StatusData, error) {
	var m LLPMonitorStatus
	if err := json.Unmarshal(payload, &m); err != nil {
		return StatusData{}, err
	}
	if strings.TrimSpace(m.T) == "" {
		return StatusData{}, errors.New("llp-monitor status has no t")
	}
	at, err := time.Parse(time.RFC3339, m.T)
	if err != nil {
		return StatusData{}, fmt.Errorf("llp-monitor status t: %w", err)
	}
	m.Level = strings.ToUpper(strings.TrimSpace(m.Level))
	// Empty lists, not null: "nothing firing" and "no errors" are the
	// healthy readings and the card renders them as such.
	if m.Fired == nil {
		m.Fired = []LLPMonitorFired{}
	}
	if m.Errors == nil {
		m.Errors = []string{}
	}
	if string(m.LiveProxy) == "null" {
		m.LiveProxy = nil
	}
	if string(m.PnlHistory) == "null" {
		m.PnlHistory = nil
	}
	return StatusData{
		TS:         at.Unix(),
		UpdatedAt:  at.UTC().Format(time.RFC3339),
		LLPMonitor: &m,
	}, nil
}

// decodeTargetPayload picks the decoder for a target's S3 object.
func decodeTargetPayload(target TargetConfig, payload []byte) (StatusData, error) {
	if target.Service == llpMonitorService {
		return decodeLLPMonitorPayload(payload)
	}
	return decodeStatusPayload(payload)
}
