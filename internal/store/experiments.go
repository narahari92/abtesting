package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"variantsvc/internal/experiment"
)

const experimentColumns = `id, site_id, key, name, description, status, seed, hash_version, coverage_bp, url_path, created_at, updated_at`

func scanExperiment(row pgx.Row) (experiment.Experiment, error) {
	var e experiment.Experiment
	err := row.Scan(&e.ID, &e.SiteID, &e.Key, &e.Name, &e.Description, &e.Status, &e.Seed,
		&e.HashVersion, &e.CoverageBP, &e.URLPath, &e.CreatedAt, &e.UpdatedAt)
	return e, mapErr(err)
}

// CreateExperiment inserts an experiment with its variants and bumps the
// site's payload version, all in one transaction. The experiment must
// already be validated.
func (s *Store) CreateExperiment(ctx context.Context, siteID string, e experiment.Experiment) (experiment.Experiment, error) {
	var out experiment.Experiment
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO experiments (site_id, key, name, description, status, seed, hash_version, coverage_bp, url_path)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING `+experimentColumns,
			siteID, e.Key, e.Name, e.Description, e.Status, e.Seed, e.HashVersion, e.CoverageBP, e.URLPath)
		var err error
		if out, err = scanExperiment(row); err != nil {
			return err
		}
		if err := insertVariants(ctx, tx, out.ID, e.Variants); err != nil {
			return err
		}
		out.Variants = e.Variants
		return bumpPayloadVersion(ctx, tx, siteID)
	})
	if err != nil {
		return experiment.Experiment{}, mapErr(err)
	}
	return s.GetExperiment(ctx, siteID, out.Key)
}

// SaveExperiment persists changes to an existing experiment: scalar fields
// and the complete variant list (replaced). Bumps the payload version.
func (s *Store) SaveExperiment(ctx context.Context, siteID string, e experiment.Experiment) (experiment.Experiment, error) {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE experiments SET name = $3, description = $4, status = $5, coverage_bp = $6, url_path = $7, updated_at = now()
			WHERE site_id = $1 AND key = $2`,
			siteID, e.Key, e.Name, e.Description, e.Status, e.CoverageBP, e.URLPath)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM variants WHERE experiment_id = $1`, e.ID); err != nil {
			return err
		}
		if err := insertVariants(ctx, tx, e.ID, e.Variants); err != nil {
			return err
		}
		return bumpPayloadVersion(ctx, tx, siteID)
	})
	if err != nil {
		return experiment.Experiment{}, mapErr(err)
	}
	return s.GetExperiment(ctx, siteID, e.Key)
}

func insertVariants(ctx context.Context, tx pgx.Tx, experimentID string, variants []experiment.Variant) error {
	for i, v := range variants {
		content := v.Content
		if content == nil {
			content = map[string]any{}
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		position := v.Position
		if position == 0 {
			position = i
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO variants (experiment_id, key, weight_bp, is_control, content, source, approved, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			experimentID, v.Key, v.WeightBP, v.IsControl, raw, v.Source, v.Approved, position); err != nil {
			return err
		}
	}
	return nil
}

// GetExperiment returns one experiment with its variants in position order.
// An experiment belonging to another site is ErrNotFound.
func (s *Store) GetExperiment(ctx context.Context, siteID, key string) (experiment.Experiment, error) {
	e, err := scanExperiment(s.pool.QueryRow(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE site_id = $1 AND key = $2`, siteID, key))
	if err != nil {
		return experiment.Experiment{}, err
	}
	byID, err := s.variantsFor(ctx, []string{e.ID})
	if err != nil {
		return experiment.Experiment{}, err
	}
	e.Variants = byID[e.ID]
	return e, nil
}

// ListExperiments returns a site's experiments, newest first, with variants.
func (s *Store) ListExperiments(ctx context.Context, siteID string) ([]experiment.Experiment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+experimentColumns+` FROM experiments WHERE site_id = $1 ORDER BY created_at DESC, key`, siteID)
	if err != nil {
		return nil, err
	}
	exps, err := collectExperiments(rows)
	if err != nil {
		return nil, err
	}
	return s.attachVariants(ctx, exps)
}

// LoadAll returns every site with all of its experiments, for the
// configuration refresher. Three queries regardless of tenant count.
func (s *Store) LoadAll(ctx context.Context) ([]experiment.Site, map[string][]experiment.Experiment, error) {
	sites, err := s.ListSites(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+experimentColumns+` FROM experiments ORDER BY site_id, key`)
	if err != nil {
		return nil, nil, err
	}
	exps, err := collectExperiments(rows)
	if err != nil {
		return nil, nil, err
	}
	exps, err = s.attachVariants(ctx, exps)
	if err != nil {
		return nil, nil, err
	}
	bySite := make(map[string][]experiment.Experiment, len(sites))
	for _, e := range exps {
		bySite[e.SiteID] = append(bySite[e.SiteID], e)
	}
	return sites, bySite, nil
}

func collectExperiments(rows pgx.Rows) ([]experiment.Experiment, error) {
	defer rows.Close()
	out := []experiment.Experiment{}
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) attachVariants(ctx context.Context, exps []experiment.Experiment) ([]experiment.Experiment, error) {
	if len(exps) == 0 {
		return exps, nil
	}
	ids := make([]string, len(exps))
	for i, e := range exps {
		ids[i] = e.ID
	}
	byID, err := s.variantsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range exps {
		exps[i].Variants = byID[exps[i].ID]
		if exps[i].Variants == nil {
			exps[i].Variants = []experiment.Variant{}
		}
	}
	return exps, nil
}

func (s *Store) variantsFor(ctx context.Context, experimentIDs []string) (map[string][]experiment.Variant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT experiment_id, key, weight_bp, is_control, content, source, approved, position
		FROM variants WHERE experiment_id = ANY($1::uuid[]) ORDER BY experiment_id, position, key`, experimentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]experiment.Variant, len(experimentIDs))
	for rows.Next() {
		var expID string
		var v experiment.Variant
		var raw []byte
		if err := rows.Scan(&expID, &v.Key, &v.WeightBP, &v.IsControl, &raw, &v.Source, &v.Approved, &v.Position); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &v.Content); err != nil {
			return nil, err
		}
		if v.Content == nil {
			v.Content = map[string]any{}
		}
		out[expID] = append(out[expID], v)
	}
	return out, rows.Err()
}
