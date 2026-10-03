package store

import "context"

// VariantCounts is the raw aggregate for one variant of one experiment.
type VariantCounts struct {
	VariantKey  string
	Exposures   int
	Converted   int            // distinct exposed visitors with at least one matching conversion
	ByGoal      map[string]int // distinct exposed visitors converted per goal
	ValueByGoal map[string]float64
}

// ExperimentCounts aggregates an experiment's events. Conversions are
// attributed by joining to the visitor's exposure, so the variant comes
// from what was actually shown, never from the conversion beacon. goal
// filters attribution to one goal when non-empty.
func (s *Store) ExperimentCounts(ctx context.Context, siteID, experimentID, goal string) (map[string]*VariantCounts, int, error) {
	out := map[string]*VariantCounts{}

	rows, err := s.pool.Query(ctx, `
		SELECT variant_key, count(*) FROM exposures
		WHERE site_id = $1 AND experiment_id = $2 GROUP BY variant_key`, siteID, experimentID)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		vc := &VariantCounts{ByGoal: map[string]int{}, ValueByGoal: map[string]float64{}}
		if err := rows.Scan(&vc.VariantKey, &vc.Exposures); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out[vc.VariantKey] = vc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT e.variant_key, c.goal, count(*), coalesce(sum(c.value), 0)
		FROM conversions c
		JOIN exposures e ON e.experiment_id = c.experiment_id AND e.visitor_id = c.visitor_id
		WHERE c.site_id = $1 AND c.experiment_id = $2 AND ($3 = '' OR c.goal = $3)
		GROUP BY e.variant_key, c.goal`, siteID, experimentID, goal)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var variant, g string
		var n int
		var value float64
		if err := rows.Scan(&variant, &g, &n, &value); err != nil {
			rows.Close()
			return nil, 0, err
		}
		vc := out[variant]
		if vc == nil {
			vc = &VariantCounts{VariantKey: variant, ByGoal: map[string]int{}, ValueByGoal: map[string]float64{}}
			out[variant] = vc
		}
		vc.ByGoal[g] = n
		vc.ValueByGoal[g] = value
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Distinct converted visitors per variant (a visitor with two goals counts once).
	rows, err = s.pool.Query(ctx, `
		SELECT e.variant_key, count(DISTINCT c.visitor_id)
		FROM conversions c
		JOIN exposures e ON e.experiment_id = c.experiment_id AND e.visitor_id = c.visitor_id
		WHERE c.site_id = $1 AND c.experiment_id = $2 AND ($3 = '' OR c.goal = $3)
		GROUP BY e.variant_key`, siteID, experimentID, goal)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var variant string
		var n int
		if err := rows.Scan(&variant, &n); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if vc := out[variant]; vc != nil {
			vc.Converted = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var unattributed int
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM conversions c
		WHERE c.site_id = $1 AND c.experiment_id = $2 AND ($3 = '' OR c.goal = $3)
		  AND NOT EXISTS (SELECT 1 FROM exposures e WHERE e.experiment_id = c.experiment_id AND e.visitor_id = c.visitor_id)`,
		siteID, experimentID, goal).Scan(&unattributed)
	return out, unattributed, err
}

// Goals lists the distinct goals recorded for an experiment.
func (s *Store) Goals(ctx context.Context, siteID, experimentID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT goal FROM conversions WHERE site_id = $1 AND experiment_id = $2 ORDER BY goal`, siteID, experimentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
