// Package arcusstatus projects Arcus runtime files into a read-only dashboard
// contract. It never acquires the executor lock or writes any runtime file.
package arcusstatus

import (
	"time"
)

const SchemaVersion = 1
const HeartbeatSecs = 180
const DefaultStaleSecs = 1920 // Two missed 15-minute ticks plus scheduling jitter.

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
	ExportedAt             string      `json:"exported_at"`
	Healthy                bool        `json:"healthy"`
	HealthReasons          []string    `json:"health_reasons"`
	Pair                   string      `json:"pair"`
	Mode                   string      `json:"mode"`
	Sequence               uint64      `json:"sequence"`
	LastTickAt             string      `json:"last_tick_at"`
	TickOutcome            string      `json:"tick_outcome"`
	ServiceResult          string      `json:"service_result"`
	ExitCode               *int        `json:"exit_code"`
	TimerActive            *bool       `json:"timer_active"`
	TimerEnabled           *bool       `json:"timer_enabled"`
	LastObservationAt      string      `json:"last_observation_at"`
	Decision               string      `json:"decision"`
	DecisionAt             string      `json:"decision_at"`
	DecisionPending        bool        `json:"decision_pending"`
	HoldCode               string      `json:"hold_code"`
	ZScore                 *float64    `json:"z_score"`
	QuoteReceivedAt        string      `json:"quote_received_at"`
	Regime                 string      `json:"regime"`
	LastRotationAt         string      `json:"last_rotation_at"`
	RotatedQuantity        *float64    `json:"rotated_quantity"`
	Inventory              []Inventory `json:"inventory"`
	EquityUSD              *float64    `json:"equity_usd"`
	DailyBaselineDay       string      `json:"daily_baseline_day"`
	DailyLossUSD           *float64    `json:"daily_loss_usd"`
	CumulativeLossUSD      *float64    `json:"cumulative_loss_usd"`
	InventoryDrawdownUSD   *float64    `json:"inventory_drawdown_usd"`
	DailyLossLimitUSD      *float64    `json:"daily_loss_limit_usd"`
	CumulativeLossLimitUSD *float64    `json:"cumulative_loss_limit_usd"`
	RiskHalt               *Halt       `json:"risk_halt"`
	BudgetDay              string      `json:"budget_day"`
	DailyBudgetUsed        *int        `json:"daily_budget_used"`
	MaxSwapsPerDay         *int        `json:"max_swaps_per_day"`
	ActiveExecutionPhase   string      `json:"active_execution_phase"`
	LastSwapAt             string      `json:"last_swap_at"`
	GasBalanceETH          *float64    `json:"gas_balance_eth"`
	GasObservedAt          string      `json:"gas_observed_at"`
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

// ServiceStatus ages both the producer heartbeat and the independent bot clocks.
// A fresh exporter can never hide a stalled bot, or vice versa.
func (s *Status) ServiceStatus(now time.Time, staleSecs int) string {
	if staleSecs <= 0 {
		staleSecs = DefaultStaleSecs
	}
	for _, clock := range []struct {
		at    string
		limit int
	}{
		{s.ExportedAt, HeartbeatSecs}, {s.LastTickAt, staleSecs}, {s.LastObservationAt, staleSecs},
	} {
		at, err := time.Parse(time.RFC3339Nano, clock.at)
		if err != nil || at.After(now.Add(30*time.Second)) {
			return "unknown"
		}
		if now.Sub(at) > time.Duration(clock.limit)*time.Second {
			return "stale"
		}
	}
	if !s.Healthy || s.RiskHalt != nil {
		return "degraded"
	}
	return "active"
}
