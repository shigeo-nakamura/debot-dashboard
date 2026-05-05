package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsCollector wraps a private prometheus.Registry plus the metric
// vectors we emit and the per-target derived state needed for "time since
// last trade" and "risk events seen". See bot-strategy#314 (Phase 1 PoC).
type metricsCollector struct {
	registry *prometheus.Registry

	// Group 1 — trade activity.
	trades                *prometheus.GaugeVec
	wins                  *prometheus.GaugeVec
	winRate               *prometheus.GaugeVec
	pnlTotal              *prometheus.GaugeVec
	pnlToday              *prometheus.GaugeVec
	positionCount         *prometheus.GaugeVec
	hasPosition           *prometheus.GaugeVec
	timeSinceLastTradeSec *prometheus.GaugeVec
	statusAgeSec          *prometheus.GaugeVec

	// Group 2 — DD timing.
	dailyPnlUsd        *prometheus.GaugeVec
	dailyPnlBps        *prometheus.GaugeVec
	dailyMaxLossBps    *prometheus.GaugeVec
	dailyRiskHalted    *prometheus.GaugeVec
	sessionStartTS     *prometheus.GaugeVec
	currentEquity      *prometheus.GaugeVec
	peakEquity         *prometheus.GaugeVec
	sessionDDBps       *prometheus.GaugeVec
	sessionMaxLossBps  *prometheus.GaugeVec
	sessionHalted      *prometheus.GaugeVec
	sessionHaltTS      *prometheus.GaugeVec

	// Group 3 — risk halt state.
	cbActive             *prometheus.GaugeVec
	cbConsecutiveLosses  *prometheus.GaugeVec
	cbUntilTS            *prometheus.GaugeVec
	cbCooldownRemaining  *prometheus.GaugeVec
	killSwitchActive     *prometheus.GaugeVec
	shutdownPending      *prometheus.GaugeVec
	riskEventsTotal      *prometheus.CounterVec

	// Group 6 — system health.
	serviceActive       *prometheus.GaugeVec
	serviceStartedTS    *prometheus.GaugeVec
	processUptimeSec    *prometheus.GaugeVec
	wsReset24h          *prometheus.GaugeVec
	errorCount30m       *prometheus.GaugeVec
	warnCount30m        *prometheus.GaugeVec
	errorCountTotal     *prometheus.GaugeVec
	warnCountTotal      *prometheus.GaugeVec
	lastErrorTS         *prometheus.GaugeVec
	lastWarnTS          *prometheus.GaugeVec

	// Group 7 — config visibility.
	dryRun       *prometheus.GaugeVec
	backtestMode *prometheus.GaugeVec

	// Phase 2 derived "when X happened" timestamps (answers the
	// user's "when did the DD start / when did we last trade"
	// question without needing pairtrade-side instrumentation).
	lastTradeTS               *prometheus.GaugeVec
	peakEquityLastChangedTS   *prometheus.GaugeVec

	// Dashboard self-metrics.
	pollErrorsTotal *prometheus.CounterVec
	targetsTotal    prometheus.Gauge
	lastPollTS      prometheus.Gauge

	// Per-target derived state. Keyed by target name (config.yaml `name`).
	mu              sync.Mutex
	lastTradeCount  map[string]int64
	tradeChangedAt  map[string]time.Time
	lastRiskEventTS map[string]int64
	lastPeakEquity  map[string]float64
	peakChangedAt   map[string]time.Time
	// startedAt is used as the floor for time_since_last_trade when we
	// have not yet observed any trade-count change.
	startedAt time.Time
}

// instanceLabels are attached to every per-bot metric so a Grafana panel
// can split A/B/C and Frankfurt vs Tokyo.
var instanceLabels = []string{"target", "service", "instance_id", "agent", "dex"}

