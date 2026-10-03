// Package events is the write path: it turns exposure and conversion
// beacons into idempotent rows. It never fails a request. Every problem,
// from a bad origin to a database outage, ends in a dropped event and a
// log line, because the caller is a customer's page and must never see an
// error status from us.
package events

import (
	"context"
	"log/slog"
	"math"
	"time"

	"variantsvc/internal/assign"
	"variantsvc/internal/configcache"
	"variantsvc/internal/experiment"
	"variantsvc/internal/site"
	"variantsvc/internal/store"
)

// WriteBudget bounds one event insert. Longer than this and the event is
// dropped rather than queued behind a slow database.
const WriteBudget = 2 * time.Second

// Reasons an event is dropped, for logs and tests.
const (
	DropNoStore        = "no_store"
	DropBadSite        = "bad_site"
	DropUnknownSite    = "unknown_site"
	DropSuspended      = "site_suspended"
	DropUnauthorized   = "unauthorized"
	DropRateLimited    = "rate_limited"
	DropBadVisitor     = "bad_visitor"
	DropUnknownExp     = "unknown_experiment"
	DropUnknownVariant = "unknown_variant"
	DropBadGoal        = "bad_goal"
	DropBadValue       = "bad_value"
	DropStoreError     = "store_error"

	// Accepted outcomes.
	ReasonAccepted  = "accepted"
	ReasonDuplicate = "duplicate"
)

const (
	kindExposure    = "exposure"
	kindConversion  = "conversion"
	maxGoalLen      = 64
	burstMultiplier = 2
	valueMaxAbs     = 1e12
)

// Exposure is a parsed exposure beacon.
type Exposure struct {
	Site       string `json:"site"`
	Visitor    string `json:"v"`
	Experiment string `json:"experiment"`
	Variant    string `json:"variant"`
}

// Conversion is a parsed conversion beacon. Variant is deliberately absent:
// results join conversions to exposures by (experiment, visitor).
type Conversion struct {
	Site       string   `json:"site"`
	Visitor    string   `json:"v"`
	Experiment string   `json:"experiment"`
	Goal       string   `json:"goal"`
	Value      *float64 `json:"value"`
}

// Credentials carried by the request, used to authorise the write.
type Credentials struct {
	// Origin is the request Origin header (browser callers).
	Origin string
	// APIKey is the Bearer token, if any (server callers).
	APIKey string
}

// Recorder authorises and writes events.
type Recorder struct {
	Cache   *configcache.Cache
	Store   *store.Store // nil when running without a database
	Limiter *site.Limiter
	Log     *slog.Logger
}

// Outcome describes what happened to an event: Accepted is true when a row
// was written or already existed; Reason explains drops.
type Outcome struct {
	Accepted bool
	Inserted bool
	Reason   string
}

// authorise resolves the site and checks credentials, suspension and rate
// limit. Shared by both event kinds.
func (r *Recorder) authorise(siteKey string, creds Credentials) (*configcache.Entry, Outcome) {
	if r.Store == nil {
		return nil, Outcome{Reason: DropNoStore}
	}
	if !experiment.ValidKey(siteKey) {
		return nil, Outcome{Reason: DropBadSite}
	}
	entry := r.Cache.Get(siteKey)
	if entry == nil {
		return nil, Outcome{Reason: DropUnknownSite}
	}
	if entry.Site.Status == experiment.SiteSuspended {
		return entry, Outcome{Reason: DropSuspended}
	}
	switch {
	case creds.APIKey != "":
		// A server-side caller: the key must belong to this very site. A
		// valid key for another site is as unauthorised as a wrong one.
		if entry.Site.APIKeyHash == "" || site.HashKey(creds.APIKey) != entry.Site.APIKeyHash {
			return entry, Outcome{Reason: DropUnauthorized}
		}
	case site.OriginAllowed(creds.Origin, entry.Site.AllowedOrigins):
	default:
		return entry, Outcome{Reason: DropUnauthorized}
	}
	if r.Limiter != nil && !r.Limiter.Allow("events:"+siteKey, entry.Site.EventsRPSLimit, entry.Site.EventsRPSLimit*burstMultiplier) {
		return entry, Outcome{Reason: DropRateLimited}
	}
	return entry, Outcome{}
}

