package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"variantsvc/internal/experiment"
)

const siteColumns = `id, key, name, status, api_key_hash, allowed_origins, assign_rps_limit, events_rps_limit, payload_version, created_at`

func scanSite(row pgx.Row) (experiment.Site, error) {
	var s experiment.Site
	err := row.Scan(&s.ID, &s.Key, &s.Name, &s.Status, &s.APIKeyHash, &s.AllowedOrigins,
		&s.AssignRPSLimit, &s.EventsRPSLimit, &s.PayloadVersion, &s.CreatedAt)
	if s.AllowedOrigins == nil {
		s.AllowedOrigins = []string{}
	}
	return s, mapErr(err)
}

// CreateSite inserts a site. The caller supplies the API key hash; the key
// itself never reaches the store. Zero limits take the schema defaults.
func (s *Store) CreateSite(ctx context.Context, in experiment.Site) (experiment.Site, error) {
	if in.AllowedOrigins == nil {
		in.AllowedOrigins = []string{}
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO sites (key, name, api_key_hash, allowed_origins, assign_rps_limit, events_rps_limit)
		VALUES ($1, $2, $3, $4,
		        CASE WHEN $5 > 0 THEN $5 ELSE 2000 END,
		        CASE WHEN $6 > 0 THEN $6 ELSE 500 END)
		RETURNING `+siteColumns,
		in.Key, in.Name, in.APIKeyHash, in.AllowedOrigins, in.AssignRPSLimit, in.EventsRPSLimit)
	return scanSite(row)
}

// GetSite looks a site up by its public key.
func (s *Store) GetSite(ctx context.Context, key string) (experiment.Site, error) {
	return scanSite(s.pool.QueryRow(ctx, `SELECT `+siteColumns+` FROM sites WHERE key = $1`, key))
}

// GetSiteByAPIKeyHash resolves a private API key (already hashed) to its site.
func (s *Store) GetSiteByAPIKeyHash(ctx context.Context, hash string) (experiment.Site, error) {
	return scanSite(s.pool.QueryRow(ctx, `SELECT `+siteColumns+` FROM sites WHERE api_key_hash = $1`, hash))
}

// ListSites returns every site, oldest first.
func (s *Store) ListSites(ctx context.Context) ([]experiment.Site, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+siteColumns+` FROM sites ORDER BY created_at, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []experiment.Site{}
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// UpdateSiteSettings changes name and allowed origins. Neither affects the
// compiled payload, so payload_version is left alone.
func (s *Store) UpdateSiteSettings(ctx context.Context, key, name string, origins []string) (experiment.Site, error) {
	if origins == nil {
		origins = []string{}
	}
	return scanSite(s.pool.QueryRow(ctx, `
		UPDATE sites SET name = $2, allowed_origins = $3, updated_at = now()
		WHERE key = $1 RETURNING `+siteColumns, key, name, origins))
}

// SetSiteStatus suspends or reactivates a site and bumps the payload
// version, since a suspended site compiles to an empty payload.
func (s *Store) SetSiteStatus(ctx context.Context, key, status string) (experiment.Site, error) {
	return scanSite(s.pool.QueryRow(ctx, `
		UPDATE sites SET status = $2, payload_version = payload_version + 1, updated_at = now()
		WHERE key = $1 RETURNING `+siteColumns, key, status))
}

// RotateSiteKey replaces the API key hash. The old key stops working on
// every replica once their snapshots refresh.
func (s *Store) RotateSiteKey(ctx context.Context, key, newHash string) (experiment.Site, error) {
	return scanSite(s.pool.QueryRow(ctx, `
		UPDATE sites SET api_key_hash = $2, updated_at = now()
		WHERE key = $1 RETURNING `+siteColumns, key, newHash))
}

// DeleteSite removes a site; experiments, variants and events cascade.
func (s *Store) DeleteSite(ctx context.Context, key string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sites WHERE key = $1`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// bumpPayloadVersion is called inside every transaction that changes what
// a site's payload compiles to.
func bumpPayloadVersion(ctx context.Context, tx pgx.Tx, siteID string) error {
	_, err := tx.Exec(ctx, `UPDATE sites SET payload_version = payload_version + 1, updated_at = now() WHERE id = $1`, siteID)
	return err
}