func newMetricsCollector() *metricsCollector {
	r := prometheus.NewRegistry()

	gv := func(name, help string, labels []string) *prometheus.GaugeVec {
		v := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
		r.MustRegister(v)
		return v
	}
	cv := func(name, help string, labels []string) *prometheus.CounterVec {
		v := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
		r.MustRegister(v)
		return v
	}
	g := func(name, help string) prometheus.Gauge {
		v := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
		r.MustRegister(v)
		return v
	}

	mc := &metricsCollector{
		registry: r,

		trades:                gv("debot_trades_total", "Total trades reported by the bot (Gauge to tolerate bot-restart resets).", instanceLabels),
		wins:                  gv("debot_wins_total", "Total winning trades reported by the bot.", instanceLabels),
		winRate:               gv("debot_win_rate", "Rolling win rate from trade_stats.", instanceLabels),
		pnlTotal:              gv("debot_pnl_total_usd", "Total equity (status.json pnl_total).", instanceLabels),
		pnlToday:              gv("debot_pnl_today_usd", "PnL today (equity delta from session start).", instanceLabels),
		positionCount:         gv("debot_position_count", "Number of open positions.", instanceLabels),
		hasPosition:           gv("debot_has_position", "1 if any position open, 0 otherwise.", instanceLabels),
		timeSinceLastTradeSec: gv("debot_time_since_last_trade_seconds", "Seconds since trade_stats.trades last incremented (dashboard-derived).", instanceLabels),
		statusAgeSec:          gv("debot_status_age_seconds", "Seconds between now and the bot's status.json ts.", instanceLabels),

		dailyPnlUsd:       gv("debot_daily_pnl_usd", "Daily realised PnL in USD.", instanceLabels),
		dailyPnlBps:       gv("debot_daily_pnl_bps", "Daily realised PnL in bps of session-start equity.", instanceLabels),
		dailyMaxLossBps:   gv("debot_daily_max_loss_bps", "Effective daily max-loss threshold in bps (after leverage scaling).", instanceLabels),
		dailyRiskHalted:   gv("debot_daily_risk_halted", "1 if daily DD halt is active.", instanceLabels),
		sessionStartTS:    gv("debot_session_start_ts", "Unix ts (s) of session start (daily rollover).", instanceLabels),
		currentEquity:     gv("debot_current_equity_usd", "Current equity (session_risk.current_equity).", instanceLabels),
		peakEquity:        gv("debot_peak_equity_usd", "Rolling peak equity over the session lookback window.", instanceLabels),
		sessionDDBps:      gv("debot_session_dd_bps", "Session drawdown in bps from rolling peak.", instanceLabels),
		sessionMaxLossBps: gv("debot_session_max_loss_bps", "Effective session max-loss threshold in bps.", instanceLabels),
		sessionHalted:     gv("debot_session_halted", "1 if sticky session DD halt is active.", instanceLabels),
		sessionHaltTS:     gv("debot_session_halt_ts", "Unix ts (s) when session halt fired (0 if not halted).", instanceLabels),

		cbActive:            gv("debot_circuit_breaker_active", "1 if circuit-breaker cooldown is active.", instanceLabels),
		cbConsecutiveLosses: gv("debot_circuit_breaker_consecutive_losses", "Consecutive-loss counter feeding the circuit breaker.", instanceLabels),
		cbUntilTS:           gv("debot_circuit_breaker_until_ts", "Unix ts (s) when circuit-breaker cooldown expires (0 if not active).", instanceLabels),
		cbCooldownRemaining: gv("debot_circuit_breaker_cooldown_remaining_secs", "Seconds remaining in circuit-breaker cooldown (0 if not active).", instanceLabels),
		killSwitchActive:    gv("debot_kill_switch_active", "1 if /opt/debot/KILL_SWITCH file is present on the target instance.", instanceLabels),
		shutdownPending:     gv("debot_shutdown_pending", "1 if graceful shutdown is in progress.", instanceLabels),
		riskEventsTotal:     cv("debot_risk_events_total", "Risk-history events seen by the dashboard, by gate kind and transition type.", append(append([]string{}, instanceLabels...), "kind", "event_type")),

		serviceActive:    gv("debot_service_active", "1 if systemctl reports the service as active.", []string{"target", "service", "instance_id"}),
		serviceStartedTS: gv("debot_service_started_ts", "Unix ts (s) when the systemd service last entered the active state.", []string{"target", "service", "instance_id"}),
		processUptimeSec: gv("debot_process_uptime_seconds", "Seconds since the systemd service entered the active state. Helps detect the running-binary-lags-source case.", []string{"target", "service", "instance_id"}),
		wsReset24h:       gv("debot_ws_reset_24h", "WebSocket reset events observed in the service's journald log over the last 24 hours.", []string{"target", "service", "instance_id"}),
		errorCount30m:    gv("debot_error_count_30m", "ERROR-level log events in the bot's last 30 minutes (or 5m for older bots).", instanceLabels),
		warnCount30m:     gv("debot_warn_count_30m", "WARN-level log events in the bot's last 30 minutes (or 5m for older bots).", instanceLabels),
		errorCountTotal:  gv("debot_error_count_total_snapshot", "Cumulative ERROR-level log events since the bot started. Snapshot-on-poll: resets when the bot restarts.", instanceLabels),
		warnCountTotal:   gv("debot_warn_count_total_snapshot", "Cumulative WARN-level log events since the bot started.", instanceLabels),
		lastErrorTS:      gv("debot_last_error_ts", "Unix ts (s) of the most recent ERROR-level log event (0 if never).", instanceLabels),
		lastWarnTS:       gv("debot_last_warn_ts", "Unix ts (s) of the most recent WARN-level log event (0 if never).", instanceLabels),

		dryRun:       gv("debot_dry_run", "1 if the bot is running in dry-run mode (no real orders sent).", instanceLabels),
		backtestMode: gv("debot_backtest_mode", "1 if the bot is running in backtest mode.", instanceLabels),

		lastTradeTS:             gv("debot_last_trade_ts", "Unix ts (s) when trade_stats.trades last incremented (dashboard-derived; 0 until first observed change).", instanceLabels),
		peakEquityLastChangedTS: gv("debot_peak_equity_last_changed_ts", "Unix ts (s) when session_risk.peak_equity last increased. While dd_bps > 0 this is effectively 'when the current drawdown began'.", instanceLabels),

		pollErrorsTotal: cv("debot_dashboard_poll_errors_total", "SSM poll failures observed by the dashboard.", []string{"target", "service"}),
		targetsTotal:    g("debot_dashboard_targets_total", "Number of bot targets the dashboard is polling."),
		lastPollTS:      g("debot_dashboard_last_poll_ts", "Unix ts (s) of the most recent successful dashboard poll cycle."),

		lastTradeCount:  map[string]int64{},
		tradeChangedAt:  map[string]time.Time{},
		lastRiskEventTS: map[string]int64{},
		lastPeakEquity:  map[string]float64{},
		peakChangedAt:   map[string]time.Time{},
		startedAt:       time.Now(),
	}
	return mc
}

