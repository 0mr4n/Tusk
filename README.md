# Tusk

Tusk is an open source URL shortener written in Go and built for high traffic.

- Runs on Postgres, MySQL/MariaDB or SQLite.
- JSON API v2 with `X-API-KEY` keys, plus a `token` JWT cookie for the web UI. Passwords use bcrypt.
- Configured with environment variables, each of which also has a `*_FILE` Docker-secret form.
- Docker volumes and port: `/var/lib/tusk` (SQLite), `/tusk/custom` (UI overrides), `:3000`.

Highlights:

- **Redirect hot path**: an in-process LRU, then Redis, then one indexed DB query, with singleflight and
  a negative cache. There are no DB writes on the request path. The budget is p99 < 5 ms at the origin.
- **Visit pipeline**: clicks become events, and a worker folds them into per-(link, hour) deltas.
  10k clicks/s on one link costs about one row update per second instead of 10k transactions.
- **Optional microservice split**: `redirector` ×N, `api` ×M and `worker` ×K from a single image, behind
  Traefik and Cloudflare. See [deploy/README.md](deploy/README.md).
- **Configurable geo**: the Cloudflare `CF-IPCountry` header, a MaxMind or DB-IP `.mmdb` file, or none.
- **Web UI**: a React 19 + shadcn/ui SPA with a dark mode and mobile layouts. It is embedded in the binary.

Architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick start

```sh
# Single binary + SQLite
JWT_SECRET=$(openssl rand -hex 32) DEFAULT_DOMAIN=localhost:3000 docker compose up -d
# The compose files pull ghcr.io/0mr4n/tusk:latest; pin with TUSK_IMAGE=ghcr.io/0mr4n/tusk:0.3.1
open http://localhost:3000          # first visit → create the admin account
```

### Prebuilt image

Every release is published to GitHub Container Registry for `linux/amd64` and `linux/arm64`:

```sh
docker pull ghcr.io/0mr4n/tusk:latest     # or a pinned version, e.g. :0.3.1 / :0.3
docker run -d -p 3000:3000 -v tusk-data:/var/lib/tusk \
  -e JWT_SECRET=$(openssl rand -hex 32) -e DEFAULT_DOMAIN=localhost:3000 \
  ghcr.io/0mr4n/tusk:latest
```

Releases are cut by pushing a `vX.Y.Z` tag (see `.github/workflows/release.yml`).

With Postgres or MariaDB:

```sh
DB_PASSWORD=change-me JWT_SECRET=... docker compose -f docker-compose.postgres.yml up -d
DB_PASSWORD=change-me JWT_SECRET=... docker compose -f docker-compose.mariadb.yml up -d
```

Scaled out behind Traefik + Cloudflare: [deploy/README.md](deploy/README.md).

### From source

Requires Go 1.27+ and Node 22+ (26 recommended).

```sh
make build        # npm ci + vite build → internal/webui/dist → static binaries in bin/
make run          # ./bin/tusk on :3000 (SQLite at db/data)
make test         # go vet, go test -race ./..., web typecheck + lint
make docker       # docker build -t tusk:dev .  (run it with TUSK_IMAGE=tusk:dev docker compose up -d)
```

`go build ./...` also works without Node. The binary then serves a placeholder page instead of the UI,
and the API and redirects work normally. For UI development, run `cd web && npm run dev`, which proxies
`/api` to `TUSK_API` (default `http://127.0.0.1:3000`).

### Binaries

| Binary | Roles | Port |
|---|---|---|
| `tusk` | all (`SERVICE_ROLES=all`) | `PORT` (3000) |
| `redirector` | `GET /{id}`, custom-domain homepage redirect, protected / banned / 404 pages, `/api/v2/health` | `REDIRECTOR_PORT` (3001) |
| `api` | `/api/v2/*`, auth, OIDC, admin, web UI | `PORT` (3000) |
| `worker` | visit event consumer, expired-link sweeper | metrics only |

The Docker image contains all four under `/usr/local/bin/`. `tusk` is the default entrypoint.

## Configuration

Every variable also accepts `NAME_FILE=/path`,
which reads the value from a file (Docker secrets).

### Core variables

