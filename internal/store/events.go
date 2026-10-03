package store

import "context"

// InsertExposure records that a visitor saw a variant. The primary key
// (experiment_id, visitor_id) makes repeats a no-op: the first write wins.
// Returns whether a row was inserted.
func (s *Store) InsertExposure(ctx context.Context, siteID, experimentID, visitorID, variantKey string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO exposures (site_id, experiment_id, visitor_id, variant_key)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		siteID, experimentID, visitorID, variantKey)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// InsertConversion records a goal for a visitor, once per
// (experiment, visitor, goal). value may be nil.
func (s *Store) InsertConversion(ctx context.Context, siteID, experimentID, visitorID, goal string, value *float64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO conversions (site_id, experiment_id, visitor_id, goal, value)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
		siteID, experimentID, visitorID, goal, value)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// InsertConversionsForExposed records a goal for every experiment the
// visitor has an exposure row for on this site, in one idempotent
// statement. This is how browser conversions are attributed: the page does
// not need to know which experiments the visitor saw elsewhere. Returns the
// number of rows inserted (zero when the visitor was never exposed or has
// already converted on this goal everywhere).
func (s *Store) InsertConversionsForExposed(ctx context.Context, siteID, visitorID, goal string, value *float64) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO conversions (site_id, experiment_id, visitor_id, goal, value)
		SELECT e.site_id, e.experiment_id, e.visitor_id, $3, $4
		FROM exposures e WHERE e.site_id = $1 AND e.visitor_id = $2
		ON CONFLICT DO NOTHING`,
		siteID, visitorID, goal, value)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// EventCounts is a test and diagnostics helper.
type EventCounts struct {
	Exposures   int
	Conversions int
}

// CountEvents returns the number of exposure and conversion rows for one
// experiment.
func (s *Store) CountEvents(ctx context.Context, experimentID string) (EventCounts, error) {
	var c EventCounts
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM exposures WHERE experiment_id = $1),
		       (SELECT count(*) FROM conversions WHERE experiment_id = $1)`, experimentID).Scan(&c.Exposures, &c.Conversions)
	return c, err
}
