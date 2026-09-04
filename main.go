package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gopkg.in/yaml.v3"
)

const (
	defaultPollIntervalSecs  = 20
	commandTimeout           = 15 * time.Second
	accumulatorSchemaVersion = 1
	// Status objects older than this are reported as "stale". Bot
	// status writers emit at 60s cadence, so 180s gives 3× headroom
	// before the dashboard flips a target to stale (bot-strategy#343).
	s3StatusStaleSecs = 180
)

type Config struct {
	Region           string         `yaml:"region"`
	PollIntervalSecs int            `yaml:"poll_interval_secs"`
	Targets          []TargetConfig `yaml:"targets"`
	Auth             *AuthConfig    `yaml:"auth"`
}

type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type TargetConfig struct {
	Name       string `yaml:"name"`
	InstanceID string `yaml:"instance_id"`
	Service    string `yaml:"service"`
	Region     string `yaml:"region"`
	// Bucket name and full key for the bot's `<id>.json` object.
	// Sibling files (equity_history.jsonl) are derived by suffix-
	// replacing the key. The bot mirrors `status.json` to this key
	// via its `STATUS_S3_BUCKET` / `STATUS_S3_KEY_PREFIX` env vars.
	S3Bucket string `yaml:"s3_bucket"`
	S3Key    string `yaml:"s3_key"`
	// AWS region of the bucket. Optional; falls back to `region` when
	// empty for back-compat. Split out so `region` can keep its display
	// meaning ("where the bot runs") even when the bucket lives
	// elsewhere — otherwise a Tokyo bot writing to a Frankfurt bucket
	// would have to advertise itself as Frankfurt and end up under the
	// wrong region group on the FE.
	S3Region string `yaml:"s3_region"`
}

type StatusPosition struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Size       string  `json:"size"`
	EntryPrice *string `json:"entry_price"`
}

type TradeStats struct {
	Trades  int64   `json:"trades"`
	Wins    int64   `json:"wins"`
	WinRate float64 `json:"win_rate"`
	MaxDD   float64 `json:"max_dd"`
	Pnl     float64 `json:"pnl"`
}

type ShutdownPosition struct {
	Key             string `json:"key"`
	EnteredTS       int64  `json:"entered_ts"`
	ForceCloseEtaTS int64  `json:"force_close_eta_ts"`
}

type ShutdownStatus struct {
	Pending         bool               `json:"pending"`
	GraceDeadlineTS int64              `json:"grace_deadline_ts"`
	ForceCloseEtaTS *int64             `json:"force_close_eta_ts"`
	Positions       []ShutdownPosition `json:"positions"`
}

// AccumulatorStatus is the dashboard-safe balance/activity view emitted by
// hype-accumulator. It intentionally excludes account identity and signing
// material. TotalEquityUSDC is derived bot-side from the reconciled balances.
type AccumulatorStatus struct {
	TotalEquityUSDC   float64 `json:"total_equity_usdc"`
	USDCBalance       float64 `json:"usdc_balance"`
	HYPEBalance       float64 `json:"hype_balance"`
	HYPEPriceUSDC     float64 `json:"hype_price_usdc"`
	BalanceObservedAt string  `json:"balance_observed_at"`
	LastTradeAt       *string `json:"last_trade_at,omitempty"`
	TradeCadence      string  `json:"trade_cadence"`
	Healthy           bool    `json:"healthy"`
	HealthReason      *string `json:"health_reason,omitempty"`
}

