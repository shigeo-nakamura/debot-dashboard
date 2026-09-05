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
