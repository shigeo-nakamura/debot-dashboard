package main

import (
	"fmt"
	"strings"
)

// Return-source buckets (bot-strategy#954, docs/return-source-taxonomy.md
// §3). A target's bucket decides which benchmark its card is judged
// against — 0 after costs for an α candidate, spot buy & hold for a β
// bot, cost per subsidy unit for a subsidy bot — so summing equity or
// PnL across buckets is meaningless and the frontend aggregates per
// bucket instead (bot-strategy#959).
const (
	BucketAlphaCandidate = "alpha_candidate"
	BucketBeta           = "beta"
	BucketSubsidy        = "subsidy"
	// BucketUnclassified is assigned to a target whose service is not in
	// the taxonomy and that does not declare `bucket:` in config. Such a
	// target still renders a card (with its health) but is excluded from
	// every per-bucket aggregate.
	BucketUnclassified = "unclassified"
)

// taxonomyBuckets mirrors the table in docs/buckets.md, which is itself
// a mirror of bot-strategy docs/return-source-taxonomy.md §3 — the
// source of truth for the classification. TestBucketRegistryMatchesDoc
// fails when the two drift apart, so a bot moving between buckets is a
// documentation change first and a code change second.
var taxonomyBuckets = map[string]string{
	"debot-bull-holder":            BucketBeta,
	"hype-accumulator":             BucketBeta,
	"debot-pair-robinhood-lighter": BucketSubsidy,
	"arcus-spot-live-tick":         BucketSubsidy,
	"engine-b-live":                BucketAlphaCandidate,
	"book-runtime-xsmom-695":       BucketAlphaCandidate,
	"xsmom-695-shadow":             BucketAlphaCandidate,
}

func validBucket(bucket string) bool {
	switch bucket {
	case BucketAlphaCandidate, BucketBeta, BucketSubsidy:
		return true
	}
	return false
}

// resolveBucket fills in target.Bucket. An empty `bucket:` inherits the
// taxonomy's classification for the service (so the deployed config does
// not have to repeat what the ledger already states), and falls back to
// "unclassified" for a service the taxonomy does not know yet. An
// explicit value must be one of the three buckets and must not
// contradict the taxonomy — a bot cannot be re-bucketed by editing the
// dashboard config alone.
func resolveBucket(target *TargetConfig) error {
	declared := strings.TrimSpace(target.Bucket)
	registered, known := taxonomyBuckets[target.Service]
	if declared == "" {
		if known {
			target.Bucket = registered
		} else {
			target.Bucket = BucketUnclassified
		}
		return nil
	}
	if !validBucket(declared) {
		return fmt.Errorf(
			"unknown bucket %q (want %s, %s or %s)",
			declared, BucketAlphaCandidate, BucketBeta, BucketSubsidy,
		)
	}
	if known && registered != declared {
		return fmt.Errorf(
			"bucket %q contradicts the return-source taxonomy (%s is %s); "+
				"change docs/buckets.md and bot-strategy docs/return-source-taxonomy.md §3 first",
			declared, target.Service, registered,
		)
	}
	target.Bucket = declared
	return nil
}
