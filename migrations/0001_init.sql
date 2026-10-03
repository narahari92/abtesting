-- Tenants. The API key is stored only as a SHA-256 hash.
CREATE TABLE sites (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key              text NOT NULL UNIQUE,
    name             text NOT NULL DEFAULT '',
    api_key_hash     text NOT NULL UNIQUE,
    allowed_origins  text[] NOT NULL DEFAULT '{}',
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    assign_rps_limit integer NOT NULL DEFAULT 2000,
    events_rps_limit integer NOT NULL DEFAULT 500,
    payload_version  bigint NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE experiments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id      uuid NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    key          text NOT NULL,
    name         text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'running', 'paused', 'archived')),
    seed         text NOT NULL,
    hash_version integer NOT NULL DEFAULT 1,
    coverage_bp  integer NOT NULL CHECK (coverage_bp BETWEEN 0 AND 10000),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, key)
);

CREATE TABLE variants (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    experiment_id uuid NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    key           text NOT NULL,
    weight_bp     integer NOT NULL CHECK (weight_bp BETWEEN 0 AND 10000),
    is_control    boolean NOT NULL DEFAULT false,
    content       jsonb NOT NULL DEFAULT '{}'::jsonb,
    source        text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'llm')),
    approved      boolean NOT NULL DEFAULT true,
    position      integer NOT NULL DEFAULT 0,
    UNIQUE (experiment_id, key)
);

-- Events are append-only. site_id is denormalised so results queries and
-- deletion are single-table predicates, and so partitioning by tenant
-- later needs no schema change.
CREATE TABLE exposures (
    site_id       uuid NOT NULL,
    experiment_id uuid NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    visitor_id    text NOT NULL,
    variant_key   text NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (experiment_id, visitor_id)
);
CREATE INDEX exposures_experiment_variant_idx ON exposures (experiment_id, variant_key);
CREATE INDEX exposures_site_idx ON exposures (site_id);

CREATE TABLE conversions (
    site_id       uuid NOT NULL,
    experiment_id uuid NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    visitor_id    text NOT NULL,
    goal          text NOT NULL,
    value         numeric,
    first_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (experiment_id, visitor_id, goal)
);
CREATE INDEX conversions_site_idx ON conversions (site_id);
