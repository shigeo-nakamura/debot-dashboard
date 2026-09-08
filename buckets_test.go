package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseBucketDoc reads the service → bucket table out of docs/buckets.md.
// Kept deliberately literal (markdown table, third column) so a
// reformatted or reordered table still parses, but a changed value does
// not slip past the registry comparison below.
func parseBucketDoc(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("docs/buckets.md")
	if err != nil {
		t.Fatalf("read docs/buckets.md: %v", err)
	}
	buckets := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		service := strings.Trim(strings.TrimSpace(cells[1]), "`")
		bucket := strings.Trim(strings.TrimSpace(cells[2]), "`")
		if !validBucket(bucket) {
			continue // header and separator rows
		}
		if _, dup := buckets[service]; dup {
			t.Fatalf("docs/buckets.md lists service %q twice", service)
		}
		buckets[service] = bucket
	}
	if len(buckets) == 0 {
		t.Fatal("docs/buckets.md has no parseable service rows")
	}
	return buckets
}

func TestBucketRegistryMatchesDoc(t *testing.T) {
	doc := parseBucketDoc(t)
	if !reflect.DeepEqual(doc, taxonomyBuckets) {
		t.Fatalf("taxonomyBuckets and docs/buckets.md disagree:\ndoc      = %v\nregistry = %v", doc, taxonomyBuckets)
	}
}

// The example config is the only config checked into the repo; the
// deployed one lives on the host. Validating it here catches a `bucket:`
// that contradicts the taxonomy before it is copy-pasted into
// /opt/debot-dashboard/config.yaml.
func TestConfigExampleBucketsMatchDoc(t *testing.T) {
	data, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config.example.yaml: %v", err)
	}
	if len(cfg.Targets) == 0 {
		t.Fatal("config.example.yaml has no active targets")
	}
	doc := parseBucketDoc(t)
	for i := range cfg.Targets {
		target := &cfg.Targets[i]
		if err := resolveBucket(target); err != nil {
			t.Fatalf("targets[%d] (%s): %v", i, target.Service, err)
		}
		want, known := doc[target.Service]
		if !known {
			t.Fatalf("targets[%d]: service %q is not in docs/buckets.md", i, target.Service)
		}
		if target.Bucket != want {
			t.Fatalf("targets[%d]: bucket %q, docs/buckets.md says %q", i, target.Bucket, want)
		}
	}
}

func TestResolveBucket(t *testing.T) {
	cases := []struct {
		name    string
		target  TargetConfig
		want    string
		wantErr string
	}{
		{
			name:   "empty inherits the taxonomy",
			target: TargetConfig{Service: "debot-bull-holder"},
			want:   BucketBeta,
		},
		{
			name:   "unknown service is unclassified, not an error",
			target: TargetConfig{Service: "some-new-bot"},
			want:   BucketUnclassified,
		},
		{
			name:   "explicit value agreeing with the taxonomy is kept",
			target: TargetConfig{Service: "arcus-spot-live-tick", Bucket: " subsidy "},
			want:   BucketSubsidy,
		},
		{
			name:   "explicit value for an unregistered service is kept",
			target: TargetConfig{Service: "some-new-bot", Bucket: BucketAlphaCandidate},
			want:   BucketAlphaCandidate,
		},
		{
			name:    "typo is rejected instead of silently unclassified",
			target:  TargetConfig{Service: "debot-bull-holder", Bucket: "Beta"},
			wantErr: "unknown bucket",
		},
		{
			name:    "config cannot re-bucket a bot behind the taxonomy's back",
			target:  TargetConfig{Service: "book-runtime-xsmom-695", Bucket: BucketBeta},
			wantErr: "contradicts the return-source taxonomy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			err := resolveBucket(&target)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target.Bucket != tc.want {
				t.Fatalf("bucket = %q, want %q", target.Bucket, tc.want)
			}
		})
	}
}

// normalizeConfig is the startup path; resolveBucket being correct is
// worth nothing if it is never called, and a target that reaches the API
// without a bucket would be grouped as unclassified on the dashboard
// while the taxonomy already classifies it.
func TestNormalizeConfigResolvesAndValidatesBuckets(t *testing.T) {
	cfg := Config{
		Region: "eu-central-1",
		Targets: []TargetConfig{
			{Service: "arcus-spot-live-tick", S3Bucket: "b", S3Key: "k"},
			{Service: "some-new-bot", S3Bucket: "b", S3Key: "k"},
			{Service: "engine-b-live", Bucket: BucketAlphaCandidate, S3Bucket: "b", S3Key: "k"},
		},
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	want := []string{BucketSubsidy, BucketUnclassified, BucketAlphaCandidate}
	for i, bucket := range want {
		if cfg.Targets[i].Bucket != bucket {
			t.Fatalf("targets[%d].Bucket = %q, want %q", i, cfg.Targets[i].Bucket, bucket)
		}
	}

	bad := Config{
		Region:  "eu-central-1",
		Targets: []TargetConfig{{Service: "debot-bull-holder", Bucket: BucketSubsidy, S3Bucket: "b", S3Key: "k"}},
	}
	if err := normalizeConfig(&bad); err == nil {
		t.Fatal("normalizeConfig accepted a bucket that contradicts the taxonomy")
	}
}
