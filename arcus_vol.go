package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Arcus presence / volume runtime (arcus_vol_runtime, bot-strategy#1093).
//
// The runtime rests one post-only quote per side a few bp behind the touch
// of a single market and re-pegs only when the touch leaves a band, so the
// card's headline question is not "how much did it make" but "was it
// quoting, and what did that cost". It reads the runtime's same-host
// status.json; the operator's stop limits and the API key's expiry live in
// files the dashboard cannot (and should not) read, so they are carried as
// display values in the target config.
type ArcusVolConfig struct {
	StatusPath string `yaml:"status_path"`
	// Stop limits as configured for the runtime (DAILY_STOP_USD /
	// CUM_STOP_USD in its config.env). Display-only: the dashboard derives
	// "remaining before the stop" from them and the runtime's own PnL.
	DailyStopUSD *float64 `yaml:"daily_stop_usd"`
	CumStopUSD   *float64 `yaml:"cum_stop_usd"`
	// RFC3339 expiry of the venue API key the runtime signs with. The
	// runtime cannot renew it; an expired key would fail every order while
	// the process kept running.
	APIKeyValidUntil string `yaml:"api_key_valid_until"`
	// The runtime rewrites status.json about once a second, so a file
	// older than this is a stalled or dead process. Default 20 s.
	StaleAfterSecs int `yaml:"stale_after_secs"`
}

const (
	arcusVolDefaultStaleSecs = 20
	arcusVolPresenceWindow   = 24 * time.Hour
	// Bound on retained samples per target: a 24 h window at a 5 s poll.
	arcusVolPresenceMaxSamples = 24 * 3600 / 5
	// Days left on the API key below which the card warns.
	arcusVolKeyWarnDays = 30
)

func (c ArcusVolConfig) validate() error {
	if !filepath.IsAbs(c.StatusPath) {
		return errors.New("arcus_vol.status_path must be absolute")
	}
	for name, v := range map[string]*float64{"daily_stop_usd": c.DailyStopUSD, "cum_stop_usd": c.CumStopUSD} {
		if v != nil && (*v <= 0 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return fmt.Errorf("arcus_vol.%s must be a positive number", name)
		}
	}
	if c.APIKeyValidUntil != "" {
		if _, err := time.Parse(time.RFC3339, c.APIKeyValidUntil); err != nil {
			return errors.New("arcus_vol.api_key_valid_until must be RFC3339 (e.g. 2027-03-30T06:32:00Z)")
		}
	}
	if c.StaleAfterSecs < 0 {
		return errors.New("arcus_vol.stale_after_secs must be positive")
	}
	return nil
}

func (c ArcusVolConfig) staleAfter() int {
	if c.StaleAfterSecs > 0 {
		return c.StaleAfterSecs
	}
	return arcusVolDefaultStaleSecs
}

// ArcusVolStatus is the dashboard-safe view of the runtime: an explicit
// allowlist of what status.json carries plus what the dashboard derives.
// Order ids are not forwarded.
type ArcusVolStatus struct {
	Market string `json:"market"`
	Mode   string `json:"mode"`
	// State summarises plan + halt + freshness for the card header:
	// quoting | pulled | halted | stale | unavailable.
	State string `json:"state"`
	// Plan is the runtime's own tick plan ("quote", "pull:<reason>",
	// "flatten:<reason>", "wait"); PlanReason is the part after the colon.
	Plan       string  `json:"plan"`
	PlanReason string  `json:"plan_reason,omitempty"`
	Halt       *string `json:"halt"`
	Backoff    bool    `json:"backoff"`
	Cooldown   bool    `json:"cooldown"`

	// MeasurementsAvailable is false when the status could not be read or
	// decoded: the blocks below are then absent, never zero — a zero PnL is
	// a measurement, and the subsidy bucket would count it as a cost of 0.
	MeasurementsAvailable bool            `json:"measurements_available"`
	QuoteOffsetBps        float64         `json:"quote_offset_bps"`
	RepegBandBps          float64         `json:"repeg_band_bps"`
	EffectiveCapUSD       float64         `json:"effective_cap_usd"`
	Book                  *ArcusVolBook   `json:"book,omitempty"`
	Quotes                *ArcusVolQuotes `json:"quotes,omitempty"`
	Inventory             *ArcusVolInv    `json:"inventory,omitempty"`
	PnL                   *ArcusVolPnL    `json:"pnl,omitempty"`
	Volume                *ArcusVolVolume `json:"volume,omitempty"`
	CostPer1MUSD          *float64        `json:"cost_per_1m_usd,omitempty"`
	PendingUnres          int             `json:"pending_unresolved"`
	Presence              *ArcusVolPres   `json:"presence"`
	APIKey                *ArcusVolAPIKey `json:"api_key"`
}

type ArcusVolBook struct {
	Bid float64 `json:"bid"`
	Ask float64 `json:"ask"`
}

type ArcusVolQuotes struct {
	Bid *ArcusVolQuote `json:"bid"`
	Ask *ArcusVolQuote `json:"ask"`
}

type ArcusVolQuote struct {
	Px           float64  `json:"px"`
	Qty          float64  `json:"qty"`
	NotionalUSD  float64  `json:"notional_usd"`
	DistTouchBps *float64 `json:"dist_touch_bps"`
	Filled       float64  `json:"filled"`
}

type ArcusVolInv struct {
	Qty        float64 `json:"qty"`
	USD        float64 `json:"usd"`
	AvgPx      float64 `json:"avg_px"`
	OpenedAtMs *int64  `json:"opened_at_ms"`
}

type ArcusVolPnL struct {
	DailyNet    float64 `json:"daily_net"`
	CumNet      float64 `json:"cum_net"`
	CumRealized float64 `json:"cum_realized"`
	CumFees     float64 `json:"cum_fees"`
	// Configured stops and the room left before each one trips
	// (stop + net, floored at zero). Nil when the stop is not configured.
	DailyStopUSD   *float64 `json:"daily_stop_usd"`
	CumStopUSD     *float64 `json:"cum_stop_usd"`
	RemainingDaily *float64 `json:"remaining_daily_usd"`
	RemainingCum   *float64 `json:"remaining_cum_usd"`
}

type ArcusVolVolume struct {
	Day      float64 `json:"day"`
	Cum      float64 `json:"cum"`
	CumMaker float64 `json:"cum_maker"`
	CumTaker float64 `json:"cum_taker"`
	Fills    int64   `json:"fills"`
	// Share of cumulative volume done as maker; nil before the first fill.
	MakerShare *float64 `json:"maker_share"`
}

// ArcusVolPres is the dashboard's own sampling of the runtime: at each poll
// it records whether the runtime planned to quote with both sides resting
// and whether either order changed since the previous sample. Kept in
// memory only, so it starts over when the dashboard restarts.
type ArcusVolPres struct {
	WindowSecs      int64    `json:"window_secs"`
	CoveredSecs     int64    `json:"covered_secs"`
	Samples         int      `json:"samples"`
	QuotingFraction *float64 `json:"quoting_fraction"`
	Requotes        int      `json:"requotes"`
}

type ArcusVolAPIKey struct {
	ValidUntil string  `json:"valid_until"`
	DaysLeft   float64 `json:"days_left"`
	Warn       bool    `json:"warn"`
	Expired    bool    `json:"expired"`
}

// arcusVolRaw mirrors the producer's status.json. Decimals arrive as
// strings; anything unknown is ignored.
type arcusVolRaw struct {
	Bot             string  `json:"bot"`
	TSMs            int64   `json:"ts_ms"`
	Mode            string  `json:"mode"`
	Market          string  `json:"market"`
	Plan            string  `json:"plan"`
	Halt            *string `json:"halt"`
	Backoff         bool    `json:"backoff"`
	Cooldown        bool    `json:"cooldown"`
	QuoteOffsetBps  string  `json:"quote_offset_bps"`
	RepegBandBps    string  `json:"repeg_band_bps"`
	EffectiveCapUSD string  `json:"effective_cap_usd"`
	Book            *struct {
		Bid string `json:"bid"`
		Ask string `json:"ask"`
	} `json:"book"`
	Quotes struct {
		Bid *arcusVolRawQuote `json:"bid"`
		Ask *arcusVolRawQuote `json:"ask"`
	} `json:"quotes"`
	Inventory struct {
		Qty        string `json:"qty"`
		USD        string `json:"usd"`
		AvgPx      string `json:"avg_px"`
		OpenedAtMs *int64 `json:"opened_at_ms"`
	} `json:"inventory"`
	PnL struct {
		DailyNet    string `json:"daily_net"`
		CumNet      string `json:"cum_net"`
		CumRealized string `json:"cum_realized"`
		CumFees     string `json:"cum_fees"`
	} `json:"pnl"`
	Volume struct {
		Day      string `json:"day"`
		Cum      string `json:"cum"`
		CumMaker string `json:"cum_maker"`
		CumTaker string `json:"cum_taker"`
		Fills    int64  `json:"fills"`
	} `json:"volume"`
	CostPer1MUSD      *string           `json:"cost_per_1m_usd"`
	PendingUnresolved []json.RawMessage `json:"pending_unresolved"`
}

type arcusVolRawQuote struct {
	Px           string  `json:"px"`
	Qty          string  `json:"qty"`
	DistTouchBps *string `json:"dist_touch_bps"`
	DistBps      *string `json:"dist_bps"`
	Filled       string  `json:"filled"`
	OrderID      string  `json:"order_id"`
}

// arcusVolSample is one poll's observation for the presence tracker.
type arcusVolSample struct {
	at      time.Time
	quoting bool
	bidID   string
	askID   string
}

type arcusVolTracker struct {
	mu      sync.Mutex
	samples map[string][]arcusVolSample
}

var arcusVolPresence = &arcusVolTracker{samples: map[string][]arcusVolSample{}}

// record appends a sample for `key` and returns the window summary. A
// re-quote is counted when a side's order id changes between consecutive
// samples while both are non-empty (a pull and a fresh place show as
// empty → id and are not counted as re-pegs).
func (t *arcusVolTracker) record(key string, s arcusVolSample, window time.Duration, maxSamples int) ArcusVolPres {
	t.mu.Lock()
	defer t.mu.Unlock()
	all := append(t.samples[key], s)
	cutoff := s.at.Add(-window)
	first := sort.Search(len(all), func(i int) bool { return !all[i].at.Before(cutoff) })
	all = all[first:]
	if len(all) > maxSamples {
		all = all[len(all)-maxSamples:]
	}
	t.samples[key] = all
	return summariseArcusVol(all, window)
}

func summariseArcusVol(all []arcusVolSample, window time.Duration) ArcusVolPres {
	p := ArcusVolPres{WindowSecs: int64(window / time.Second), Samples: len(all)}
	if len(all) == 0 {
		return p
	}
	quoting := 0
	for i, s := range all {
		if s.quoting {
			quoting++
		}
		if i == 0 {
			continue
		}
		prev := all[i-1]
		if prev.bidID != "" && s.bidID != "" && prev.bidID != s.bidID {
			p.Requotes++
		}
		if prev.askID != "" && s.askID != "" && prev.askID != s.askID {
			p.Requotes++
		}
	}
	frac := float64(quoting) / float64(len(all))
	p.QuotingFraction = &frac
	p.CoveredSecs = int64(all[len(all)-1].at.Sub(all[0].at) / time.Second)
	return p
}

// arcusVolTrackerForTest swaps the process-wide tracker so tests do not
// share state; it returns a restore func.
func arcusVolTrackerForTest() func() {
	prev := arcusVolPresence
	arcusVolPresence = &arcusVolTracker{samples: map[string][]arcusVolSample{}}
	return func() { arcusVolPresence = prev }
}

func optNumber(s string) *float64 {
	v, err := number(s)
	if err != nil {
		return nil
	}
	return &v
}

func numberOr0(s string) float64 {
	v, err := number(s)
	if err != nil {
		return 0
	}
	return v
}

func arcusVolQuote(q *arcusVolRawQuote) (*ArcusVolQuote, string) {
	if q == nil {
		return nil, ""
	}
	px, e1 := number(q.Px)
	qty, e2 := number(q.Qty)
	if e1 != nil || e2 != nil {
		return nil, ""
	}
	out := &ArcusVolQuote{Px: px, Qty: qty, NotionalUSD: px * qty, Filled: numberOr0(q.Filled)}
	if q.DistTouchBps != nil {
		out.DistTouchBps = optNumber(*q.DistTouchBps)
	} else if q.DistBps != nil {
		out.DistTouchBps = optNumber(*q.DistBps)
	}
	return out, q.OrderID
}

// remainingBeforeStop is the room left before a loss limit trips: the limit
// plus the (signed) net result, floored at zero. A positive day leaves the
// whole limit.
func remainingBeforeStop(limit *float64, net float64) *float64 {
	if limit == nil {
		return nil
	}
	left := *limit + net
	if left > *limit {
		left = *limit
	}
	if left < 0 {
		left = 0
	}
	return &left
}

func arcusVolAPIKey(validUntil string, now time.Time) *ArcusVolAPIKey {
	if validUntil == "" {
		return nil
	}
	until, err := time.Parse(time.RFC3339, validUntil)
	if err != nil {
		return nil
	}
	days := until.Sub(now).Hours() / 24
	return &ArcusVolAPIKey{
		ValidUntil: until.UTC().Format(time.RFC3339),
		DaysLeft:   math.Round(days*10) / 10,
		Warn:       days <= arcusVolKeyWarnDays,
		Expired:    days <= 0,
	}
}

// arcusVolState names the card state from the runtime's plan, halt and
// freshness. Halt wins over plan, staleness over both.
func arcusVolState(plan string, halt *string, stale bool) string {
	switch {
	case stale:
		return "stale"
	case halt != nil && *halt != "":
		return "halted"
	case plan == "quote":
		return "quoting"
	default:
		return "pulled"
	}
}

func splitPlan(plan string) (kind, reason string) {
	kind, reason, _ = strings.Cut(plan, ":")
	return strings.TrimSpace(kind), strings.TrimSpace(reason)
}

// decodeArcusVol parses status.json into the allowlisted view. It returns
// an error for a payload that is not this runtime's.
func decodeArcusVol(payload []byte, cfg *ArcusVolConfig, now time.Time) (*ArcusVolStatus, int64, string, string, error) {
	var raw arcusVolRaw
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, 0, "", "", errors.New("invalid arcus-vol status")
	}
	if raw.Bot != "arcus_vol_runtime" || raw.TSMs <= 0 || raw.Market == "" || (raw.Mode != "live" && raw.Mode != "dry_run") {
		return nil, 0, "", "", errors.New("invalid arcus-vol status")
	}
	s := &ArcusVolStatus{
		MeasurementsAvailable: true,
		Market:                raw.Market,
		Mode:                  raw.Mode,
		Plan:                  raw.Plan,
		Halt:                  raw.Halt,
		Backoff:               raw.Backoff,
		Cooldown:              raw.Cooldown,
		QuoteOffsetBps:        numberOr0(raw.QuoteOffsetBps),
		RepegBandBps:          numberOr0(raw.RepegBandBps),
		EffectiveCapUSD:       numberOr0(raw.EffectiveCapUSD),
		PendingUnres:          len(raw.PendingUnresolved),
	}
	_, s.PlanReason = splitPlan(raw.Plan)
	if raw.Book != nil {
		bid, e1 := number(raw.Book.Bid)
		ask, e2 := number(raw.Book.Ask)
		if e1 == nil && e2 == nil {
			s.Book = &ArcusVolBook{Bid: bid, Ask: ask}
		}
	}
	var bidID, askID string
	s.Quotes = &ArcusVolQuotes{}
	s.Quotes.Bid, bidID = arcusVolQuote(raw.Quotes.Bid)
	s.Quotes.Ask, askID = arcusVolQuote(raw.Quotes.Ask)
	s.Inventory = &ArcusVolInv{
		Qty:        numberOr0(raw.Inventory.Qty),
		USD:        numberOr0(raw.Inventory.USD),
		AvgPx:      numberOr0(raw.Inventory.AvgPx),
		OpenedAtMs: raw.Inventory.OpenedAtMs,
	}
	s.PnL = &ArcusVolPnL{
		DailyNet:    numberOr0(raw.PnL.DailyNet),
		CumNet:      numberOr0(raw.PnL.CumNet),
		CumRealized: numberOr0(raw.PnL.CumRealized),
		CumFees:     numberOr0(raw.PnL.CumFees),
	}
	if cfg != nil {
		s.PnL.DailyStopUSD = cfg.DailyStopUSD
		s.PnL.CumStopUSD = cfg.CumStopUSD
		s.PnL.RemainingDaily = remainingBeforeStop(cfg.DailyStopUSD, s.PnL.DailyNet)
		s.PnL.RemainingCum = remainingBeforeStop(cfg.CumStopUSD, s.PnL.CumNet)
		s.APIKey = arcusVolAPIKey(cfg.APIKeyValidUntil, now)
	}
	s.Volume = &ArcusVolVolume{
		Day:      numberOr0(raw.Volume.Day),
		Cum:      numberOr0(raw.Volume.Cum),
		CumMaker: numberOr0(raw.Volume.CumMaker),
		CumTaker: numberOr0(raw.Volume.CumTaker),
		Fills:    raw.Volume.Fills,
	}
	if s.Volume.Cum > 0 {
		share := s.Volume.CumMaker / s.Volume.Cum
		s.Volume.MakerShare = &share
	}
	if raw.CostPer1MUSD != nil {
		s.CostPer1MUSD = optNumber(*raw.CostPer1MUSD)
	}
	return s, raw.TSMs, bidID, askID, nil
}

