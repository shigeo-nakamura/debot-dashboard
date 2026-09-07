package main

import (
	"testing"
	"time"
)

func TestHolderOperationalMetricsWithoutTradingValues(t *testing.T) {
	mc := newMetricsCollector()
	kill := true
	target := TargetStatus{Name: "holder", KillSwitchActive: &kill, Status: &StatusData{
		TS: time.Now().Unix() - 30, DryRun: true, BullHolder: &BullHolderStatus{}, PnlTotal: 999,
	}}
	check := func(want map[string]bool) {
		t.Helper()
		families, err := mc.registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, family := range families {
			seen[family.GetName()] = true
			if family.GetName() == "debot_kill_switch_active" || family.GetName() == "debot_dry_run" {
				if family.Metric[0].Gauge.GetValue() != 1 {
					t.Fatalf("%s should be active", family.GetName())
				}
			}
		}
		for name, present := range want {
			if seen[name] != present {
				t.Errorf("%s presence = %v, want %v", name, seen[name], present)
			}
		}
	}
	mc.Update(DashboardSnapshot{Targets: []TargetStatus{target}})
	check(map[string]bool{"debot_kill_switch_active": true, "debot_status_age_seconds": true, "debot_dry_run": true, "debot_pnl_total_usd": false, "debot_position_count": false})
	// A missing producer must not retain stale mode/freshness gauges, while
	// the independently sampled kill switch remains observable.
	target.Status.TS = 0
	mc.Update(DashboardSnapshot{Targets: []TargetStatus{target}})
	check(map[string]bool{"debot_kill_switch_active": true, "debot_status_age_seconds": false, "debot_dry_run": false})
	target.KillSwitchActive = nil
	mc.Update(DashboardSnapshot{Targets: []TargetStatus{target}})
	check(map[string]bool{"debot_kill_switch_active": false})
}

// A book target feeds different gauges than a pairtrade one: equity comes
// from book.equity_usd (its top-level pnl_total is PnL against the equity
// reference), and its halts live on the nested block rather than the
// session_risk / daily_risk shape the generic path reads.
func TestBookMetricsUseNestedEquityAndHalts(t *testing.T) {
	gauge := func(mc *metricsCollector, name string) float64 {
		t.Helper()
		families, err := mc.registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() == name {
				return family.Metric[0].Gauge.GetValue()
			}
		}
		t.Fatalf("%s not exported", name)
		return 0
	}
	book := func(mutate func(*BookStatus)) *StatusData {
		b := &BookStatus{
			InstanceID:  "xsmom-695",
			EquityUsd:   1009.93,
			EquityReady: true,
		}
		if mutate != nil {
			mutate(b)
		}
		// pnl_total is deliberately the PnL, not the capital.
		return &StatusData{TS: time.Now().Unix(), PnlTotal: 9.93, Book: b}
	}

	mc := newMetricsCollector()
	mc.Update(DashboardSnapshot{Targets: []TargetStatus{{Name: "xsmom", Status: book(nil)}}})
	if got := gauge(mc, "debot_pnl_total_usd"); got != 1009.93 {
		t.Fatalf("debot_pnl_total_usd = %v, want the book's equity 1009.93", got)
	}
	if got := gauge(mc, "debot_session_halted"); got != 0 {
		t.Fatalf("session halted = %v on a healthy book", got)
	}
	if got := gauge(mc, "debot_daily_risk_halted"); got != 0 {
		t.Fatalf("daily halted = %v on a healthy book", got)
	}

	for _, tc := range []struct {
		name           string
		mutate         func(*BookStatus)
		session, daily float64
	}{
		{"session halt", func(b *BookStatus) { b.SessionHalted = true }, 1, 0},
		{"daily halt", func(b *BookStatus) { b.DailyHalted = true }, 0, 1},
		// A live equity outage blocks every opening intent, which is what
		// an alert on the session gauge is really asking about.
		{"equity unavailable", func(b *BookStatus) { b.EquityReady = false }, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mc := newMetricsCollector()
			mc.Update(DashboardSnapshot{Targets: []TargetStatus{{Name: "xsmom", Status: book(tc.mutate)}}})
			if got := gauge(mc, "debot_session_halted"); got != tc.session {
				t.Fatalf("session halted = %v, want %v", got, tc.session)
			}
			if got := gauge(mc, "debot_daily_risk_halted"); got != tc.daily {
				t.Fatalf("daily halted = %v, want %v", got, tc.daily)
			}
		})
	}

	// A pairtrade-shaped target keeps reading pnl_total.
	mc = newMetricsCollector()
	mc.Update(DashboardSnapshot{Targets: []TargetStatus{
		{Name: "pair", Status: &StatusData{TS: time.Now().Unix(), PnlTotal: 42}},
	}})
	if got := gauge(mc, "debot_pnl_total_usd"); got != 42 {
		t.Fatalf("non-book target equity = %v, want 42", got)
	}
}