// Exposure validates, authorises and writes one exposure.
func (r *Recorder) Exposure(ctx context.Context, e Exposure, creds Credentials) Outcome {
	entry, out := r.authorise(e.Site, creds)
	if out.Reason != "" {
		return r.logged(kindExposure, e.Site, e.Experiment, out)
	}
	if !assign.ValidVisitorID(e.Visitor) {
		return r.logged(kindExposure, e.Site, e.Experiment, Outcome{Reason: DropBadVisitor})
	}
	exp := entry.Experiments[e.Experiment]
	if exp == nil || exp.ID == "" {
		return r.logged(kindExposure, e.Site, e.Experiment, Outcome{Reason: DropUnknownExp})
	}
	if !hasVariant(exp, e.Variant) {
		return r.logged(kindExposure, e.Site, e.Experiment, Outcome{Reason: DropUnknownVariant})
	}
	wctx, cancel := context.WithTimeout(ctx, WriteBudget)
	defer cancel()
	inserted, err := r.Store.InsertExposure(wctx, entry.Site.ID, exp.ID, e.Visitor, e.Variant)
	return r.logged(kindExposure, e.Site, e.Experiment, afterWrite(inserted, err))
}

// Conversion validates, authorises and writes one conversion.
func (r *Recorder) Conversion(ctx context.Context, c Conversion, creds Credentials) Outcome {
	entry, out := r.authorise(c.Site, creds)
	if out.Reason != "" {
		return r.logged(kindConversion, c.Site, c.Experiment, out)
	}
	if !assign.ValidVisitorID(c.Visitor) {
		return r.logged(kindConversion, c.Site, c.Experiment, Outcome{Reason: DropBadVisitor})
	}
	exp := entry.Experiments[c.Experiment]
	if exp == nil || exp.ID == "" {
		return r.logged(kindConversion, c.Site, c.Experiment, Outcome{Reason: DropUnknownExp})
	}
	if !experiment.ValidKey(c.Goal) || len(c.Goal) > maxGoalLen {
		return r.logged(kindConversion, c.Site, c.Experiment, Outcome{Reason: DropBadGoal})
	}
	if c.Value != nil && (math.IsNaN(*c.Value) || math.IsInf(*c.Value, 0) || math.Abs(*c.Value) > valueMaxAbs) {
		return r.logged(kindConversion, c.Site, c.Experiment, Outcome{Reason: DropBadValue})
	}
	wctx, cancel := context.WithTimeout(ctx, WriteBudget)
	defer cancel()
	inserted, err := r.Store.InsertConversion(wctx, entry.Site.ID, exp.ID, c.Visitor, c.Goal, c.Value)
	return r.logged(kindConversion, c.Site, c.Experiment, afterWrite(inserted, err))
}

func afterWrite(inserted bool, err error) Outcome {
	if err != nil {
		return Outcome{Reason: DropStoreError}
	}
	if inserted {
		return Outcome{Accepted: true, Inserted: true, Reason: ReasonAccepted}
	}
	return Outcome{Accepted: true, Reason: ReasonDuplicate}
}

func hasVariant(exp *experiment.Experiment, key string) bool {
	for _, v := range exp.Variants {
		if v.Key == key {
			return true
		}
	}
	return false
}

func (r *Recorder) logged(kind, siteKey, expKey string, out Outcome) Outcome {
	if r.Log == nil {
		return out
	}
	level := slog.LevelDebug
	if !out.Accepted {
		level = slog.LevelInfo
	}
	r.Log.Log(context.Background(), level, "event", "kind", kind, "site", siteKey, "experiment", expKey, "reason", out.Reason)
	return out
}
