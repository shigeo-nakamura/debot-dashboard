package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"gopkg.in/yaml.v3"
	"io"
)

const (
	defaultPollIntervalSecs = 20
	commandTimeout          = 15 * time.Second
	commandPollInterval     = 500 * time.Millisecond
	// Status objects older than this are reported as "stale" — replaces
	// the SSM `systemctl is-active` probe when the target source is
	// "s3" (#343). Bot status writers emit at 60s cadence, so 180s gives
	// 3× headroom before the dashboard flips a target to stale.
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
	StatusPath string `yaml:"status_path"`
	Region     string `yaml:"region"`
	// Source selects how the dashboard reads this target's status.json.
	//   "" or "ssm" → original SSM SendCommand probe (back-compat).
	//   "s3"        → read from S3 (#343); bot mirrors via STATUS_S3_*.
	// Per-target so cutover happens one bot at a time.
	Source string `yaml:"source"`
	// S3-source-only: bucket name and full key for the bot's
	// `<id>.json` object. Sibling files (equity_history.jsonl,
	// backtest_alert.json) are derived by suffix-replacing the key.
	S3Bucket string `yaml:"s3_bucket"`
	S3Key    string `yaml:"s3_key"`
	// S3-source-only: AWS region of the bucket. Optional; falls back
	// to `region` when empty for back-compat. Split out so that
	// `region` can keep its display meaning ("where the bot runs")
	// even when the bucket lives elsewhere — otherwise a Tokyo bot
	// writing to a Frankfurt bucket would have to advertise itself
	// as Frankfurt and end up under the wrong region group on the FE.
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
	Key              string `json:"key"`
	EnteredTS        int64  `json:"entered_ts"`
	ForceCloseEtaTS  int64  `json:"force_close_eta_ts"`
}

type ShutdownStatus struct {
	Pending          bool               `json:"pending"`
	GraceDeadlineTS  int64              `json:"grace_deadline_ts"`
	ForceCloseEtaTS  *int64             `json:"force_close_eta_ts"`
	Positions        []ShutdownPosition `json:"positions"`
}

type StatusData struct {
	TS             int64            `json:"ts"`
	UpdatedAt      string           `json:"updated_at"`
	// ProcessStartedAt is the bot process boot time (epoch s),
	// emitted by both pairtrade and xvenue-arb since #343 phase 1.
	// Replaces the SSM `systemctl ActiveEnterTimestamp` probe when
	// the target's source is "s3". Optional (zero in pre-#343 bots).
	ProcessStartedAt int64 `json:"process_started_at,omitempty"`
	// WsReset24hCount mirrors the dashboard's old journalctl
	// `Connection reset...` probe via #343. Bot self-reports the
	// 24h count.
	WsReset24hCount  uint64 `json:"ws_reset_24h_count,omitempty"`
	// KillSwitchActive mirrors the SSM `cat /opt/debot/KILL_SWITCH`
	// probe via #343.
	KillSwitchActive bool   `json:"kill_switch_active,omitempty"`
	ID             *string          `json:"id"`
	Agent          *string          `json:"agent"`
	Dex            string           `json:"dex"`
	DryRun         bool             `json:"dry_run"`
	BacktestMode   bool             `json:"backtest_mode"`
	IntervalSecs   int              `json:"interval_secs"`
	PositionsReady bool             `json:"positions_ready"`
	PositionCount  int              `json:"position_count"`
	HasPosition    bool             `json:"has_position"`
	Positions      []StatusPosition `json:"positions"`
	PnlTotal       float64          `json:"pnl_total"`
	PnlToday       float64          `json:"pnl_today"`
	PnlSource      string           `json:"pnl_source"`
	TradeStats     *TradeStats            `json:"trade_stats,omitempty"`
	Maintenance    *string                `json:"maintenance,omitempty"`
	Shutdown       *ShutdownStatus        `json:"shutdown,omitempty"`
	ErrorSummary   *ErrorSummary          `json:"error_summary,omitempty"`
	EquityHistory  []EquityPoint          `json:"equity_history,omitempty"`
	BacktestAlert  map[string]interface{} `json:"backtest_alert,omitempty"`
	// Risk gates emitted by pairtrade since bot-strategy#185.
	// All three may be nil when the threshold is disabled (the bot
	// skips emission to keep status.json compact). The dashboard
	// renders a halt pill / progress panel only when the field is
	// present.
	DailyRisk      *DailyRiskSnapshot      `json:"daily_risk,omitempty"`
	SessionRisk    *SessionRiskSnapshot    `json:"session_risk,omitempty"`
	CircuitBreaker *CircuitBreakerSnapshot `json:"circuit_breaker,omitempty"`
	// RiskHistory is a bounded ring buffer (cap 200) of recent halt
	// transitions emitted by pairtrade since 9755490. Empty when no
	// gate has ever fired (or for old bots not yet restarted with the
	// Phase B binary).
	RiskHistory []RiskHistoryEvent `json:"risk_history,omitempty"`
}

