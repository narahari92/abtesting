#!/usr/bin/env sh
# Provisions the tenant and experiments that examples/customer-site expects,
# using only the public HTTP API. Safe to re-run: existing objects are left
# alone and reported.
#
#   PLATFORM_ADMIN_KEY=... examples/customer-site/setup.sh
#
# Environment:
#   SERVICE_URL         where the variant service runs (default http://localhost:8080)
#   PLATFORM_ADMIN_KEY  the platform key the service was started with (required)
#   SITE_KEY            tenant key (default acme-demo, must match config.js)
#   SITE_ORIGIN         origin the example site is served from (default http://localhost:3000)
set -eu

SERVICE_URL="${SERVICE_URL:-http://localhost:8080}"
SITE_KEY="${SITE_KEY:-acme-demo}"
SITE_ORIGIN="${SITE_ORIGIN:-http://localhost:3000}"
: "${PLATFORM_ADMIN_KEY:?set PLATFORM_ADMIN_KEY to the key the service was started with}"

json_field() { python3 -c 'import sys, json; d = json.load(sys.stdin); print(d'"$1"')' 2>/dev/null || true; }

echo "service:  $SERVICE_URL"
echo "site:     $SITE_KEY (origin $SITE_ORIGIN)"
echo

# 1. Site. On 409 the site exists; the API key was shown once at creation and
#    cannot be read back, so offer rotation.
resp=$(curl -s -w '\n%{http_code}' -X POST "$SERVICE_URL/v1/platform/sites" \
  -H "Authorization: Bearer $PLATFORM_ADMIN_KEY" -H 'Content-Type: application/json' \
  -d "{\"key\":\"$SITE_KEY\",\"name\":\"Acme Analytics (example)\",\"allowed_origins\":[\"$SITE_ORIGIN\",\"http://127.0.0.1:3000\"]}")
code=$(printf '%s' "$resp" | tail -n1)
body=$(printf '%s' "$resp" | sed '$d')
case "$code" in
  201)
    API_KEY=$(printf '%s' "$body" | json_field '["api_key"]')
    echo "created site $SITE_KEY"
    ;;
  409)
    if [ -n "${SITE_API_KEY:-}" ]; then
      API_KEY="$SITE_API_KEY"
      echo "site $SITE_KEY exists; using SITE_API_KEY from the environment"
    else
      echo "site $SITE_KEY already exists. Rotating its API key so this script can continue."
      resp=$(curl -s -X POST "$SERVICE_URL/v1/platform/sites/$SITE_KEY/rotate-key" -H "Authorization: Bearer $PLATFORM_ADMIN_KEY")
      API_KEY=$(printf '%s' "$resp" | json_field '["api_key"]')
    fi
    ;;
  401) echo "platform key rejected (401). Check PLATFORM_ADMIN_KEY." >&2; exit 1 ;;
  *)   echo "create site failed: HTTP $code $body" >&2; exit 1 ;;
esac
[ -n "$API_KEY" ] || { echo "could not obtain the site API key" >&2; exit 1; }

admin() { # method path [json]
  if [ $# -ge 3 ]; then
    curl -s -w '\n%{http_code}' -X "$1" "$SERVICE_URL/v1/admin$2" -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' -d "$3"
  else
    curl -s -w '\n%{http_code}' -X "$1" "$SERVICE_URL/v1/admin$2" -H "Authorization: Bearer $API_KEY"
  fi
}

create_and_start() { # key json
  resp=$(admin POST /experiments "$2"); code=$(printf '%s' "$resp" | tail -n1)
  case "$code" in
    201) echo "created experiment $1" ;;
    409) echo "experiment $1 already exists" ;;
    *)   echo "create $1 failed: HTTP $code $(printf '%s' "$resp" | sed '$d')" >&2; exit 1 ;;
  esac
  resp=$(admin PATCH "/experiments/$1" '{"status":"running"}'); code=$(printf '%s' "$resp" | tail -n1)
  case "$code" in
    200) echo "  running" ;;
    409) echo "  $(printf '%s' "$resp" | sed '$d' | json_field '["error"]')" ;;
    *)   echo "  start failed: HTTP $code" >&2; exit 1 ;;
  esac
}

# 2. Content-driven experiment on the home page: copy only, no code on the customer side.
create_and_start hero-cta '{
  "key": "hero-cta",
  "url_path": "/",
  "name": "Homepage hero headline and CTA",
  "description": "Content-driven: the snippet writes headline and cta into data-ab elements.",
  "coverage_bp": 10000,
  "variants": [
    {"key": "control", "weight_bp": 5000, "is_control": true,
     "content": {"headline": "Analytics your whole team will actually use", "cta": "Start free trial"}},
    {"key": "b", "weight_bp": 5000,
     "content": {"headline": "See every metric in one place, in minutes", "cta": "Get started free"}}
  ]
}'

# 3. Key-driven experiment on the pricing page: the page branches on the variant key; content
#    carries settings the dashboard can tune without a deploy. 90 % coverage
#    leaves a 10 % hold-back that sees the default page.
create_and_start pricing-layout '{
  "key": "pricing-layout",
  "url_path": "/pricing.html",
  "name": "Pricing page layout",
  "description": "Key-driven: preselect annual billing and highlight a plan.",
  "coverage_bp": 9000,
  "variants": [
    {"key": "control", "weight_bp": 5000, "is_control": true,
     "content": {"default_period": "monthly", "featured_plan": "team"}},
    {"key": "annual-first", "weight_bp": 5000,
     "content": {"default_period": "annual", "featured_plan": "business"}}
  ]
}'

echo
echo "payload: $SERVICE_URL/v1/sites/$SITE_KEY/payload.json"
echo "site API key (shown once, keep it for the dashboard and results):"
echo "  $API_KEY"
echo
echo "Now serve the site from $SITE_ORIGIN, for example:"
echo "  python3 -m http.server 3000 --directory examples/customer-site"
