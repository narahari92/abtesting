# Example customer site

A small static website for a fictional company, "Acme Analytics", that integrates with the variant service the way a real customer would: the snippet is installed by URL, the site runs on its own origin, and the service's origin is never on the render path after the first visit.

What it demonstrates:

| Page | Experiment | Style |
|---|---|---|
| `index.html` | `hero-cta` (page `/`) | Content-driven: `data-ab="hero-cta:headline"` and `data-ab="hero-cta:cta"` elements receive the chosen variant's text. No page code involved. |
| `pricing.html` | `pricing-layout` (page `/pricing.html`) | Key-driven: page code reads `ab.ready` and branches on the variant key; `content` carries settings (default billing period, featured plan). 90 % coverage, so one visitor in ten is held back and sees the default page. |
| `docs.html` | none | The snippet runs, finds no experiment for this page, and records nothing. A conversion fired here is still attributed to experiments the user was exposed to on other pages. |
| `login.html` | none | Mock login, any password. Every other page redirects here without a session. |

Every page has a debug panel (bottom right) showing the user, the visitor id, payload version and source, the number of payload requests in this view, and the assignments, with "Refetch payload" and "Log out" buttons.

## Run it

1. Start the service with a database and a platform key (see the repository README):

   ```sh
   PLATFORM_ADMIN_KEY=plat-secret make run
   ```

2. Provision the tenant and both experiments through the API:

   ```sh
   PLATFORM_ADMIN_KEY=plat-secret examples/customer-site/setup.sh
   ```

   It creates site `acme-demo` with `http://localhost:3000` in its origin allow-list, creates and starts the two experiments, and prints the site API key.

3. Serve the site on its own origin:

   ```sh
   python3 -m http.server 3000 --directory examples/customer-site
   ```

   Open <http://localhost:3000/>.

To point the pages at a different service host or tenant, edit `config.js` and pass `SERVICE_URL` / `SITE_KEY` / `SITE_ORIGIN` to `setup.sh`.

## One page per experiment

Every experiment has a `url_path`, the single page it runs on. The snippet evaluates and records exposure only when `location.pathname` matches it (trailing slashes and `/index.html` are normalised), so `hero-cta` counts an exposure only for visits to the home page and `pricing-layout` only for the pricing page. Conversions are attributed by the server to the experiments the user has been exposed to, whichever page the conversion happens on: a signup on the docs page still counts for `hero-cta` if the user saw the home page first.

## Identity

The site requires a login (`login.html`, any password). `session.js` keeps the username in `localStorage` and every page hands it to the snippet as `visitorId: "user:<name>"`, so the experiment identity is the account, not the browser: the same user sees the same variants on a phone, a laptop and after clearing cookies, and the server-side `GET /v1/assign?v=user:<name>` agrees with the browser. The snippet never sets its `_abv` cookie on this site because an explicit visitor id is always supplied.

Logging out invalidates the cache: the session and the cached payload are removed, and the next login starts from a fresh payload fetch. Nothing about exposures or conversions is kept in the browser.

## See the results

Open the service's dashboard at <http://localhost:8080/dashboard/> and sign in with the site API key that `setup.sh` printed. Select `hero-cta` or `pricing-layout`: the results table shows exposures and conversions per variant from the beacons this site sent, with a goal filter for `signup` and `checkout`. The dashboard is also where you pause, resume, archive or raise coverage, and where new experiments are created.

## Things to try

- Log in as `alice`, reload the home page several times: same headline, `payloadSource: cache`, zero payload requests. Log out and log in as `bob`, `carol`, ... until you have seen both headlines.
- Open the pricing page: annual billing is preselected and the Business plan highlighted for the `annual-first` variant, otherwise monthly and Team. About one user in ten sees no `pricing-layout` assignment at all.
- Append `?ab_force=hero-cta:b` to the URL to force a variant for QA.
- Pause `hero-cta` from the admin API, then reload after the 60 s stale window (or click "Refetch payload"): the default headline returns and `payloadVersion` has increased.
- Stop the service and reload: the cached payload keeps the experiments running. Clear site data and reload with the service down: defaults render within the snippet timeout and the panel shows `payloadSource: none`.
- Open a private window and log in as `alice` again: identical variants to the first window. Compare with `curl 'localhost:8080/v1/assign?site=acme-demo&v=user:alice'`.
- Watch the Network tab on a first visit: one `payload.json` request to the service origin with `Access-Control-Allow-Origin: *`, no preflight.

Buttons call `track('signup')` and `track('checkout', price)`, which forward to `ab.convert`. The snippet keeps no tracking state in the browser: it sends one exposure beacon per evaluated experiment on every page view, and a single conversion beacon naming only the user and the goal. The server deduplicates exposures by primary key and attributes each conversion to every experiment that user has an exposure for on this site. Beacons from an origin that is not in the site's allow-list are accepted with 202 and silently dropped, which you can see by serving the site from a different port.
