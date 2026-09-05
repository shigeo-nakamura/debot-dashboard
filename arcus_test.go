package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"debot-dashboard/internal/arcusstatus"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestArcusFetchUsesBotAndExporterClocksWithoutTradingDefaults(t *testing.T) {
	now := time.Now().UTC()
	tick := now.Add(-15 * time.Minute)
	a := &arcusstatus.Status{Healthy: true, ExportedAt: now.Format(time.RFC3339), LastTickAt: tick.Format(time.RFC3339), LastObservationAt: tick.Format(time.RFC3339)}
	p := arcusstatus.Payload{SchemaVersion: 1, TS: tick.Unix(), UpdatedAt: a.LastTickAt, Arcus: a}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	}))
	defer server.Close()
	client := s3.New(s3.Options{Region: "eu-central-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: aws.AnonymousCredentials{}})
	pool := &S3ClientPool{clients: map[string]*s3.Client{"eu-central-1": client}}
	target := TargetConfig{Name: "Arcus", Region: "eu-central-1", S3Bucket: "test", S3Key: "status.json", StaleAfterSecs: 1920}
	got := fetchTargetS3(context.Background(), target, pool, true, 0)
	if got.Error != "" || got.ServiceStatus != "active" || got.StaleAfterSecs != 1920 {
		t.Fatalf("unexpected target: %+v", got)
	}
	if got.KillSwitchActive != nil || got.WsReset24h != nil || requests != 1 {
		t.Fatal("Arcus must not invent pairtrade metrics or fetch equity history")
	}
	p.Arcus.ExportedAt = now.Add(-4 * time.Minute).Format(time.RFC3339)
	if got := fetchTargetS3(context.Background(), target, pool, false, 0); got.ServiceStatus != "stale" {
		t.Fatalf("stopped exporter hidden: %+v", got)
	}
}

func TestArcusSchemaAndFreshnessConfig(t *testing.T) {
	for _, body := range []string{`{"schema_version":2,"arcus":{}}`, `{"schema_version":1,"arcus":{},"accumulator":{}}`, `{"schema_version":1,"arcus":{},"han_bridge":{}}`} {
		if _, err := decodeStatusPayload([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if s, err := decodeStatusPayload([]byte(`{"schema_version":1,"arcus":{"daily_loss_usd":null,"cumulative_loss_usd":0}}`)); err != nil || s.Arcus.DailyLossUSD != nil || s.Arcus.CumulativeLossUSD == nil {
		t.Fatal("unknown risk must remain distinct from zero")
	}
	cfg := Config{Region: "eu-central-1", Targets: []TargetConfig{{Service: "legacy", S3Bucket: "test", S3Key: "legacy.json"}, {Service: "arcus", S3Bucket: "test", S3Key: "arcus.json", StaleAfterSecs: 1920}}}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Targets[0].StaleAfterSecs != 180 || cfg.Targets[1].StaleAfterSecs != 1920 {
		t.Fatal("freshness defaults changed")
	}
	cfg.Targets[1].StaleAfterSecs = -1
	if normalizeConfig(&cfg) == nil {
		t.Fatal("negative threshold accepted")
	}
}

func TestArcusMetricsExcludeInventedTradingValues(t *testing.T) {
	mc := newMetricsCollector()
	mc.Update(DashboardSnapshot{Targets: []TargetStatus{{Name: "Arcus", ServiceStatus: "degraded", Status: &StatusData{TS: time.Now().Unix(), Arcus: &arcusstatus.Status{}, PnlTotal: 3000, PnlToday: 50, PositionCount: 2}}}})
	families, err := mc.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seenAge := false
	for _, f := range families {
		switch f.GetName() {
		case "debot_pnl_total_usd", "debot_pnl_today_usd", "debot_position_count", "debot_kill_switch_active":
			t.Fatalf("invented Arcus metric %s", f.GetName())
		case "debot_status_age_seconds":
			seenAge = true
		case "debot_service_active":
			if f.Metric[0].Gauge.GetValue() != 0 {
				t.Fatal("degraded Arcus counted as active")
			}
		}
	}
	if !seenAge {
		t.Fatal("missing operational freshness")
	}
}
