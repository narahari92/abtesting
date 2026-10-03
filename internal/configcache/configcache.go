package configcache

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"variantsvc/internal/experiment"
	"variantsvc/internal/payload"
)

// SiteConfig is everything a Source knows about one site.
type SiteConfig struct {
	Site        experiment.Site         `json:"site"`
	Experiments []experiment.Experiment `json:"experiments"`
}

// Entry is one site's compiled state inside a snapshot.
type Entry struct {
	Site    experiment.Site
	Payload *payload.Payload
	// Bytes is the serialized payload, computed once at build time so the
	// hot path only copies memory.
	Bytes []byte
	ETag  string
}

// Snapshot is an immutable view of all sites. Readers get a pointer and
// never see a half-built state.
type Snapshot struct {
	Sites    map[string]*Entry
	LoadedAt time.Time
}

// Source produces site configurations: a JSON file in phase 1, the
// database from phase 2 on.
type Source interface {
	Load(ctx context.Context) ([]SiteConfig, error)
}

// Build compiles site configurations into a snapshot. Experiments that are
// running but not servable are logged and left out.
func Build(configs []SiteConfig, now time.Time, log *slog.Logger) *Snapshot {
	s := &Snapshot{Sites: make(map[string]*Entry, len(configs)), LoadedAt: now}
	for _, c := range configs {
		var p *payload.Payload
		if c.Site.Status == experiment.SiteSuspended {
			p = payload.Empty(c.Site.Key)
		} else {
			var skipped []string
			p, skipped = payload.Compile(c.Site.Key, c.Site.PayloadVersion, c.Experiments)
			if len(skipped) > 0 && log != nil {
				log.Warn("experiments running but not servable", "site", c.Site.Key, "experiments", skipped)
			}
		}
		b, err := p.Marshal()
		if err != nil {
			if log != nil {
				log.Error("marshal payload", "site", c.Site.Key, "err", err)
			}
			continue
		}
		s.Sites[c.Site.Key] = &Entry{Site: c.Site, Payload: p, Bytes: b, ETag: p.ETag()}
	}
	return s
}

// Cache holds the current snapshot behind an atomic pointer. Get is a
// single pointer load plus a map lookup; Swap is a single pointer store.
type Cache struct {
	ptr atomic.Pointer[Snapshot]
}

// Current returns the current snapshot, or nil before the first load.
func (c *Cache) Current() *Snapshot { return c.ptr.Load() }

// Get returns the entry for a site key, or nil if unknown or not loaded.
func (c *Cache) Get(siteKey string) *Entry {
	s := c.ptr.Load()
	if s == nil {
		return nil
	}
	return s.Sites[siteKey]
}

// Swap installs a new snapshot.
func (c *Cache) Swap(s *Snapshot) { c.ptr.Store(s) }

// Refresh loads from src and swaps the snapshot. On error the previous
// snapshot stays in place: stale configuration beats no configuration.
func (c *Cache) Refresh(ctx context.Context, src Source, log *slog.Logger) error {
	configs, err := src.Load(ctx)
	if err != nil {
		return err
	}
	c.Swap(Build(configs, time.Now(), log))
	return nil
}

// Run refreshes on a fixed interval until ctx is cancelled. Failures are
// logged, never fatal. The first refresh happens immediately.
func (c *Cache) Run(ctx context.Context, src Source, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.Refresh(ctx, src, log); err != nil {
			age := -1.0
			if s := c.Current(); s != nil {
				age = time.Since(s.LoadedAt).Seconds()
			}
			log.Error("config refresh failed, serving last good snapshot", "err", err, "config_age_seconds", age)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
