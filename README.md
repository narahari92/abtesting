# Variant Service

A multi-tenant experimentation service: a small JavaScript snippet decides which variant a visitor sees, records exposures and conversions, and reports which variant is winning. Assignment is a deterministic hash evaluated in the browser against a cached per-site payload, so the service's origin is never on the page-render path.

Work in progress. The plan is in [`spec/implementation-plan.md`](spec/implementation-plan.md); the design is in [`spec/technical-plan.md`](spec/technical-plan.md).

## Develop

```sh
make build        # bin/server
make test         # Go and JS tests (database tests skip unless TEST_DATABASE_URL is set)
make lint         # go vet + gofmt
```

Run with a database (`docker compose up -d`, or `scripts/localdb.sh start` for a throwaway Homebrew Postgres when Docker is unavailable):

```sh
eval "$(scripts/localdb.sh start)"      # exports DATABASE_URL and TEST_DATABASE_URL
PLATFORM_ADMIN_KEY=change-me make run   # migrates at boot, serves on :8080
```

Run from a config file instead, with no database and the control plane disabled:

```sh
CONFIG_FILE=internal/configcache/testdata/config.sample.json make run
```