// HanBridgeStatus is the Engine B (bot-strategy#866/#872, codename "Han
// Bridge") dashboard block: which symbols this instance trades and why
// today's entry was or wasn't taken, so an operator does not have to grep
// journalctl or read status.json by hand to answer "why didn't it trade
// today". Unlike AccumulatorStatus, this bot has real positions/PnL too
// (it is a directional single-symbol strategy, not a passive holder), so
// the dashboard renders this as an *additional* section alongside the
// normal trading view rather than replacing it.
type HanBridgeStatus struct {
	KrPrimarySymbol   string   `json:"kr_primary_symbol"`
	UsPrimarySymbol   string   `json:"us_primary_symbol"`
	DayEntered        bool     `json:"day_entered"`
	DayExited         bool     `json:"day_exited"`
	IneligibleReasons []string `json:"ineligible_reasons"`
	SessionHaltReason *string  `json:"session_halt_reason,omitempty"`
}

type StatusData struct {
	SchemaVersion uint8  `json:"schema_version,omitempty"`
	TS            int64  `json:"ts"`
	UpdatedAt     string `json:"updated_at"`
	// ProcessStartedAt is the bot process boot time (epoch s),
	// self-reported by both pairtrade and xvenue-arb since
	// bot-strategy#343.
	ProcessStartedAt int64 `json:"process_started_at,omitempty"`
	// WsReset24hCount is the count of `Connection reset without
	// closing handshake` events over the last 24h, self-reported by
	// the bot via bot-strategy#343.
	WsReset24hCount uint64 `json:"ws_reset_24h_count,omitempty"`
	// KillSwitchActive reports whether `/opt/debot/KILL_SWITCH`
	// exists on the bot host, self-reported via bot-strategy#343.
	KillSwitchActive bool             `json:"kill_switch_active,omitempty"`
	ID               *string          `json:"id"`
	Agent            *string          `json:"agent"`
	Dex              string           `json:"dex"`
	DryRun           bool             `json:"dry_run"`
	BacktestMode     bool             `json:"backtest_mode"`
	IntervalSecs     int              `json:"interval_secs"`
	PositionsReady   bool             `json:"positions_ready"`
	PositionCount    int              `json:"position_count"`
	HasPosition      bool             `json:"has_position"`
	Positions        []StatusPosition `json:"positions"`
	PnlTotal         float64          `json:"pnl_total"`
	PnlToday         float64          `json:"pnl_today"`
	PnlSource        string           `json:"pnl_source"`
	// Sum of `funding_carry_usd` across cycles closed during the
	// current UTC session (bot-strategy#371). Pointer so a pre-#371
	// binary's missing field deserialises to nil and the dashboard
	// renders "-" instead of a misleading "$0.00". Post-#371
	// binaries always emit the field (zero is a meaningful "no
	// closed cycles today" measurement).
	FundingCarryToday *float64           `json:"funding_carry_today,omitempty"`
	Accumulator       *AccumulatorStatus `json:"accumulator,omitempty"`
	HanBridge         *HanBridgeStatus   `json:"han_bridge,omitempty"`
	TradeStats        *TradeStats        `json:"trade_stats,omitempty"`
	Maintenance       *string            `json:"maintenance,omitempty"`
	Shutdown          *ShutdownStatus    `json:"shutdown,omitempty"`
	ErrorSummary      *ErrorSummary      `json:"error_summary,omitempty"`
	EquityHistory     []EquityPoint      `json:"equity_history,omitempty"`
	// Risk gates emitted by pairtrade since bot-strategy#185.
	// All three may be nil when the threshold is disabled (the bot
	// skips emission to keep status.json compact). The dashboard
	// renders a halt pill / progress panel only when the field is
	// present.
	DailyRisk      *DailyRiskSnapshot      `json:"daily_risk,omitempty"`
	SessionRisk    *SessionRiskSnapshot    `json:"session_risk,omitempty"`
	CircuitBreaker *CircuitBreakerSnapshot `json:"circuit_breaker,omitempty"`
	// RiskHistory is a bounded ring buffer (cap 200) of recent risk-audit
	// events emitted by pairtrade since 9755490. Besides halt lifecycle
	// transitions, newer bots may include non-halt events such as capital
	// rebaselines; the frontend filters those out of its halt-history strip.
	RiskHistory []RiskHistoryEvent `json:"risk_history,omitempty"`
}