// RiskHistoryEvent mirrors pairtrade's RiskHistoryEvent (status.rs).
// The dashboard renders these as colored dots on a 30-d strip below
// each card's risk panel; severity color comes from `kind`. Detail
// is forwarded raw to the FE which extracts whichever fields it
// wants for the tooltip.
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
	StatusPath       string      `json:"status_path"`
	Region           string      `json:"region"`
	ServiceStatus    string      `json:"service_status"`
	ServiceStartedAt *time.Time  `json:"service_started_at,omitempty"`
	Status           *StatusData `json:"status,omitempty"`
	// WsReset24h is the count of `Connection reset without closing handshake`
	// WebSocket events observed in the service's journald log over the last
	// 24 hours. Sourced via SSM (journalctl) rather than the bot, so it works
	// without any bot-side changes. Threshold for alerting is 10/day per
	// bot-strategy#47.
	WsReset24h       *int      `json:"ws_reset_24h,omitempty"`
	// KillSwitchActive reports whether /opt/debot/KILL_SWITCH exists on the
	// target instance. Sourced via SSM, independent of any bot-side code.
	// True → operator-triggered halt file is present; see bot-strategy#185.
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

type ClientPool struct {
	mu      sync.Mutex
	clients map[string]*ssm.Client
}

func (p *ClientPool) Client(ctx context.Context, region string) (*ssm.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[region]; ok {
		return client, nil
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := ssm.NewFromConfig(cfg)
	p.clients[region] = client
	return client, nil
}

// S3ClientPool serves the same role for the `source: s3` path
// (bot-strategy#343). Region is the bucket's region — pairtrade /
// xvenue-arb both write to `debot-dashboard` in eu-central-1, so all
// targets typically share one entry.
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
	pool := &ClientPool{clients: map[string]*ssm.Client{}}
	s3pool := &S3ClientPool{clients: map[string]*s3.Client{}}
	mc := newMetricsCollector()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollLoop(ctx, cfg, pool, s3pool, cache, mc)

	mux := http.NewServeMux()
	mux.Handle("/metrics", mc.Handler())
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		rangeParam := strings.TrimSpace(r.URL.Query().Get("range"))
		includeHistory, cutoffMs := historyCutoff(rangeParam)
		if includeHistory {
			snapshot := fetchAll(ctx, cfg, pool, s3pool, true, cutoffMs)
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
		// S3-source targets need bucket+key; SSM-source targets need
		// instance_id+status_path. instance_id and status_path are
		// kept on S3 targets for FE rendering / labeling but are no
		// longer required to reach the bot.
		if strings.EqualFold(target.Source, "s3") {
			if target.S3Bucket == "" {
				return fmt.Errorf("targets[%d] (source=s3) missing s3_bucket", i)
			}
			if target.S3Key == "" {
				return fmt.Errorf("targets[%d] (source=s3) missing s3_key", i)
			}
		} else {
			if target.InstanceID == "" {
				return fmt.Errorf("targets[%d] missing instance_id", i)
			}
			if target.StatusPath == "" {
				return fmt.Errorf("targets[%d] missing status_path", i)
			}
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

func pollLoop(ctx context.Context, cfg Config, pool *ClientPool, s3pool *S3ClientPool, cache *StatusCache, mc *metricsCollector) {
	pollInterval := time.Duration(cfg.PollIntervalSecs) * time.Second
	fetch := func() {
		snapshot := fetchAll(ctx, cfg, pool, s3pool, false, 0)
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

func fetchAll(ctx context.Context, cfg Config, pool *ClientPool, s3pool *S3ClientPool, includeHistory bool, cutoffMs int64) DashboardSnapshot {
	results := make([]TargetStatus, len(cfg.Targets))
	var wg sync.WaitGroup
	for i, target := range cfg.Targets {
		i := i
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			if strings.EqualFold(target.Source, "s3") {
				results[i] = fetchTargetS3(ctx, target, s3pool, includeHistory, cutoffMs)
			} else {
				results[i] = fetchTarget(ctx, target, pool, includeHistory, cutoffMs)
			}
		}()
	}
	wg.Wait()
	return DashboardSnapshot{
		UpdatedAt:        time.Now(),
		PollIntervalSecs: cfg.PollIntervalSecs,
		Targets:          results,
	}
}

func fetchTarget(ctx context.Context, target TargetConfig, pool *ClientPool, includeHistory bool, cutoffMs int64) TargetStatus {
	result := TargetStatus{
		Name:       target.Name,
		InstanceID: target.InstanceID,
		Service:    target.Service,
		StatusPath: target.StatusPath,
		Region:     target.Region,
		CheckedAt:  time.Now(),
	}
	client, err := pool.Client(ctx, target.Region)
	if err != nil {
		result.Error = fmt.Sprintf("ssm client error: %v", err)
		return result
	}

	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	stdout, stderr, err := runCommand(cmdCtx, client, target, includeHistory)
	if err != nil {
		result.Error = fmt.Sprintf("ssm command error: %v", err)
		if strings.TrimSpace(stderr) != "" {
			result.Error = fmt.Sprintf("%s (stderr: %s)", result.Error, strings.TrimSpace(stderr))
		}
		return result
	}

	serviceStatus, serviceStart, status, wsReset, killSwitch, parseErr := parseOutput(stdout, includeHistory, cutoffMs)
	result.ServiceStatus = serviceStatus
	if serviceStart != nil {
		utc := serviceStart.UTC()
		result.ServiceStartedAt = &utc
	}
	result.Status = status
	result.WsReset24h = wsReset
	result.KillSwitchActive = killSwitch
	if parseErr != nil {
		result.Error = fmt.Sprintf("parse error: %v", parseErr)
	}
	if strings.TrimSpace(stderr) != "" && result.Error == "" {
		result.Error = fmt.Sprintf("stderr: %s", strings.TrimSpace(stderr))
	}
	return result
}

// fetchTargetS3 reads `<bucket>/<key>` (= status.json mirrored by the
// bot-side `STATUS_S3_*` path, bot-strategy#343 phase 1) plus, when
// `includeHistory` is set, the sibling `*.equity_history.jsonl`.
//
// Service liveness moves from `systemctl is-active` to "object age <
// s3StatusStaleSecs". This is strictly more accurate than the SSM
// probe — the latter says "active" even when the bot is hung and
// not making progress, while object staleness catches both crashes
// and stalls.
//
// `KillSwitchActive` and `WsReset24h` come straight from the JSON
// payload now (#343 phase 2 fields), no separate probes needed.
func fetchTargetS3(ctx context.Context, target TargetConfig, s3pool *S3ClientPool, includeHistory bool, cutoffMs int64) TargetStatus {
	result := TargetStatus{
		Name:       target.Name,
		InstanceID: target.InstanceID,
		Service:    target.Service,
		StatusPath: target.StatusPath,
		Region:     target.Region,
		CheckedAt:  time.Now(),
	}
	if target.S3Bucket == "" || target.S3Key == "" {
		result.Error = "s3 source target missing s3_bucket / s3_key"
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

	var status StatusData
	if err := json.Unmarshal(body, &status); err != nil {
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

func runCommand(ctx context.Context, client *ssm.Client, target TargetConfig, includeHistory bool) (string, string, error) {
	cmd := buildCommand(target, includeHistory)
	sendOut, err := client.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String("AWS-RunShellScript"),
		InstanceIds:  []string{target.InstanceID},
		Parameters: map[string][]string{
			"commands": {cmd},
		},
	})
	if err != nil {
		return "", "", err
	}
	commandID := aws.ToString(sendOut.Command.CommandId)

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		default:
		}
		out, err := client.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(commandID),
			InstanceId: aws.String(target.InstanceID),
		})
		if err != nil {
			if strings.Contains(err.Error(), "InvocationDoesNotExist") {
				time.Sleep(commandPollInterval)
				continue
			}
			return "", "", err
		}
		switch out.Status {
		case ssmtypes.CommandInvocationStatusSuccess:
			return aws.ToString(out.StandardOutputContent), aws.ToString(out.StandardErrorContent), nil
		case ssmtypes.CommandInvocationStatusCancelled,
			ssmtypes.CommandInvocationStatusFailed,
			ssmtypes.CommandInvocationStatusTimedOut:
			return aws.ToString(out.StandardOutputContent), aws.ToString(out.StandardErrorContent),
				fmt.Errorf("command status: %s", out.Status)
		default:
			time.Sleep(commandPollInterval)
		}
	}
}

func buildCommand(target TargetConfig, includeHistory bool) string {
	service := shellEscape(target.Service)
	path := shellEscape(target.StatusPath)
	alertPath := shellEscape(backtestAlertPath(target.StatusPath))
	// awk is used (instead of `grep -c ... || echo 0`) so the count prints
	// exactly once even when there are zero matches — `grep -c` on empty
	// input exits 1, which would otherwise double-print via the `||` fallback.
	cmd := fmt.Sprintf(
		"systemctl is-active %s 2>/dev/null || true; echo \"__START__\"; systemctl show -p ActiveEnterTimestamp --value %s 2>/dev/null || true; echo \"__STATUS__\"; if [ -f %s ]; then cat %s; fi; echo \"__ALERT__\"; if [ -f %s ]; then cat %s; fi; echo \"__WS_RESET__\"; journalctl -u %s --since '24 hours ago' --no-pager 2>/dev/null | awk '/Connection reset without closing handshake/ {c++} END {print c+0}'; echo \"__KILL_SWITCH__\"; if [ -f /opt/debot/KILL_SWITCH ]; then echo 1; else echo 0; fi",
		service,
		service,
		path,
		path,
		alertPath,
		alertPath,
		service,
	)
	if includeHistory {
		historyPath := shellEscape(equityHistoryPath(target.StatusPath))
		cmd = fmt.Sprintf("%s; echo \"__HISTORY__\"; if [ -f %s ]; then cat %s; fi", cmd, historyPath, historyPath)
	}
	return cmd
}

func parseOutput(output string, includeHistory bool, cutoffMs int64) (string, *time.Time, *StatusData, *int, *bool, error) {
	parts := strings.SplitN(output, "__STATUS__", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(output), nil, nil, nil, nil, errors.New("missing status marker")
	}
	startParts := strings.SplitN(parts[0], "__START__", 2)
	serviceStatus := strings.TrimSpace(startParts[0])
	var serviceStart *time.Time
	if len(startParts) == 2 {
		serviceStart = parseServiceStart(strings.TrimSpace(startParts[1]))
	}

	// Tail payload after __STATUS__:
	//   status.json [__ALERT__ alert.json] [__WS_RESET__ count] [__KILL_SWITCH__ 0|1] [__HISTORY__ equity.jsonl]
	tail := strings.TrimSpace(parts[1])
	payload := tail
	alertPayload := ""
	wsResetPayload := ""
	killSwitchPayload := ""
	historyPayload := ""

	if idx := strings.Index(tail, "__ALERT__"); idx >= 0 {
		payload = strings.TrimSpace(tail[:idx])
		tail = tail[idx+len("__ALERT__"):]
	}
	if idx := strings.Index(tail, "__WS_RESET__"); idx >= 0 {
		alertPayload = strings.TrimSpace(tail[:idx])
		tail = tail[idx+len("__WS_RESET__"):]
	} else if !strings.HasPrefix(strings.TrimSpace(tail), "__HISTORY__") {
		alertPayload = strings.TrimSpace(tail)
		tail = ""
	}
	if idx := strings.Index(tail, "__KILL_SWITCH__"); idx >= 0 {
		wsResetPayload = strings.TrimSpace(tail[:idx])
		tail = tail[idx+len("__KILL_SWITCH__"):]
	}
	if idx := strings.Index(tail, "__HISTORY__"); idx >= 0 {
		if killSwitchPayload == "" && wsResetPayload == "" {
			// Back-compat: server without __KILL_SWITCH__ marker; tail between
			// __WS_RESET__ and __HISTORY__ is still ws-reset count only.
			wsResetPayload = strings.TrimSpace(tail[:idx])
		} else {
			killSwitchPayload = strings.TrimSpace(tail[:idx])
		}
		tail = tail[idx+len("__HISTORY__"):]
		historyPayload = strings.TrimSpace(tail)
	} else if killSwitchPayload == "" && wsResetPayload == "" {
		wsResetPayload = strings.TrimSpace(tail)
	} else {
		killSwitchPayload = strings.TrimSpace(tail)
	}

	if payload == "" {
		return serviceStatus, serviceStart, nil, nil, nil, errors.New("status.json is empty")
	}
	var status StatusData
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		return serviceStatus, serviceStart, nil, nil, nil, err
	}
	if alertPayload != "" {
		var alert map[string]interface{}
		if err := json.Unmarshal([]byte(alertPayload), &alert); err == nil {
			status.BacktestAlert = alert
		}
	}
	if includeHistory {
		status.EquityHistory = parseEquityHistory(historyPayload, cutoffMs)
	}

	var wsReset *int
	if wsResetPayload != "" {
		// wsResetPayload may include additional shell chatter; scan the last
		// non-empty line as the count (journalctl | grep -c prints a single
		// integer).
		lines := strings.Split(wsResetPayload, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			if n, err := strconv.Atoi(line); err == nil {
				wsReset = &n
			}
			break
		}
	}

	var killSwitch *bool
	if killSwitchPayload != "" {
		lines := strings.Split(killSwitchPayload, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			active := line == "1"
			killSwitch = &active
			break
		}
	}
	return serviceStatus, serviceStart, &status, wsReset, killSwitch, nil
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

func equityHistoryPath(statusPath string) string {
	ext := filepath.Ext(statusPath)
	base := strings.TrimSuffix(statusPath, ext)
	if base == "" {
		base = statusPath
	}
	return base + ".equity_history.jsonl"
}

func backtestAlertPath(statusPath string) string {
	dir := filepath.Dir(statusPath)
	return filepath.Join(dir, "backtest_alert.json")
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

func shellEscape(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func parseServiceStart(raw string) *time.Time {
	text := strings.TrimSpace(raw)
	if text == "" || strings.EqualFold(text, "n/a") {
		return nil
	}
	layouts := []string{
		"Mon 2006-01-02 15:04:05 MST",
		time.RFC1123,
		time.RFC1123Z,
		time.RFC3339,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return &parsed
		}
	}
	return nil
}