| Variable | Description | Default |
|---|---|---|
| `JWT_SECRET` | Signs auth tokens. Keep it stable across restarts so sessions survive. Required in production. | dev: `securekey` |
| `PORT` | HTTP port (all-in-one and `api`) | `3000` |
| `SITE_NAME` | Site name shown in the UI and page titles | `Tusk` |
| `DEFAULT_DOMAIN` | Domain the app runs on | `localhost:3000` |
| `LINK_LENGTH` | Length of generated addresses | `6` |
| `LINK_CUSTOM_ALPHABET` | Alphabet for generated addresses | `abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789` |
| `DISALLOW_REGISTRATION` | Disable sign-up (sign-up also needs `MAIL_ENABLED`) | `true` |
| `DISALLOW_LOGIN_FORM` | Hide email/password login (use with OIDC) | `false` |
| `DISALLOW_ANONYMOUS_LINKS` | Require login to shorten | `true` |
| `TRUST_PROXY` | Trust `X-Forwarded-For` from the proxy | `true` |
| `DB_CLIENT` | `pg`, `pg-native`, `mysql`, `mysql2`, `sqlite3`, `better-sqlite3` | `better-sqlite3` |
| `DB_FILENAME` | SQLite file | `db/data` |
| `DB_HOST` / `DB_PORT` / `DB_NAME` / `DB_USER` / `DB_PASSWORD` | Postgres/MySQL connection | `localhost` / `5432` / `tusk` / `postgres` / – |
| `DB_SSL` | TLS to the database | `false` |
| `DB_POOL_MIN` / `DB_POOL_MAX` | Connection pool bounds | `0` / `10` |
| `REDIS_ENABLED` | Use Redis (cache, and event stream in split mode) | `false` |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` / `REDIS_DB` | Redis connection | `127.0.0.1` / `6379` / – / `0` |
| `SERVER_IP_ADDRESS` | IP shown in the custom-domain instructions (display only) | – |
| `SERVER_CNAME_ADDRESS` | CNAME target shown in the custom-domain instructions (display only) | – |
| `CUSTOM_DOMAIN_USE_HTTPS` | Build `https://` short URLs for custom domains | `false` |
| `ENABLE_RATE_LIMIT` | Rate-limit auth and link-creation routes | `false` |
| `MAIL_ENABLED` | Emails for sign-up, verification, password reset and reports | `false` |
| `MAIL_HOST` / `MAIL_PORT` / `MAIL_SECURE` / `MAIL_USER` / `MAIL_PASSWORD` / `MAIL_FROM` | SMTP | – / `587` / `false` / – / – / – |
| `OIDC_ENABLED` | OpenID Connect login | `false` |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` / `OIDC_PROMPT` | OIDC provider | – |
| `OIDC_SCOPE` | Scopes | `openid profile email` |
| `OIDC_EMAIL_CLAIM` | Claim holding the email | `email` |
| `OIDC_BUTTON_TEXT` | Login button label | `Log in with OIDC` |
| `REPORT_EMAIL` | Receives abuse reports; enables `/report` | – |
| `CONTACT_EMAIL` | Support address shown in the footer | – |
| `NODE_ENV` / `TUSK_ENV` | `production` turns on production checks (for example, a required `JWT_SECRET`) | – |

### Tusk additions

| Variable | Description | Default |
|---|---|---|
| `GEO_PROVIDER` | Visitor country source: `auto` (trusted header, then `.mmdb`, then unknown), `cloudflare`, `geoip`, `none` | `auto` |
| `GEO_HEADER` | Country header name | `CF-IPCountry` |
| `GEOIP_DB_PATH` | MaxMind GeoLite2-Country / DB-IP lite `.mmdb` (required for `geoip`) | – |
| `GEO_TRUST_CLOUDFLARE_ONLY` | Honour `CF-IPCountry` / `CF-Connecting-IP` only from Cloudflare IP ranges | `true` |
| `CLOUDFLARE_IP_RANGES_URLS` | Where to refresh Cloudflare ranges (daily; an embedded list is the fallback) | `https://www.cloudflare.com/ips-v4,https://www.cloudflare.com/ips-v6` |
| `SERVICE_ROLES` | `all`, or a comma list of `api`, `redirector`, `worker`. Anything other than `all` requires `REDIS_ENABLED=true`. | `all` |
| `REDIRECTOR_PORT` | Listen port of `cmd/redirector` | `3001` |
| `METRICS_PORT` | Prometheus `/metrics` listener; `0` disables it | `9090` |
| `LOCAL_CACHE_SIZE` | In-process LRU entries (links + domains) | `100000` |
| `LOCAL_CACHE_TTL` | Upper bound on staleness across nodes after an edit | `30s` |
| `REDIS_CACHE_TTL` | Redis link-cache TTL | `15m` |
| `VISIT_BATCH_SIZE` | Visit events folded per DB flush | `500` |
| `VISIT_FLUSH_INTERVAL` | Max wait before a flush | `1s` |
| `VISIT_BUFFER_SIZE` | In-memory event buffer (single-binary mode); overflow is dropped and counted | `100000` |
| `EXPIRED_LINK_SWEEP` | Expired-link cleanup interval | `30s` |
| `ROUTING_ENABLED` | Platform routing ("universal links", [docs/UNIVERSAL-LINKS.md](docs/UNIVERSAL-LINKS.md)). `false`: routed links redirect to their target and the API rejects routing writes | `true` |
| `ROUTING_DEFAULT_MODES` | Routing modes users get unless an admin overrides them: any of `redirect,deeplink,smart`, or `none` (opt-in per user). Admins always get every mode | `redirect,deeplink,smart` |
| `ROUTING_SMART_TIMEOUT` | How long the smart page waits for the app before falling back (max 10s) | `1500ms` |
| `TUSK_CUSTOM_DIR` | Directory for `images/` and `css/` overrides | `custom` (image: `/tusk/custom`) |

Durations use Go syntax (`500ms`, `30s`, `15m`).

## Theming (`custom/`)

Files in `custom/images/` and `custom/css/` override the built-in ones. They are served at
`/images/*` and `/css/*`. Every `custom/css/*.css` file is linked from the UI. The UI uses shadcn/ui CSS
variables, so a theme can be a few lines:

```css
/* custom/css/theme.css */
:root { --primary: oklch(0.55 0.22 25); --radius: 0.25rem; }
.dark { --primary: oklch(0.7 0.19 25); }
```

Replace `custom/images/logo.png` and the favicons the same way.

## Web UI

`web/` is Vite + React 19 + TypeScript + Tailwind CSS v4 + shadcn/ui (default theme and components),
TanStack Query and React Router. It talks only to `/api/v2` with `credentials: "include"`, and every call
lives in [`web/src/lib/api.ts`](web/src/lib/api.ts). The admin, settings and stats pages are code-split;
Recharts only loads on the stats page. `npm run build` writes precompressed `.br`/`.gz` files, and
`internal/webui` serves them with immutable caching for hashed assets, `no-cache` for `index.html`,
`custom/` overrides and an SPA fallback.

## License

MIT. Tusk began as a Go port of [Kutt](https://github.com/thedevs-network/kutt) (MIT).