// fetchArcusVol builds the target's status from the same-host status file.
// Kill switch, halt and staleness feed the generic header fields so the
// fleet summary and alerting see them without knowing this shape.
func fetchArcusVol(target TargetConfig, now time.Time) TargetStatus {
	cfg := target.ArcusVol
	staleAfter := cfg.staleAfter()
	r := TargetStatus{
		StaleAfterSecs: staleAfter,
		Name:           target.Name,
		InstanceID:     target.InstanceID,
		Service:        target.Service,
		Region:         target.Region,
		CheckedAt:      now,
		ServiceStatus:  "unknown",
	}
	s := StatusData{Dex: "Arcus Perps"}
	r.Status = &s
	key := target.Service + "|" + cfg.StatusPath
	// Every attempted poll is a presence sample. A poll that finds no
	// readable, valid, fresh status is a non-quoting one: an outage between
	// two quoting samples must lower the fraction, not vanish from it, and
	// its empty order ids keep a re-peg from being counted across the gap.
	miss := func(state, errText string) TargetStatus {
		r.Error = errText
		a := &ArcusVolStatus{State: state}
		p := arcusVolPresence.record(key, arcusVolSample{at: now}, arcusVolPresenceWindow, arcusVolPresenceMaxSamples)
		a.Presence = &p
		s.ArcusVol = a
		return r
	}
	payload, err := os.ReadFile(cfg.StatusPath)
	if err != nil {
		return miss("unavailable", "Arcus-vol status unavailable")
	}
	a, tsMs, bidID, askID, err := decodeArcusVol(payload, cfg, now)
	if err != nil {
		return miss("unavailable", err.Error())
	}
	s.TS = tsMs / 1000
	s.UpdatedAt = time.UnixMilli(tsMs).UTC().Format(time.RFC3339)
	s.DryRun = a.Mode == "dry_run"
	age := now.Unix() - s.TS
	stale := age > int64(staleAfter) || age < -60
	r.ServiceStatus = "active"
	if stale {
		r.ServiceStatus = "stale"
	}
	a.State = arcusVolState(a.Plan, a.Halt, stale)
	kill := a.Halt != nil && *a.Halt == "kill_switch"
	s.KillSwitchActive = kill
	r.KillSwitchActive = &kill
	// Sample presence only from a fresh status: a frozen file is not the
	// runtime quoting, whatever its last plan said.
	quoting := !stale && a.Plan == "quote" && a.Quotes.Bid != nil && a.Quotes.Ask != nil
	if stale {
		// A frozen file carries the ids of orders the dead process left
		// behind; they are not resting quotes and must not pair up into a
		// re-peg with the ids seen after a restart.
		bidID, askID = "", ""
	}
	p := arcusVolPresence.record(key, arcusVolSample{at: now, quoting: quoting, bidID: bidID, askID: askID}, arcusVolPresenceWindow, arcusVolPresenceMaxSamples)
	a.Presence = &p
	s.ArcusVol = a
	return r
}
