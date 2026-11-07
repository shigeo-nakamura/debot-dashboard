package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Total equity dedupe label travels config -> API unchanged (trimmed),
// and is omitted for a target that does not set it.
func TestEquityAccountLabelReachesTheAPI(t *testing.T) {
	var target TargetConfig
	if err := yaml.Unmarshal([]byte("service: debot-arcus-vol\nequity_account: \" arcus-a \"\n"), &target); err != nil {
		t.Fatal(err)
	}
	if target.EquityAccount != " arcus-a " {
		t.Fatalf("yaml equity_account not parsed: %q", target.EquityAccount)
	}
	fixture, err := os.ReadFile("tests/fixtures/arcus-vol-status.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Region: "ap-northeast-1", Targets: []TargetConfig{
		{Name: "a", Service: "debot-arcus-vol", EquityAccount: target.EquityAccount, ArcusVol: &ArcusVolConfig{StatusPath: path}},
		{Name: "b", Service: "debot-arcus-vol-gld", ArcusVol: &ArcusVolConfig{StatusPath: path}},
	}}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	snap := fetchAll(context.Background(), cfg, nil, false, 0)
	if got := snap.Targets[0].EquityAccount; got != "arcus-a" {
		t.Fatalf("equity_account = %q, want trimmed arcus-a", got)
	}
	raw, err := json.Marshal(snap.Targets[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "equity_account") {
		t.Fatalf("unset label must be omitted: %s", raw)
	}
}
