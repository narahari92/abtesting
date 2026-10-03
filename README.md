# Variant Service

A multi-tenant experimentation service for websites. A small JavaScript snippet on a customer's page decides which variant each visitor sees, records exposures and conversions, and a dashboard reports how each variant is doing.

Assignment is a deterministic hash of an experiment's seed and the visitor's id, evaluated **in the browser** against a cached per-site payload. Nothing per visitor is stored, and after the first visit the service is not contacted on the page-render path at all. The design and its reasoning are in [`DESIGN.md`](DESIGN.md).

## Contents

- [Prerequisites](#prerequisites)
- [Run with Docker Compose](#run-with-docker-compose)
- [Run with a local Postgres and no Docker](#run-with-a-local-postgres-and-no-docker)
- [Run from a config file, no database](#run-from-a-config-file-no-database)
- [Configuration](#configuration)
- [First use: create a site and an experiment](#first-use-create-a-site-and-an-experiment)
- [Dashboard](#dashboard)
- [Example customer site](#example-customer-site)
- [Integrating the snippet](#integrating-the-snippet)
- [API overview](#api-overview)
- [Tests](#tests)
- [Repository layout](#repository-layout)

## Prerequisites

- Go 1.23 or newer
- Node 22 or newer (only for the snippet's tests)
- One of: Docker with Compose, or a local PostgreSQL 16/17 install (Homebrew `postgresql@17` works)
- `curl` and `python3` for the helper scripts

Clone the repository and copy the environment template:

```sh
cp .env.example .env     # edit values; .env is gitignored and never committed
```

The service reads plain environment variables. The commands below export them inline; you can equally `source .env` after filling it in.

## Run with Docker Compose

Compose runs PostgreSQL only. The Go binary runs on your machine so you can rebuild and restart it quickly.

```sh
docker compose up -d                        # PostgreSQL 17 on localhost:5432, data in a named volume
docker compose ps                           # wait for "healthy"

export DATABASE_URL='postgres://variants:variants@localhost:5432/variants?sslmode=disable'
export TEST_DATABASE_URL="$DATABASE_URL"    # lets the integration tests run

PUBLIC_BASE_URL=http://localhost:8080 PLATFORM_ADMIN_KEY=plat-secret make run
```

`make run` builds `bin/server` and starts it on port 8080. On the first start it applies the embedded migrations and logs `config source: postgres`. Check it is up:

```sh
curl -s localhost:8080/readyz
# {"status":"ok","snapshot_loaded":true,"config_age_seconds":0.4,"sites":0,"db_reachable":true}
```

Useful Compose commands:

```sh
docker compose exec -T postgres psql -U variants -d variants -c 'select key, status from sites;'
docker compose stop postgres     # simulate a database outage: pages keep working, admin returns 503
docker compose start postgres
docker compose down              # stop; add -v to also delete the data volume
```

If port 5432 is already in use on your machine, change the left side of the `ports:` mapping in `docker-compose.yml` and the port in the two URLs above.

## Run with a local Postgres and no Docker

`scripts/localdb.sh` runs a throwaway PostgreSQL from a locally installed server (Homebrew `postgresql@17` or `postgresql@16`, or a Debian package). It keeps its data directory in `./.localdb`, which is gitignored, listens on `127.0.0.1:54329`, and registers nothing as a system service.

```sh
brew install postgresql@17                  # once, if you do not have a server binary

eval "$(scripts/localdb.sh start)"          # initdb on first run, start, then export DATABASE_URL and TEST_DATABASE_URL
PUBLIC_BASE_URL=http://localhost:8080 PLATFORM_ADMIN_KEY=plat-secret make run
```

The `start` command prints the two `export` lines, which is why it is wrapped in `eval`. Other commands:

```sh
scripts/localdb.sh url        # print the connection string
scripts/localdb.sh stop       # stop the server, keep the data
scripts/localdb.sh destroy    # stop and delete ./.localdb
PGPORT=55432 scripts/localdb.sh start      # use another port
PG_BIN=/path/to/pg/bin scripts/localdb.sh start   # server binaries in a non-standard place
```

The script sets `LC_ALL=C` and `PGGSSENCMODE=disable` for its own calls, which avoids two macOS pitfalls: PostgreSQL refusing the default locale, and `libpq` attempting Kerberos against a corporate realm and hanging. If you use `psql` yourself against this database, export the same two variables.

## Run from a config file, no database

For a quick look at the read path only. Experiments come from a JSON file; the admin, platform and tracking endpoints are disabled (admin and platform answer 503, tracking answers 202 and drops).

```sh
CONFIG_FILE=internal/configcache/testdata/config.sample.json make run
open http://localhost:8080/demo
```

The sample file defines a `demo` site with two running experiments on the `/demo` page. Edit the file while the server runs: it is re-read every `CONFIG_REFRESH_INTERVAL` and the payload's version changes.

## Configuration

All settings are environment variables. `.env.example` documents each one.

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DATABASE_URL` | | PostgreSQL connection string. When set, the service migrates at boot and enables the control plane and tracking |
| `CONFIG_FILE` | | JSON configuration used when `DATABASE_URL` is empty |
| `PLATFORM_ADMIN_KEY` | | Bearer key for `/v1/platform/*`. Without it those endpoints answer 503. Generate with `openssl rand -hex 32` |
| `PUBLIC_BASE_URL` | | Public URL of this service. Baked into the served snippet as the host to fetch payloads from |
| `CDN_BASE_URL` | `PUBLIC_BASE_URL` | Host the snippet fetches from when a CDN sits in front |
| `CONFIG_REFRESH_INTERVAL` | `10s` | How often each replica reloads configuration into memory |
| `LOG_LEVEL` | `info` | `debug` also logs every accepted event with its outcome |
| `ANTHROPIC_API_KEY`, `LLM_MODEL` | | Reserved for the LLM-assisted variant feature, not yet built. Never sent to browsers |
| `DEMO_MODE` | | Reserved for the demo site and simulate endpoint, not yet built |

One of `DATABASE_URL` or `CONFIG_FILE` is required; the service exits with a message if neither is set. When both are set, `DATABASE_URL` wins.

## First use: create a site and an experiment

A **site** is a tenant. The platform operator creates it with the platform key and receives the site's private API key once.

```sh
B=localhost:8080

curl -s -X POST $B/v1/platform/sites \
  -H 'Authorization: Bearer plat-secret' -H 'Content-Type: application/json' \
  -d '{"key":"acme","name":"Acme","allowed_origins":["https://www.acme.com"]}'
# {"site":{...},"api_key":"sk_live_...","note":"Store this key now. It is shown once and only its hash is kept."}

export KEY=sk_live_...
```

The customer configures experiments with that key. Every experiment runs on exactly one page of the site, given as a URL path.

```sh
curl -s -X POST $B/v1/admin/experiments \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"key":"hero-cta","url_path":"/","name":"Hero headline","coverage_bp":10000,
       "variants":[{"key":"control","weight_bp":5000,"is_control":true,"content":{"headline":"Default headline"}},
                   {"key":"b","weight_bp":5000,"content":{"headline":"Ship faster"}}]}'

curl -s -X PATCH $B/v1/admin/experiments/hero-cta \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"status":"running"}'

curl -s $B/v1/sites/acme/payload.json                   # the public payload the snippet evaluates
curl -s "$B/v1/assign?site=acme&v=visitor-1&path=/"     # the same decision, computed server-side
```

Weights are basis points and must total 10,000; `coverage_bp` is the share of visitors admitted. Once running, weights and variants are immutable and coverage may only increase; the API explains why in its 409 responses.

## Dashboard

Open <http://localhost:8080/dashboard/> and sign in with a site API key. The dashboard lists experiments with their page and status, creates and edits drafts, starts, pauses, resumes and archives, raises coverage, moves an experiment to another page, edits the site's allowed origins, and shows results per variant: exposures, conversions, conversion rate and a per-goal breakdown, with a goal filter and optional auto-refresh. The key is kept in the browser tab's session only.

## Example customer site

`examples/customer-site` is a static site for a fictional company that integrates the way a real customer would: the snippet is installed by URL, the site runs on its own origin, and a mock login gives every user a stable id.

```sh
PLATFORM_ADMIN_KEY=plat-secret examples/customer-site/setup.sh   # creates site acme-demo and two experiments via the API
make example-site                                                 # serves it on http://localhost:3000
```

Log in with any name and password, then watch the debug panel at the bottom right and the dashboard. The example's own [README](examples/customer-site/README.md) lists things to try, including outages and cross-page conversions.

## Integrating the snippet

Install by URL, or inline the contents of `/v1/ab.js` to save a request:

```html
<link rel="preconnect" href="https://<service-host>" crossorigin>
<style>html:not([data-ab-ready]) [data-ab]{visibility:hidden}</style>
<script src="https://<service-host>/v1/ab.js"></script>
<script>ab.init({ site: "acme", visitorId: window.currentUserId /* optional */ });</script>

<h1 data-ab="hero-cta:headline">Default headline</h1>
<button onclick="ab.convert('signup')">Sign up</button>
```

- **Content-driven:** elements marked `data-ab="<experiment>:<field>"` receive the chosen variant's string field. No page code.
- **Key-driven:** `ab.ready.then(({assignments}) => ...)` gives the variant key and `content` for each experiment on this page, for layout or flow changes.
- **Identity:** without `visitorId` the snippet sets a first-party cookie on your domain. Pass a stable user id to get the same variant across devices.
- **Conversions:** `ab.convert(goal, {value})` sends one beacon; the server attributes it to every experiment the visitor was exposed to, on any page.
- **QA:** append `?ab_force=<experiment>:<variant>` to a page URL.
- **Fail-safe:** the page is authored with default content; the snippet only swaps text, times out after 300 ms, never throws, and keeps working from its cached payload when the service is unreachable.

Beacons from a browser are accepted only when the page's origin is in the site's allow-list. Server-side integrations call `GET /v1/assign` and `POST /v1/events/*` with the site API key instead.

## API overview

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /v1/sites/{site}/payload.json` | none | Compiled per-site configuration, cacheable |
| `GET /v1/assign?site&v&e&path` | none | Server-side evaluation for one visitor |
| `POST /v1/events/exposure` | origin or site key | `{site, v, experiment, variant}`; always 202 |
| `POST /v1/events/conversion` | origin or site key | `{site, v, goal, value?}`; always 202 |
| `GET /v1/ab.js` | none | The snippet |
| `GET /v1/admin/site`, `PATCH` | site key | Site settings, allowed origins |
| `POST /v1/admin/experiments`, `GET`, `GET /{key}`, `PATCH /{key}` | site key | Experiment lifecycle |
| `GET /v1/admin/experiments/{key}/results?goal` | site key | Counts per variant |
| `POST /v1/platform/sites`, `GET`, `PATCH /{key}`, `POST /{key}/rotate-key`, `DELETE /{key}` | platform key | Tenant management |
| `GET /healthz`, `GET /readyz` | none | Liveness; readiness with snapshot age and database reachability |

Errors from the admin and platform APIs are JSON `{"error": "..."}` with a message meant to be shown to the user.

## Tests

```sh
make test        # go test ./... and node --test web/*.test.js
make lint        # go vet and gofmt
```

Database-backed tests run when `TEST_DATABASE_URL` is set and skip otherwise. Each test creates its own schema and drops it, so the database you point them at stays clean. The Go and JavaScript evaluators share a golden fixture, `internal/assign/testdata/golden.json`, and both suites must match it exactly. CI runs everything against a PostgreSQL service container.

## Repository layout

```
cmd/server/            the binary: wiring, environment, graceful shutdown
internal/assign/       the hash, ranges and choice, with the golden fixture
internal/experiment/   domain types, validation, status machine, URL path normalisation
internal/payload/      compiles experiments into the public payload
internal/configcache/  in-memory snapshot, file and database sources, refresher
internal/site/         API keys, origin matching, rate limiter
internal/store/        PostgreSQL repositories and embedded migrations
internal/events/       the write path: authorise, validate, insert
internal/results/      counts per variant
internal/httpapi/      handlers, middleware, static assets
web/                   ab.js and its tests, the dashboard, the demo page
migrations/            SQL applied at boot, in order
examples/customer-site a static customer site with login and a provisioning script
scripts/localdb.sh     throwaway local PostgreSQL without Docker
deploy/Dockerfile      single static binary image
spec/                  the brief, the technical plan and the phased implementation plan
DESIGN.md              the design document
```
