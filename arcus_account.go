package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Arcus volume-bot wallet value for the top-panel Total equity (owner
// request 2026-11-07, "include the Arcus wallets too"). The runtime's
// status.json carries no account value, so the dashboard reads it itself
// from the venue's public account endpoint (GET /v1/account?address=&
// accountIndex=, no key, no signature — the same read dex-connector's Arcus
// connector makes). The address lives only in config.yaml; the API serves
// a short one-way account_key so several targets on one wallet are counted
// once, never the address.

var (
	// Overridden by tests.
	arcusAPIBaseURL = "https://api.arcus.xyz"

	arcusAccountAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

const (
	arcusAccountTTL      = 60 * time.Second
	arcusAccountErrorTTL = 30 * time.Second
)

type arcusAccountReading struct {
	fetchedAt time.Time
	equity    *float64
	atMs      int64 // when the last good reading was taken
	err       string
}

type arcusAccountCache struct {
	mu       sync.Mutex
	readings map[string]*arcusAccountReading
}

var arcusAccounts = &arcusAccountCache{readings: map[string]*arcusAccountReading{}}

func arcusAccountCacheForTest() func() {
	prev := arcusAccounts
	arcusAccounts = &arcusAccountCache{readings: map[string]*arcusAccountReading{}}
	return func() { arcusAccounts = prev }
}

func (c ArcusVolConfig) accountIndex() int {
	if c.AccountIndex != nil {
		return *c.AccountIndex
	}
	return 0
}

// arcusAccountKey is the one-way id the API serves for the dedupe: equal for
// targets on the same wallet + subaccount, and not the address.
func arcusAccountKey(address string, index int) string {
	sum := sha256.Sum256([]byte("arcus|" + strings.ToLower(address) + "|" + strconv.Itoa(index)))
	return hex.EncodeToString(sum[:])[:12]
}

func (c ArcusVolConfig) validateAccount() error {
	if c.AccountAddress != "" && !arcusAccountAddressRe.MatchString(c.AccountAddress) {
		return errors.New("arcus_vol.account_address must be a 0x-prefixed 40-hex-digit address")
	}
	if c.AccountIndex != nil && (*c.AccountIndex < 0 || *c.AccountIndex > 255) {
		return errors.New("arcus_vol.account_index must be 0..255")
	}
	if c.AccountIndex != nil && c.AccountAddress == "" {
		return errors.New("arcus_vol.account_index needs arcus_vol.account_address")
	}
	if c.EquityCountedBy != "" && c.AccountAddress != "" {
		return errors.New("arcus_vol: set account_address or equity_counted_by, not both")
	}
	return nil
}

// get returns the cached reading for one wallet, refreshing it at most once
// per TTL; the lock is held across the fetch so concurrent targets on the
// same wallet make one request.
func (c *arcusAccountCache) get(ctx context.Context, client *http.Client, address string, index int, now time.Time) arcusAccountReading {
	key := strings.ToLower(address) + "|" + strconv.Itoa(index)
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.readings[key]
	if r == nil {
		r = &arcusAccountReading{}
		c.readings[key] = r
	}
	ttl := arcusAccountTTL
	if r.err != "" {
		ttl = arcusAccountErrorTTL
	}
	if !r.fetchedAt.IsZero() && now.Sub(r.fetchedAt) < ttl {
		return *r
	}
	r.fetchedAt = now
	equity, err := fetchArcusAccountEquity(ctx, client, address, index)
	if err != nil {
		// Keep the last good value and its time: the summary shows it as
		// stale rather than dropping the wallet.
		r.err = err.Error()
		return *r
	}
	r.err = ""
	r.equity = &equity
	r.atMs = now.UnixMilli()
	return *r
}

func fetchArcusAccountEquity(ctx context.Context, client *http.Client, address string, index int) (float64, error) {
	url := fmt.Sprintf("%s/v1/account?address=%s&accountIndex=%d", strings.TrimRight(arcusAPIBaseURL, "/"), address, index)
	var out struct {
		Equity *string `json:"equity"`
		Error  string  `json:"error"`
	}
	if err := holderJSON(ctx, client, url, nil, &out); err != nil {
		return 0, err
	}
	if out.Equity == nil {
		// e.g. {"error":"this account has no activity yet"}: never a $0.
		return 0, errors.New("Arcus account read returned no equity")
	}
	v, err := number(*out.Equity)
	if err != nil {
		return 0, errors.New("invalid Arcus account equity")
	}
	return v, nil
}

// applyArcusAccountEquity fills the wallet fields of an arcus_vol target's
// status. Runs for every fetch (the cache bounds the venue calls).
func applyArcusAccountEquity(ctx context.Context, client *http.Client, target TargetConfig, status *StatusData, now time.Time) {
	cfg := target.ArcusVol
	if cfg == nil || status == nil || status.ArcusVol == nil {
		return
	}
	a := status.ArcusVol
	a.EquityCountedBy = strings.TrimSpace(cfg.EquityCountedBy)
	if cfg.AccountAddress == "" {
		return
	}
	a.AccountConfigured = true
	a.AccountKey = arcusAccountKey(cfg.AccountAddress, cfg.accountIndex())
	r := arcusAccounts.get(ctx, client, cfg.AccountAddress, cfg.accountIndex(), now)
	a.AccountEquityUSD = r.equity
	a.AccountEquityAtMs = r.atMs
	a.AccountEquityError = r.err
}
