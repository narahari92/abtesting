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