// RiskHistoryEvent mirrors pairtrade's RiskHistoryEvent (status.rs).
// Actual halt lifecycle transitions render as colored dots on a 30-d
// strip below each card's risk panel; other risk-audit event types stay
// available in the API but are not presented as halts. Detail is
// forwarded raw to the FE for best-effort tooltips.
type RiskHistoryEvent struct {
	TS         int64                  `json:"ts"`
	InstanceID string                 `json:"instance_id"`
	Kind       string                 `json:"kind"`
	EventType  string                 `json:"event_type"`
	Reason     *string                `json:"reason,omitempty"`
	Detail     map[string]interface{} `json:"detail,omitempty"`
}

// DailyRiskSnapshot mirrors pairtrade's DailyRiskSnapshot
// (bot-strategy#185 Phase 2-4 + leverage-neutralization amendment).
// EffectiveMaxDailyLossBps is the leverage-scaled threshold the bot
// actually compares DailyPnlBps against; the dashboard should use the
// effective value when colouring a "X% of threshold" progress bar.
type DailyRiskSnapshot struct {
	DailyPnl                 float64 `json:"daily_pnl"`
	DailyPnlBps              float64 `json:"daily_pnl_bps"`
	SessionStartEquity       float64 `json:"session_start_equity"`
	SessionStartTS           int64   `json:"session_start_ts"`
	MaxDailyLossBps          uint32  `json:"max_daily_loss_bps"`
	EffectiveMaxDailyLossBps float64 `json:"effective_max_daily_loss_bps"`
	RiskHalted               bool    `json:"risk_halted"`
}

// SessionRiskSnapshot mirrors pairtrade's SessionRiskSnapshot
// (bot-strategy#185 Phase 3-1 + leverage-neutralization amendment).
// HaltReason / HaltTS are populated only when SessionHalted is true.
type SessionRiskSnapshot struct {
	CurrentEquity              float64 `json:"current_equity"`
	PeakEquity                 float64 `json:"peak_equity"`
	DDBps                      float64 `json:"dd_bps"`
	MaxSessionLossBps          uint32  `json:"max_session_loss_bps"`
	EffectiveMaxSessionLossBps float64 `json:"effective_max_session_loss_bps"`
	LookbackSecs               uint64  `json:"lookback_secs"`
	SampleCount                int     `json:"sample_count"`
	SessionHalted              bool    `json:"session_halted"`
	HaltReason                 *string `json:"halt_reason,omitempty"`
	HaltTS                     *int64  `json:"halt_ts,omitempty"`
}

// CircuitBreakerSnapshot mirrors pairtrade's CircuitBreakerSnapshot
// (emitted from pairtrade@212cc6f). Always present once the engine has
// run a tick; Active is false in steady state.
type CircuitBreakerSnapshot struct {
	ConsecutiveLosses     uint32 `json:"consecutive_losses"`
	Active                bool   `json:"active"`
	UntilTS               *int64 `json:"until_ts,omitempty"`
	CooldownRemainingSecs *int64 `json:"cooldown_remaining_secs,omitempty"`
	Tier1Threshold        uint32 `json:"tier1_threshold"`
	Tier2Threshold        uint32 `json:"tier2_threshold"`
}

type ErrorSummary struct {
	// Window count. Populated from `error_count_30m` (new) and
	// `error_count_5m` (old) — see UnmarshalJSON. bot-strategy#168 widened
	// the window from 5m to 30m; consumers that are still on a bot emitting
	// the old name fall back transparently.
	ErrorCountWindow uint64  `json:"-"`
	WarnCountWindow  uint64  `json:"-"`
	ErrorCountTotal  uint64  `json:"error_count_total"`
	WarnCountTotal   uint64  `json:"warn_count_total"`
	LastErrorTs      *int64  `json:"last_error_ts,omitempty"`
	LastErrorMessage *string `json:"last_error_message,omitempty"`
	LastWarnTs       *int64  `json:"last_warn_ts,omitempty"`
	LastWarnMessage  *string `json:"last_warn_message,omitempty"`
}

