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
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"gopkg.in/yaml.v3"
)

const (
	defaultPollIntervalSecs = 20
	commandTimeout          = 15 * time.Second
	commandPollInterval     = 500 * time.Millisecond
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
}

type StatusPosition struct {
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Size       string  `json:"size"`
	EntryPrice *string `json:"entry_price"`
}

type StatusData struct {
	TS             int64            `json:"ts"`
	UpdatedAt      string           `json:"updated_at"`
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
	EquityHistory  []EquityPoint    `json:"equity_history,omitempty"`
}

type EquityPoint struct {
	TS     int64   `json:"ts"`
	Equity float64 `json:"equity"`
}

type TargetStatus struct {
	Name          string      `json:"name"`
	InstanceID    string      `json:"instance_id"`
	Service       string      `json:"service"`
	StatusPath    string      `json:"status_path"`
	Region        string      `json:"region"`
	ServiceStatus string      `json:"service_status"`
	Status        *StatusData `json:"status,omitempty"`
	Error         string      `json:"error,omitempty"`
	CheckedAt     time.Time   `json:"checked_at"`
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollLoop(ctx, cfg, pool, cache)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		rangeParam := strings.TrimSpace(r.URL.Query().Get("range"))
		includeHistory, cutoffMs := historyCutoff(rangeParam)
		if includeHistory {
			snapshot := fetchAll(ctx, cfg, pool, true, cutoffMs)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		}
		snapshot := cache.Get()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	mux.Handle("/", http.FileServer(http.Dir("web")))

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
		if target.InstanceID == "" {
			return fmt.Errorf("targets[%d] missing instance_id", i)
		}
		if target.StatusPath == "" {
			return fmt.Errorf("targets[%d] missing status_path", i)
		}
		if target.Region == "" {
			target.Region = cfg.Region
		}
		if target.Region == "" {
			return fmt.Errorf("targets[%d] missing region", i)
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

func pollLoop(ctx context.Context, cfg Config, pool *ClientPool, cache *StatusCache) {
	pollInterval := time.Duration(cfg.PollIntervalSecs) * time.Second
	fetch := func() {
		snapshot := fetchAll(ctx, cfg, pool, false, 0)
		cache.Set(snapshot)
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

func fetchAll(ctx context.Context, cfg Config, pool *ClientPool, includeHistory bool, cutoffMs int64) DashboardSnapshot {
	results := make([]TargetStatus, len(cfg.Targets))
	var wg sync.WaitGroup
	for i, target := range cfg.Targets {
		i := i
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = fetchTarget(ctx, target, pool, includeHistory, cutoffMs)
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

	serviceStatus, status, parseErr := parseOutput(stdout, includeHistory, cutoffMs)
	result.ServiceStatus = serviceStatus
	result.Status = status
	if parseErr != nil {
		result.Error = fmt.Sprintf("parse error: %v", parseErr)
	}
	if strings.TrimSpace(stderr) != "" && result.Error == "" {
		result.Error = fmt.Sprintf("stderr: %s", strings.TrimSpace(stderr))
	}
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
	cmd := fmt.Sprintf("systemctl is-active %s 2>/dev/null || true; echo \"__STATUS__\"; if [ -f %s ]; then cat %s; fi", service, path, path)
	if includeHistory {
		historyPath := shellEscape(equityHistoryPath(target.StatusPath))
		cmd = fmt.Sprintf("%s; echo \"__HISTORY__\"; if [ -f %s ]; then cat %s; fi", cmd, historyPath, historyPath)
	}
	return cmd
}

func parseOutput(output string, includeHistory bool, cutoffMs int64) (string, *StatusData, error) {
	parts := strings.SplitN(output, "__STATUS__", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(output), nil, errors.New("missing status marker")
	}
	serviceStatus := strings.TrimSpace(parts[0])
	payload := strings.TrimSpace(parts[1])
	historyPayload := ""
	if includeHistory {
		historyParts := strings.SplitN(payload, "__HISTORY__", 2)
		payload = strings.TrimSpace(historyParts[0])
		if len(historyParts) == 2 {
			historyPayload = strings.TrimSpace(historyParts[1])
		}
	}
	if payload == "" {
		return serviceStatus, nil, errors.New("status.json is empty")
	}
	var status StatusData
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		return serviceStatus, nil, err
	}
	if includeHistory {
		status.EquityHistory = parseEquityHistory(historyPayload, cutoffMs)
	}
	return serviceStatus, &status, nil
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
