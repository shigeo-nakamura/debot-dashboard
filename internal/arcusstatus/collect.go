package arcusstatus

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Decode only explicit, public monitoring fields; signer configuration, wallet
// identifiers, transaction payloads and arbitrary error details are never emitted.
type decimal string

func (d *decimal) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*d = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = decimal(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*d = decimal(n.String())
	return nil
}
func number(d decimal) *float64 {
	v, err := strconv.ParseFloat(string(d), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

type basket struct {
	A decimal `json:"token_a"`
	B decimal `json:"token_b"`
}
type checkpoint struct {
	SchemaVersion int `json:"schema_version"`
	Config        struct {
		Mode string `json:"mode"`
		Pair struct {
			A string `json:"sell_symbol"`
			B string `json:"buy_symbol"`
		} `json:"pair"`
		DailyLimit      decimal `json:"daily_loss_limit_usd"`
		CumulativeLimit decimal `json:"cumulative_loss_limit_usd"`
	} `json:"config"`
	State struct {
		Sequence        uint64  `json:"sequence"`
		Inventory       basket  `json:"inventory"`
		Regime          string  `json:"regime"`
		PriceA          decimal `json:"last_token_a_reference_price_usd"`
		PriceB          decimal `json:"last_token_b_reference_price_usd"`
		Observation     string  `json:"last_observation_at"`
		Rotation        string  `json:"last_rotation_at"`
		RotatedQuantity decimal `json:"rotated_quantity"`
		InitialEquity   decimal `json:"initial_equity_usd"`
		InitialBasket   basket  `json:"initial_baseline_inventory"`
		DailyBasket     basket  `json:"daily_baseline_inventory"`
		DailyDay        string  `json:"daily_baseline_day"`
		Halt            *struct {
			Kind  string  `json:"kind"`
			At    string  `json:"engaged_at"`
			Loss  decimal `json:"loss_usd"`
			Limit decimal `json:"limit_usd"`
		} `json:"risk_halt"`
	} `json:"state"`
}
type eventEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Hash          string `json:"event_sha256"`
	EventJSON     string `json:"event_json"`
}
type event struct {
	Sequence uint64   `json:"sequence"`
	At       string   `json:"observed_at"`
	Pair     string   `json:"pair"`
	Mode     string   `json:"mode"`
	ZScore   *float64 `json:"z_score"`
	Decision struct {
		Action string `json:"action"`
		Hold   struct {
			Code string `json:"code"`
		} `json:"hold"`
		Plan struct {
			QuoteAt string `json:"quote_received_at"`
		} `json:"plan"`
	} `json:"decision"`
}
type balance struct {
	At  string  `json:"observed_at"`
	Gas decimal `json:"gas_balance_wei"`
}
type attempt struct {
	Phase     string   `json:"phase"`
	UpdatedAt string   `json:"updated_at"`
	Post      *balance `json:"post_balances"`
}
type ledger struct {
	SchemaVersion int       `json:"schema_version"`
	Active        *attempt  `json:"active"`
	History       []attempt `json:"history"`
}

// Unit properties come from systemctl show, not journal parsing. A normal
// successful oneshot is inactive/dead; inactivity alone is not a failure.
type Units struct {
	Service, Timer map[string]string
	Error          bool
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
	if len(b) > 16*1024*1024 {
		return nil, fmt.Errorf("monitoring input exceeds limit")
	}
	return b, err
}
func validTime(s string) bool { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }
func systemTime(s string) string {
	for _, layout := range []string{time.RFC3339Nano, "Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05.999999 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}
func (s *Status) problem(reason string) {
	s.HealthReasons = append(s.HealthReasons, reason)
	s.Healthy = false
}

func Collect(dir, configPath string, units Units, now time.Time) Payload {
	s := &Status{ExportedAt: now.UTC().Format(time.RFC3339Nano), Healthy: true, HealthReasons: []string{}, TickOutcome: "unknown", BudgetDay: now.UTC().Format("2006-01-02")}
	s.readUnits(units, now)
	before, err := readLimited(filepath.Join(dir, "runtime_state.json"))
	var cp checkpoint
	if err != nil || json.Unmarshal(before, &cp) != nil || cp.SchemaVersion != 1 || cp.Config.Pair.A == "" || cp.Config.Pair.B == "" || !validTime(cp.State.Observation) {
		s.problem("Runtime checkpoint unavailable or unsupported")
	} else {
		s.readCheckpoint(cp)
		s.readDecision(dir, cp)
	}
	s.readLedger(dir, configPath, now)
	after, err := readLimited(filepath.Join(dir, "runtime_state.json"))
	if err != nil || !bytes.Equal(before, after) {
		// Drop the mixed projection rather than attaching a decision from a
		// different tick to the latest inventory. The next minute retries.
		s = &Status{ExportedAt: s.ExportedAt, Healthy: false, TickOutcome: s.TickOutcome, LastTickAt: s.LastTickAt, HealthReasons: []string{"Runtime changed during collection; retrying next poll"}}
	}
	p := Payload{SchemaVersion: SchemaVersion, UpdatedAt: s.LastTickAt, ID: "arcus-spot", Dex: "arcus", DryRun: s.Mode == "read_only" || s.Mode == "replay_simulation", Arcus: s}
	if at, err := time.Parse(time.RFC3339Nano, s.LastTickAt); err == nil {
		p.TS = at.Unix()
	}
	return p
}

func (s *Status) readUnits(u Units, now time.Time) {
	if u.Error || u.Service["LoadState"] != "loaded" || u.Timer["LoadState"] != "loaded" {
		s.problem("Systemd monitoring unavailable")
		return
	}
	active := u.Timer["ActiveState"] == "active"
	enabled := u.Timer["UnitFileState"] == "enabled"
	s.TimerActive = &active
	s.TimerEnabled = &enabled
	if !active || !enabled {
		s.problem("Live-tick timer is inactive or disabled")
	}
	s.ServiceResult = u.Service["Result"]
	s.LastTickAt = systemTime(u.Service["ExecMainStartTimestamp"])
	if code, err := strconv.Atoi(u.Service["ExecMainStatus"]); err == nil {
		s.ExitCode = &code
	}
	state := u.Service["ActiveState"]
	if state == "activating" || state == "active" {
		s.TickOutcome = "running"
		if at, err := time.Parse(time.RFC3339Nano, s.LastTickAt); err != nil || now.Sub(at) > 120*time.Second {
			s.problem("Live tick exceeded its execution timeout")
		}
	} else if s.LastTickAt != "" && s.ServiceResult == "success" && s.ExitCode != nil && *s.ExitCode == 0 && u.Service["ExecMainCode"] == "1" {
		s.TickOutcome = "success"
	} else if s.LastTickAt != "" {
		s.TickOutcome = "failed"
		s.problem("Last live tick failed")
	} else {
		s.problem("No observed live-tick execution")
	}
}

func mul(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a * *b
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	return &v
}
func sum(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	return &v
}
func loss(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	v := math.Max(0, *a-*b)
	return &v
}
func value(b basket, pa, pb *float64) *float64 {
	return sum(mul(number(b.A), pa), mul(number(b.B), pb))
}

func (s *Status) readCheckpoint(c checkpoint) {
	x := c.State
	s.Pair = c.Config.Pair.A + "/" + c.Config.Pair.B
	if c.Config.Mode != "live" && c.Config.Mode != "read_only" && c.Config.Mode != "replay_simulation" {
		s.problem("Runtime mode unavailable or unsupported")
	}
	s.Mode = c.Config.Mode
	s.Sequence = x.Sequence
	s.LastObservationAt = x.Observation
	s.Regime = x.Regime
	s.LastRotationAt = x.Rotation
	s.RotatedQuantity = number(x.RotatedQuantity)
	pa, pb := number(x.PriceA), number(x.PriceB)
	s.Inventory = []Inventory{{c.Config.Pair.A, number(x.Inventory.A), pa, mul(number(x.Inventory.A), pa)}, {c.Config.Pair.B, number(x.Inventory.B), pb, mul(number(x.Inventory.B), pb)}}
	s.EquityUSD = value(x.Inventory, pa, pb)
	s.DailyBaselineDay = x.DailyDay
	s.DailyLossUSD = loss(value(x.DailyBasket, pa, pb), s.EquityUSD)
	cumulativeBenchmark := value(x.InitialBasket, pa, pb)
	s.CumulativeLossUSD = loss(cumulativeBenchmark, s.EquityUSD)
	s.InventoryDrawdownUSD = loss(number(x.InitialEquity), cumulativeBenchmark)
	s.DailyLossLimitUSD = number(c.Config.DailyLimit)
	s.CumulativeLossLimitUSD = number(c.Config.CumulativeLimit)
	if s.EquityUSD == nil || s.DailyLossUSD == nil || s.CumulativeLossUSD == nil || s.InventoryDrawdownUSD == nil || s.DailyLossLimitUSD == nil || s.CumulativeLossLimitUSD == nil {
		s.problem("Inventory, risk valuation or limits unavailable")
	}
	if x.Halt != nil {
		s.RiskHalt = &Halt{x.Halt.Kind, x.Halt.At, number(x.Halt.Loss), number(x.Halt.Limit)}
		s.problem("Strategy risk halt engaged")
	}
}

func decodeEvent(b []byte, cp checkpoint) (event, bool) {
	var env eventEnvelope
	var e event
	if json.Unmarshal(b, &env) != nil || env.SchemaVersion != 1 || env.Hash != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(env.EventJSON))) || json.Unmarshal([]byte(env.EventJSON), &e) != nil {
		return e, false
	}
	// The hash authenticates the envelope's payload consistency, not the whole
	// event chain. Sequence/pair/mode bind the decision to this checkpoint.
	return e, e.Sequence == cp.State.Sequence && e.Pair == cp.Config.Pair.A+"/"+cp.Config.Pair.B && e.Mode == cp.Config.Mode && validTime(e.At) && e.Decision.Action != ""
}
func (s *Status) readDecision(dir string, cp checkpoint) {
	paths, err := filepath.Glob(filepath.Join(dir, "live-tick-events", "????-??-??.jsonl"))
	if err == nil && len(paths) > 0 {
		sort.Strings(paths)
		if b, err := readLimited(paths[len(paths)-1]); err == nil && len(b) > 0 && b[len(b)-1] == '\n' {
			lines := bytes.Split(bytes.TrimSpace(b), []byte{'\n'})
			if e, ok := decodeEvent(lines[len(lines)-1], cp); ok {
				s.setDecision(e)
				return
			}
		}
	}
	if b, err := readLimited(filepath.Join(dir, "live-tick-event-pending.json")); err == nil {
		if e, ok := decodeEvent(b, cp); ok {
			s.setDecision(e)
			s.DecisionPending = true
			s.problem("Decision pending durable event commit")
			return
		}
	}
	s.problem("No durable decision matching the runtime checkpoint")
}
func (s *Status) setDecision(e event) {
	s.Decision = e.Decision.Action
	s.DecisionAt = e.At
	s.HoldCode = e.Decision.Hold.Code
	s.QuoteReceivedAt = e.Decision.Plan.QuoteAt
	s.ZScore = e.ZScore
}

func (s *Status) readLedger(dir, configPath string, now time.Time) {
	var cfg struct {
		Executor struct {
			Max *int `yaml:"max_swaps_per_utc_day"`
		} `yaml:"executor"`
	}
	if b, err := readLimited(configPath); err == nil && yaml.Unmarshal(b, &cfg) == nil && cfg.Executor.Max != nil && *cfg.Executor.Max > 0 {
		s.MaxSwapsPerDay = cfg.Executor.Max
	} else {
		s.problem("Daily execution limit unavailable")
	}
	b, err := readLimited(filepath.Join(dir, "ledger.json"))
	var l ledger
	if err != nil || json.Unmarshal(b, &l) != nil || l.SchemaVersion != 2 || l.History == nil {
		s.problem("Execution ledger unavailable or unsupported")
		return
	}
	used := 0
	for _, a := range l.History {
		at, err := time.Parse(time.RFC3339Nano, a.UpdatedAt)
		if err != nil {
			s.problem("Execution ledger timestamp invalid")
			return
		}
		// Match executor admission exactly: all archived attempts updated today,
		// including rejected attempts, consume the configured budget.
		if at.UTC().Format("2006-01-02") == s.BudgetDay {
			used++
		}
		s.readAttempt(a)
	}
	s.DailyBudgetUsed = &used
	s.ActiveExecutionPhase = "none"
	if l.Active != nil {
		s.ActiveExecutionPhase = l.Active.Phase
		s.readAttempt(*l.Active)
		if l.Active.Phase != "reconciled" {
			s.problem("Execution requires reconciliation: " + l.Active.Phase)
		}
	}
}
func (s *Status) readAttempt(a attempt) {
	if a.Phase != "reconciled" {
		return
	}
	if at, err := time.Parse(time.RFC3339Nano, a.UpdatedAt); err == nil {
		prev, _ := time.Parse(time.RFC3339Nano, s.LastSwapAt)
		if at.After(prev) {
			s.LastSwapAt = a.UpdatedAt
		}
	}
	if a.Post != nil && validTime(a.Post.At) {
		at, _ := time.Parse(time.RFC3339Nano, a.Post.At)
		prev, _ := time.Parse(time.RFC3339Nano, s.GasObservedAt)
		if at.After(prev) {
			scale := 1e-18
			s.GasBalanceETH = mul(number(a.Post.Gas), &scale)
			s.GasObservedAt = a.Post.At
		}
	}
}

func ParseProperties(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}