// Update is invoked from pollLoop after each cache.Set. It walks every
// target in the snapshot and (re)sets the per-target gauges. Targets
// whose Status is nil are skipped for bot-side metrics; only the
// poll-error counter is incremented.
func (mc *metricsCollector) Update(snapshot DashboardSnapshot) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.targetsTotal.Set(float64(len(snapshot.Targets)))
	mc.lastPollTS.Set(float64(snapshot.UpdatedAt.Unix()))

	for _, t := range snapshot.Targets {
		// Poll error: count once per failed cycle for this target.
		if t.Error != "" {
			mc.pollErrorsTotal.WithLabelValues(t.Name, t.Service).Inc()
		}

		// Service-level metrics live independently of status.json
		// validity — they're sourced from systemctl, so they're
		// useful even when status.json is absent / empty.
		svcLabels := prometheus.Labels{
			"target":      t.Name,
			"service":     t.Service,
			"instance_id": t.InstanceID,
		}
		mc.serviceActive.With(svcLabels).Set(boolToFloat(t.ServiceStatus == "active"))
		if t.ServiceStartedAt != nil {
			startedTS := t.ServiceStartedAt.Unix()
			mc.serviceStartedTS.With(svcLabels).Set(float64(startedTS))
			mc.processUptimeSec.With(svcLabels).Set(float64(time.Now().Unix() - startedTS))
		}
		if t.WsReset24h != nil {
			mc.wsReset24h.With(svcLabels).Set(float64(*t.WsReset24h))
		}

		if t.Status == nil {
			continue
		}
		s := t.Status

		labels := prometheus.Labels{
			"target":      t.Name,
			"service":     t.Service,
			"instance_id": t.InstanceID,
			"agent":       derefString(s.Agent),
			"dex":         s.Dex,
		}

		// Group 1 — trade activity.
		var trades, wins int64
		var winRate float64
		if s.TradeStats != nil {
			trades = s.TradeStats.Trades
			wins = s.TradeStats.Wins
			winRate = s.TradeStats.WinRate
		}
		mc.trades.With(labels).Set(float64(trades))
		mc.wins.With(labels).Set(float64(wins))
		mc.winRate.With(labels).Set(winRate)
		mc.pnlTotal.With(labels).Set(s.PnlTotal)
		mc.pnlToday.With(labels).Set(s.PnlToday)
		mc.positionCount.With(labels).Set(float64(s.PositionCount))
		mc.hasPosition.With(labels).Set(boolToFloat(s.HasPosition))

		// Time since last trade — derived from trade-count delta.
		// `lastTradeTS` only emits a non-zero value once we've observed
		// an actual change (otherwise the dashboard's startup time
		// would masquerade as a trade timestamp on Grafana panels).
		now := time.Now()
		if prev, ok := mc.lastTradeCount[t.Name]; !ok {
			// First sighting: anchor at dashboard start so the
			// "time since" gauge is monotonic from process boot.
			mc.lastTradeCount[t.Name] = trades
			mc.tradeChangedAt[t.Name] = mc.startedAt
		} else if trades != prev {
			mc.lastTradeCount[t.Name] = trades
			mc.tradeChangedAt[t.Name] = now
		}
		mc.timeSinceLastTradeSec.With(labels).Set(now.Sub(mc.tradeChangedAt[t.Name]).Seconds())
		// Only emit a real timestamp once we've seen the count actually
		// change at least once — before that, tradeChangedAt is the
		// dashboard start time, not a trade event.
		if mc.tradeChangedAt[t.Name].After(mc.startedAt) {
			mc.lastTradeTS.With(labels).Set(float64(mc.tradeChangedAt[t.Name].Unix()))
		} else {
			mc.lastTradeTS.With(labels).Set(0)
		}

		// status.ts is unix seconds.
		if s.TS > 0 {
			mc.statusAgeSec.With(labels).Set(float64(now.Unix() - s.TS))
		}

		// Group 2 — DD timing.
		if s.DailyRisk != nil {
			d := s.DailyRisk
			mc.dailyPnlUsd.With(labels).Set(d.DailyPnl)
			mc.dailyPnlBps.With(labels).Set(d.DailyPnlBps)
			mc.dailyMaxLossBps.With(labels).Set(d.EffectiveMaxDailyLossBps)
			mc.dailyRiskHalted.With(labels).Set(boolToFloat(d.RiskHalted))
			mc.sessionStartTS.With(labels).Set(float64(d.SessionStartTS))
		}
		if s.SessionRisk != nil {
			r := s.SessionRisk
			mc.currentEquity.With(labels).Set(r.CurrentEquity)
			mc.peakEquity.With(labels).Set(r.PeakEquity)
			mc.sessionDDBps.With(labels).Set(r.DDBps)
			mc.sessionMaxLossBps.With(labels).Set(r.EffectiveMaxSessionLossBps)
			mc.sessionHalted.With(labels).Set(boolToFloat(r.SessionHalted))
			haltTS := int64(0)
			if r.HaltTS != nil {
				haltTS = *r.HaltTS
			}
			mc.sessionHaltTS.With(labels).Set(float64(haltTS))

			// Track when peak_equity last increased — this is
			// effectively "when did the current drawdown begin"
			// while dd_bps > 0. First sighting anchors at the
			// poll moment (we have no earlier signal); subsequent
			// peak increases update the timestamp.
			prevPeak, seen := mc.lastPeakEquity[t.Name]
			if !seen {
				mc.lastPeakEquity[t.Name] = r.PeakEquity
				mc.peakChangedAt[t.Name] = now
			} else if r.PeakEquity > prevPeak {
				mc.lastPeakEquity[t.Name] = r.PeakEquity
				mc.peakChangedAt[t.Name] = now
			}
			mc.peakEquityLastChangedTS.With(labels).Set(float64(mc.peakChangedAt[t.Name].Unix()))
		}

		// Group 3 — risk halt state.
		if s.CircuitBreaker != nil {
			cb := s.CircuitBreaker
			mc.cbActive.With(labels).Set(boolToFloat(cb.Active))
			mc.cbConsecutiveLosses.With(labels).Set(float64(cb.ConsecutiveLosses))
			until := int64(0)
			if cb.UntilTS != nil {
				until = *cb.UntilTS
			}
			mc.cbUntilTS.With(labels).Set(float64(until))
			cd := int64(0)
			if cb.CooldownRemainingSecs != nil {
				cd = *cb.CooldownRemainingSecs
			}
			mc.cbCooldownRemaining.With(labels).Set(float64(cd))
		}
		ks := false
		if t.KillSwitchActive != nil {
			ks = *t.KillSwitchActive
		}
		mc.killSwitchActive.With(labels).Set(boolToFloat(ks))

		shutdownPending := false
		if s.Shutdown != nil {
			shutdownPending = s.Shutdown.Pending
		}
		mc.shutdownPending.With(labels).Set(boolToFloat(shutdownPending))

		// Group 6 — error/warn log counters & timestamps from the
		// bot's ErrorSummary block (issue #168).
		if s.ErrorSummary != nil {
			es := s.ErrorSummary
			mc.errorCount30m.With(labels).Set(float64(es.ErrorCountWindow))
			mc.warnCount30m.With(labels).Set(float64(es.WarnCountWindow))
			mc.errorCountTotal.With(labels).Set(float64(es.ErrorCountTotal))
			mc.warnCountTotal.With(labels).Set(float64(es.WarnCountTotal))
			lastErr := int64(0)
			if es.LastErrorTs != nil {
				lastErr = *es.LastErrorTs
			}
			mc.lastErrorTS.With(labels).Set(float64(lastErr))
			lastWarn := int64(0)
			if es.LastWarnTs != nil {
				lastWarn = *es.LastWarnTs
			}
			mc.lastWarnTS.With(labels).Set(float64(lastWarn))
		}

		// Group 7 — config visibility flags.
		mc.dryRun.With(labels).Set(boolToFloat(s.DryRun))
		mc.backtestMode.With(labels).Set(boolToFloat(s.BacktestMode))

		// Risk events counter — increment for every event in the
		// ring buffer that's strictly newer than the last seen TS.
		// status.json ships up to 200 events; the dashboard poll
		// interval (default 20s) is fast enough to never miss an
		// event whose TS we haven't yet observed unless the bot
		// emits >200 events in a single cycle (extremely unlikely).
		lastSeen := mc.lastRiskEventTS[t.Name]
		var newMax int64 = lastSeen
		for _, ev := range s.RiskHistory {
			if ev.TS <= lastSeen {
				continue
			}
			evLabels := prometheus.Labels{
				"target":      t.Name,
				"service":     t.Service,
				"instance_id": t.InstanceID,
				"agent":       derefString(s.Agent),
				"dex":         s.Dex,
				"kind":        ev.Kind,
				"event_type":  ev.EventType,
			}
			mc.riskEventsTotal.With(evLabels).Inc()
			if ev.TS > newMax {
				newMax = ev.TS
			}
		}
		mc.lastRiskEventTS[t.Name] = newMax
	}
}

// Handler returns the http.Handler that serves the registered metrics
// in Prometheus exposition format.
func (mc *metricsCollector) Handler() http.Handler {
	return promhttp.HandlerFor(mc.registry, promhttp.HandlerOpts{})
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