func (e *ErrorSummary) UnmarshalJSON(data []byte) error {
	type raw struct {
		ErrorCount30m    *uint64 `json:"error_count_30m,omitempty"`
		WarnCount30m     *uint64 `json:"warn_count_30m,omitempty"`
		ErrorCount5m     *uint64 `json:"error_count_5m,omitempty"`
		WarnCount5m      *uint64 `json:"warn_count_5m,omitempty"`
		ErrorCountTotal  uint64  `json:"error_count_total"`
		WarnCountTotal   uint64  `json:"warn_count_total"`
		LastErrorTs      *int64  `json:"last_error_ts,omitempty"`
		LastErrorMessage *string `json:"last_error_message,omitempty"`
		LastWarnTs       *int64  `json:"last_warn_ts,omitempty"`
		LastWarnMessage  *string `json:"last_warn_message,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	switch {
	case r.ErrorCount30m != nil:
		e.ErrorCountWindow = *r.ErrorCount30m
	case r.ErrorCount5m != nil:
		e.ErrorCountWindow = *r.ErrorCount5m
	}
	switch {
	case r.WarnCount30m != nil:
		e.WarnCountWindow = *r.WarnCount30m
	case r.WarnCount5m != nil:
		e.WarnCountWindow = *r.WarnCount5m
	}
	e.ErrorCountTotal = r.ErrorCountTotal
	e.WarnCountTotal = r.WarnCountTotal
	e.LastErrorTs = r.LastErrorTs
	e.LastErrorMessage = r.LastErrorMessage
	e.LastWarnTs = r.LastWarnTs
	e.LastWarnMessage = r.LastWarnMessage
	return nil
}

func (e ErrorSummary) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ErrorCount30m    uint64  `json:"error_count_30m"`
		WarnCount30m     uint64  `json:"warn_count_30m"`
		ErrorCountTotal  uint64  `json:"error_count_total"`
		WarnCountTotal   uint64  `json:"warn_count_total"`
		LastErrorTs      *int64  `json:"last_error_ts,omitempty"`
		LastErrorMessage *string `json:"last_error_message,omitempty"`
		LastWarnTs       *int64  `json:"last_warn_ts,omitempty"`
		LastWarnMessage  *string `json:"last_warn_message,omitempty"`
	}{
		ErrorCount30m:    e.ErrorCountWindow,
		WarnCount30m:     e.WarnCountWindow,
		ErrorCountTotal:  e.ErrorCountTotal,
		WarnCountTotal:   e.WarnCountTotal,
		LastErrorTs:      e.LastErrorTs,
		LastErrorMessage: e.LastErrorMessage,
		LastWarnTs:       e.LastWarnTs,
		LastWarnMessage:  e.LastWarnMessage,
	})
}

type EquityPoint struct {
	TS     int64   `json:"ts"`
	Equity float64 `json:"equity"`
}

type TargetStatus struct {
	Name             string      `json:"name"`
	InstanceID       string      `json:"instance_id"`
	Service          string      `json:"service"`
	Region           string      `json:"region"`
	ServiceStatus    string      `json:"service_status"`
	ServiceStartedAt *time.Time  `json:"service_started_at,omitempty"`
	Status           *StatusData `json:"status,omitempty"`
	// WsReset24h is the count of `Connection reset without closing handshake`
	// WebSocket events over the last 24 hours, self-reported by the bot
	// via `WsReset24hCount` in status.json (bot-strategy#343). Alerting
	// threshold is 25/day per bot-strategy#47 (raised from 10, see #547).
	WsReset24h *int `json:"ws_reset_24h,omitempty"`
	// KillSwitchActive reports whether `/opt/debot/KILL_SWITCH` exists
	// on the target instance, self-reported by the bot via status.json
	// (bot-strategy#343). True → operator-triggered halt file is
	// present; see bot-strategy#185.
	KillSwitchActive *bool     `json:"kill_switch_active,omitempty"`
	Error            string    `json:"error,omitempty"`
	CheckedAt        time.Time `json:"checked_at"`
}

type DashboardSnapshot struct {
	UpdatedAt        time.Time      `json:"updated_at"`
	PollIntervalSecs int            `json:"poll_interval_secs"`
	Targets          []TargetStatus `json:"targets"`
}

type StatusCache struct {
	mu       sync.RWMutex
	snapshot DashboardSnapshot
}

func (c *StatusCache) Set(snapshot DashboardSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = snapshot
}

func (c *StatusCache) Get() DashboardSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// S3ClientPool is keyed on the bucket's region. pairtrade / xvenue-arb
// both write to `debot-dashboard` in eu-central-1 today, so all targets
// typically share one entry. bot-strategy#343.
type S3ClientPool struct {
	mu      sync.Mutex
	clients map[string]*s3.Client
}

func (p *S3ClientPool) Client(ctx context.Context, region string) (*s3.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[region]; ok {
		return client, nil
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)
	p.clients[region] = client
	return client, nil
}

func main() {
	var cfgPath string
	var listen string
	flag.StringVar(&cfgPath, "config", "config.yaml", "path to config YAML")
	flag.StringVar(&listen, "listen", ":8080", "listen address")
	flag.Parse()

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}
	if err := normalizeConfig(&cfg); err != nil {
		log.Fatalf("config invalid: %v", err)
	}
	auth, err := resolveAuth(cfg)
	if err != nil {
		log.Fatalf("auth config invalid: %v", err)
	}

	cache := &StatusCache{}
	s3pool := &S3ClientPool{clients: map[string]*s3.Client{}}
	mc := newMetricsCollector()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollLoop(ctx, cfg, s3pool, cache, mc)

	mux := http.NewServeMux()
	mux.Handle("/metrics", mc.Handler())
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		rangeParam := strings.TrimSpace(r.URL.Query().Get("range"))
		includeHistory, cutoffMs := historyCutoff(rangeParam)
		if includeHistory {
			snapshot := fetchAll(ctx, cfg, s3pool, true, cutoffMs)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		}
		snapshot := cache.Get()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	fileServer := http.FileServer(http.Dir("web"))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" ||
			strings.HasSuffix(path, ".html") ||
			strings.HasSuffix(path, ".js") ||
			strings.HasSuffix(path, ".css") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	}))

	log.Printf("debot-dashboard listening on %s", listen)
	if err := http.ListenAndServe(listen, withAuth(mux, auth)); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) error {
	if cfg.PollIntervalSecs <= 0 {
		cfg.PollIntervalSecs = defaultPollIntervalSecs
	}
	if len(cfg.Targets) == 0 {
		return errors.New("no targets configured")
	}
	for i := range cfg.Targets {
		target := &cfg.Targets[i]
		if target.Service == "" {
			return fmt.Errorf("targets[%d] missing service", i)
		}
		if target.Name == "" {
			target.Name = target.Service
		}
		if target.Region == "" {
			target.Region = cfg.Region
		}
		if target.Region == "" {
			return fmt.Errorf("targets[%d] missing region", i)
		}
		// Bucket + key are required; the dashboard always reads from
		// S3. instance_id is kept for FE labeling and disk-watch
		// disambiguation but is no longer required to reach the bot.
		if target.S3Bucket == "" {
			return fmt.Errorf("targets[%d] missing s3_bucket", i)
		}
		if target.S3Key == "" {
			return fmt.Errorf("targets[%d] missing s3_key", i)
		}
	}
	return nil
}

type BasicAuth struct {
	Username string
	Password string
	Enabled  bool
}

func resolveAuth(cfg Config) (BasicAuth, error) {
	userEnv := strings.TrimSpace(os.Getenv("DEBOT_DASHBOARD_USERNAME"))
	passEnv := os.Getenv("DEBOT_DASHBOARD_PASSWORD")
	if userEnv != "" || passEnv != "" {
		if userEnv == "" || passEnv == "" {
			return BasicAuth{}, errors.New("DEBOT_DASHBOARD_USERNAME and DEBOT_DASHBOARD_PASSWORD must both be set")
		}
		return BasicAuth{Username: userEnv, Password: passEnv, Enabled: true}, nil
	}
	if cfg.Auth == nil {
		return BasicAuth{}, nil
	}
	userCfg := strings.TrimSpace(cfg.Auth.Username)
	passCfg := cfg.Auth.Password
	if userCfg == "" && passCfg == "" {
		return BasicAuth{}, nil
	}
	if userCfg == "" || passCfg == "" {
		return BasicAuth{}, errors.New("auth.username and auth.password must both be set")
	}
	return BasicAuth{Username: userCfg, Password: passCfg, Enabled: true}, nil
}

func withAuth(next http.Handler, auth BasicAuth) http.Handler {
	if !auth.Enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || !matchBasicAuth(user, pass, auth) {
			w.Header().Set("WWW-Authenticate", `Basic realm="debot-dashboard"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func matchBasicAuth(user, pass string, auth BasicAuth) bool {
	if user == "" || pass == "" {
		return false
	}
	userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(auth.Username)) == 1
	passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(auth.Password)) == 1
	return userMatch && passMatch
}

