package configcache

import (
	"context"

	"variantsvc/internal/store"
)

// DBSource loads every site and its experiments from PostgreSQL. Each
// refresh is three queries regardless of tenant count; a per-site
// updated_at watermark is the next step if that ever matters.
type DBSource struct {
	Store *store.Store
}

// Load implements Source.
func (d DBSource) Load(ctx context.Context) ([]SiteConfig, error) {
	sites, bySite, err := d.Store.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SiteConfig, 0, len(sites))
	for _, s := range sites {
		out = append(out, SiteConfig{Site: s, Experiments: bySite[s.ID]})
	}
	return out, nil
}
