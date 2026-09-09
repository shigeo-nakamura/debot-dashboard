// Package arcusstatus projects Arcus runtime files into a read-only dashboard
// contract. It never acquires the executor lock or writes any runtime file.
package arcusstatus

import (
	"time"
)

const SchemaVersion = 1
const HeartbeatSecs = 180
const DefaultStaleSecs = 1920 // Two missed 15-minute ticks plus scheduling jitter.

// InFlightSecs is how long an execution attempt may sit in a phase that is
// still being carried forward before that stops being normal.
//
// A swap is dispatched by one tick and reconciled by the *next* one, so
// every trade leaves an attempt mid-flight for up to a full tick interval
// by design; calling that degraded made the dashboard cry wolf on each
// trade while the real three-hour halt of bot-strategy#979 reported as
// stale, never degraded. Two tick intervals plus jitter -- the same budget
// DefaultStaleSecs gives the bot's own clocks -- is past any healthy
// hand-off and into "this is not moving".
const InFlightSecs = DefaultStaleSecs

type Inventory struct {
	Symbol            string   `json:"symbol"`
	Amount            *float64 `json:"amount"`
	ReferencePriceUSD *float64 `json:"reference_price_usd"`
	ValueUSD          *float64 `json:"value_usd"`
}

type Halt struct {
	Kind      string   `json:"kind"`
	EngagedAt string   `json:"engaged_at"`
	LossUSD   *float64 `json:"loss_usd"`
	LimitUSD  *float64 `json:"limit_usd"`
}

type Status struct {
	ExportedAt        string      `json:"exported_at"`
	Healthy           bool        `json:"healthy"`
	HealthReasons     []string    `json:"health_reasons"`
	Pair              string      `json:"pair"`
	Mode              string      `json:"mode"`
	Sequence          uint64      `json:"sequence"`
	LastTickAt        string      `json:"last_tick_at"`
	TickOutcome       string      `json:"tick_outcome"`
	ServiceResult     string      `json:"service_result"`
	ExitCode          *int        `json:"exit_code"`
	TimerActive       *bool       `json:"timer_active"`
	TimerEnabled      *bool       `json:"timer_enabled"`
	LastObservationAt string      `json:"last_observation_at"`
	Decision          string      `json:"decision"`
	DecisionAt        string      `json:"decision_at"`
	DecisionPending   bool        `json:"decision_pending"`
	HoldCode          string      `json:"hold_code"`
	ZScore            *float64    `json:"z_score"`
	QuoteReceivedAt   string      `json:"quote_received_at"`
	Regime            string      `json:"regime"`
	LastRotationAt    string      `json:"last_rotation_at"`
	RotatedQuantity   *float64    `json:"rotated_quantity"`
	Inventory         []Inventory `json:"inventory"`
	EquityUSD         *float64    `json:"equity_usd"`
	DailyBaselineDay  string      `json:"daily_baseline_day"`
	DailyLossUSD      *float64    `json:"daily_loss_usd"`
	CumulativeLossUSD *float64    `json:"cumulative_loss_usd"`
	// CumulativeCostUSD is the same comparison without the floor at zero:
	// the price-neutral initial basket's current value minus current
	// equity, so a run that came out ahead reports a negative cost. The
	// floored CumulativeLossUSD is what the risk limits are evaluated
	// against and is kept unchanged; this one is what a cost-per-unit KPI
	// divides (bot-strategy#957).
	CumulativeCostUSD      *float64 `json:"cumulative_cost_usd"`
	InventoryDrawdownUSD   *float64 `json:"inventory_drawdown_usd"`
	DailyLossLimitUSD      *float64 `json:"daily_loss_limit_usd"`
	CumulativeLossLimitUSD *float64 `json:"cumulative_loss_limit_usd"`
	RiskHalt               *Halt    `json:"risk_halt"`
	BudgetDay              string   `json:"budget_day"`
	DailyBudgetUsed        *int     `json:"daily_budget_used"`
	MaxSwapsPerDay         *int     `json:"max_swaps_per_day"`
	ActiveExecutionPhase   string   `json:"active_execution_phase"`
	// ActiveExecutionAt is when the active attempt last changed phase, so a
	// reader can tell a normal in-flight swap from a stuck one without
	// re-deriving it from the health reasons (bot-strategy#981). Empty when
	// no attempt is active.
	ActiveExecutionAt string   `json:"active_execution_at"`
	LastSwapAt        string   `json:"last_swap_at"`
	GasBalanceETH     *float64 `json:"gas_balance_eth"`
	GasObservedAt     string   `json:"gas_observed_at"`
}

type Payload struct {
	SchemaVersion int     `json:"schema_version"`
	TS            int64   `json:"ts"`
	UpdatedAt     string  `json:"updated_at"`
	ID            string  `json:"id"`
	Dex           string  `json:"dex"`
	DryRun        bool    `json:"dry_run"`
	Arcus         *Status `json:"arcus"`
}

// ServiceStatus ages both the producer heartbeat and the independent bot
// clocks. A fresh exporter can never hide a stalled bot, or vice versa.
//
// Order matters. An unreadable or future clock is `unknown`: nothing here can
// be trusted. A stale exporter heartbeat is `stale` for the same reason --
// this payload is old, so every judgement in it may be too. But a recorded
// fault outranks the bot's own stale clocks, because the two arrive together
// precisely when something has gone wrong: the halt of bot-strategy#979 froze
// the bot's observation clock *and* stranded an attempt in sticky UNKNOWN, and
// reporting that as merely `stale` is what let three hours of downtime hide
// from anyone watching for `degraded` (bot-strategy#981). Stale clocks with
// nothing else wrong still report `stale`; the fault, when there is one, is
// the more actionable of the two and its detail is in HealthReasons.
func (s *Status) ServiceStatus(now time.Time, staleSecs int) string {
	if staleSecs <= 0 {
		staleSecs = DefaultStaleSecs
	}
	clocks := []struct {
		at    string
		limit int
	}{
		{s.ExportedAt, HeartbeatSecs}, {s.LastTickAt, staleSecs}, {s.LastObservationAt, staleSecs},
	}
	stale := false
	for i, clock := range clocks {
		at, err := time.Parse(time.RFC3339Nano, clock.at)
		if err != nil || at.After(now.Add(30*time.Second)) {
			return "unknown"
		}
		if now.Sub(at) > time.Duration(clock.limit)*time.Second {
			if i == 0 {
				// The exporter's own heartbeat: this payload is stale as a
				// whole, so its health verdict is not current evidence.
				return "stale"
			}
			stale = true
		}
	}
	if !s.Healthy || s.RiskHalt != nil {
		return "degraded"
	}
	if stale {
		return "stale"
	}
	return "active"
}