func pollLoop(ctx context.Context, cfg Config, s3pool *S3ClientPool, cache *StatusCache, mc *metricsCollector) {
	pollInterval := time.Duration(cfg.PollIntervalSecs) * time.Second
	fetch := func() {
		snapshot := fetchAll(ctx, cfg, s3pool, false, 0)
		cache.Set(snapshot)
		if mc != nil {
			mc.Update(snapshot)
		}
	}
	fetch()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

func fetchAll(ctx context.Context, cfg Config, s3pool *S3ClientPool, includeHistory bool, cutoffMs int64) DashboardSnapshot {
	results := make([]TargetStatus, len(cfg.Targets))
	var wg sync.WaitGroup
	for i, target := range cfg.Targets {
		i := i
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = fetchTargetS3(ctx, target, s3pool, includeHistory, cutoffMs)
		}()
	}
	wg.Wait()
	return DashboardSnapshot{
		UpdatedAt:        time.Now(),
		PollIntervalSecs: cfg.PollIntervalSecs,
		Targets:          results,
	}
}

// fetchTargetS3 reads `<bucket>/<key>` (= status.json mirrored by the
// bot-side `STATUS_S3_*` path, bot-strategy#343) plus, when
// `includeHistory` is set, the sibling `*.equity_history.jsonl`.
//
// Service liveness is derived from object freshness: an object older
// than `s3StatusStaleSecs` flips the target to "stale". This is
// strictly more accurate than the legacy `systemctl is-active` probe —
// the latter said "active" even when the bot was hung and not making
// progress, while object staleness catches both crashes and stalls.
//
// `KillSwitchActive` and `WsReset24h` come straight from the JSON
// payload (#343), no separate probes needed.
func fetchTargetS3(ctx context.Context, target TargetConfig, s3pool *S3ClientPool, includeHistory bool, cutoffMs int64) TargetStatus {
	result := TargetStatus{
		Name:       target.Name,
		InstanceID: target.InstanceID,
		Service:    target.Service,
		Region:     target.Region,
		CheckedAt:  time.Now(),
	}
	if target.S3Bucket == "" || target.S3Key == "" {
		result.Error = "target missing s3_bucket / s3_key"
		return result
	}
	bucketRegion := target.S3Region
	if bucketRegion == "" {
		bucketRegion = target.Region
	}
	client, err := s3pool.Client(ctx, bucketRegion)
	if err != nil {
		result.Error = fmt.Sprintf("s3 client error: %v", err)
		return result
	}

	getCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	obj, err := client.GetObject(getCtx, &s3.GetObjectInput{
		Bucket: aws.String(target.S3Bucket),
		Key:    aws.String(target.S3Key),
	})
	if err != nil {
		result.Error = fmt.Sprintf("s3 get error: %v", err)
		return result
	}
	body, err := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	if err != nil {
		result.Error = fmt.Sprintf("s3 read error: %v", err)
		return result
	}
	if len(body) == 0 {
		result.Error = "s3 status object is empty"
		return result
	}

	status, err := decodeStatusPayload(body)
	if err != nil {
		result.Error = fmt.Sprintf("s3 parse error: %v", err)
		return result
	}

	// Derive service_status from object freshness — there is no
	// systemctl probe in this code path.
	now := time.Now().Unix()
	age := now - status.TS
	if status.TS == 0 {
		result.ServiceStatus = "unknown"
	} else if age > s3StatusStaleSecs {
		result.ServiceStatus = "stale"
	} else {
		result.ServiceStatus = "active"
	}
	if status.ProcessStartedAt != 0 {
		started := time.Unix(status.ProcessStartedAt, 0).UTC()
		result.ServiceStartedAt = &started
	}
	if status.WsReset24hCount != 0 || status.TS != 0 {
		// Always surface zero when the bot is alive — distinguishes
		// "0 resets in 24h" from "no data" (nil).
		ws := int(status.WsReset24hCount)
		result.WsReset24h = &ws
	}
	ks := status.KillSwitchActive
	result.KillSwitchActive = &ks

	if includeHistory {
		historyKey := strings.TrimSuffix(target.S3Key, ".json") + ".equity_history.jsonl"
		histCtx, histCancel := context.WithTimeout(ctx, commandTimeout)
		defer histCancel()
		hist, err := client.GetObject(histCtx, &s3.GetObjectInput{
			Bucket: aws.String(target.S3Bucket),
			Key:    aws.String(historyKey),
		})
		if err == nil {
			payload, readErr := io.ReadAll(hist.Body)
			_ = hist.Body.Close()
			if readErr == nil {
				status.EquityHistory = parseEquityHistory(string(payload), cutoffMs)
			}
		}
		// Missing history is not an error — the bot may not have
		// written one yet.
	}
	result.Status = &status
	return result
}

func decodeStatusPayload(payload []byte) (StatusData, error) {
	var status StatusData
	if err := json.Unmarshal(payload, &status); err != nil {
		return StatusData{}, err
	}
	if status.Accumulator != nil && status.SchemaVersion != accumulatorSchemaVersion {
		return StatusData{}, fmt.Errorf(
			"unsupported accumulator schema_version %d (want %d)",
			status.SchemaVersion,
			accumulatorSchemaVersion,
		)
	}
	return status, nil
}

func historyCutoff(rangeParam string) (bool, int64) {
	if rangeParam == "" {
		return false, 0
	}
	now := time.Now()
	switch strings.ToLower(rangeParam) {
	case "1d":
		return true, now.Add(-24 * time.Hour).UnixMilli()
	case "1w":
		return true, now.Add(-7 * 24 * time.Hour).UnixMilli()
	case "1m":
		return true, now.Add(-30 * 24 * time.Hour).UnixMilli()
	case "all":
		return true, 0
	default:
		return true, now.Add(-24 * time.Hour).UnixMilli()
	}
}

func parseEquityHistory(payload string, cutoffMs int64) []EquityPoint {
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	lines := strings.Split(payload, "\n")
	points := make([]EquityPoint, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var point EquityPoint
		if err := json.Unmarshal([]byte(line), &point); err != nil {
			continue
		}
		if cutoffMs > 0 && point.TS < cutoffMs {
			continue
		}
		points = append(points, point)
	}
	return points
}
