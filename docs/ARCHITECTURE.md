# Tusk — Architecture (Go 1.27)

Tusk must run against an existing upstream-schema database with no schema changes,
keep API v2 byte-compatible, and keep existing passwords, JWT cookies and API keys valid.

## 1. Services (one module, three binaries, one "all-in-one" mode)

    cmd/tusk        all roles in one process (default; SERVICE_ROLES=all).
    cmd/redirector  hot path only: GET /{id}, custom-domain homepage redirect, /api/v2/health.
    cmd/api         JSON API v2 + auth + admin + serves the React (shadcn) SPA.
    cmd/worker      consumes visit events, folds them into hourly buckets, expired-link sweeper.

    cmd/tusk is main() wiring the same packages with every role enabled. The split binaries
    are the SAME packages with a subset of roles. SERVICE_ROLES=api,worker etc. also works on cmd/tusk.

Why split: redirect traffic is ~99% of load, is read-only and cache-friendly, and must
never be slowed by analytics writes or admin queries. Each role scales independently.

    Internet → Cloudflare → Traefik ─┬─ Host(short.domain) && Path(/{id}) → redirector ×N
                                     ├─ /api/*, /, /assets/* ─────────────→ api ×M
                                     └─ (none)                              worker ×K (no ingress)
    redirector ──XADD visits──▶ Redis Stream "tusk:visits" ──XREADGROUP──▶ worker
    api ──PUBLISH tusk:inval──▶ redirector (drop local LRU entry on edit/delete/ban)

Single-binary mode replaces the stream with an in-process buffered channel and pub/sub
with a direct call — no Redis required, same as upstream with REDIS_ENABLED=false.

## 2. Redirect hot path (performance budget: p99 < 5 ms at the origin, cache hit)

1. Resolve Host → custom domain (DomainResolver, LRU-cached; default domain = nil).
2. Resolve (address, domain_id) → RedirectTarget through:
   local LRU (LOCAL_CACHE_TTL 30s) → Redis (REDIS_CACHE_TTL 15m, key compatible with upstream) →
   DB single indexed query. singleflight collapses concurrent misses for the same key.
   Negative results cached 5s to blunt enumeration floods.
3. Banned → 302 /banned. Expired → 404 (and enqueue delete). Password → render protected page.
4. isbot(User-Agent) check; if human and link has user_id → VisitPublisher.Publish (non-blocking,
   drops + counts on full buffer). Geo/UA parsing happens HERE (cheap) so events are self-contained.
5. 302 to target. Upstream uses 302 (res.redirect) — keep it; 301 would break stats and edits.

No DB write on the request path. Zero allocations target for the cache-hit path.

## 3. Visit pipeline (write amplification killer)

Upstream: every click = 1 transaction with SELECT … FOR UPDATE on the hour bucket + 1 UPDATE on links.
Tusk worker: pull up to VISIT_BATCH_SIZE events or wait VISIT_FLUSH_INTERVAL, fold them in memory
into VisitDelta per (link_id, hour), then one short transaction per delta. 10k clicks/s on one
hot link becomes ~1 row update/s. Output rows are identical in shape to upstream's, so the Node app
and the stats page read them unchanged. At-least-once delivery: XACK only after commit.

## 4. Geo (configurable)

GEO_PROVIDER = auto | cloudflare | geoip | none   (default auto)
  cloudflare  country from GEO_HEADER (default CF-IPCountry). "XX"/"T1"/empty → unknown.
  geoip       country from GEOIP_DB_PATH (.mmdb: MaxMind GeoLite2-Country or DB-IP lite).
  auto        header if present and trusted, else mmdb if GEOIP_DB_PATH set, else unknown.
  none        feature off; every visit stored as "unknown".
GEO_TRUST_CLOUDFLARE_ONLY=true: CF-IPCountry and CF-Connecting-IP are honoured only when the
TCP peer (or the first untrusted hop when TRUST_PROXY) is inside Cloudflare's IP ranges
(embedded list, refreshed daily from CLOUDFLARE_IP_RANGES_URLS). Prevents spoofed stats and
spoofed client IPs for rate limiting.

## 5. Frontend

React 19 + Vite + TypeScript + Tailwind v4 + shadcn/ui default components (web/).
Built to web/dist and embedded into the api binary via embed.FS; files in TUSK_CUSTOM_DIR
(custom/) override embedded ones (keeps upstream's custom/ theming for css/images).
The SPA talks only to /api/v2 with the JWT "token" cookie (httpOnly, SameSite=Lax) —
the same cookie upstream sets, so a user logged into upstream is logged into Tusk and vice versa.
Server-rendered fallbacks stay for pages that must work without JS: password-protected link
form, /banned, /404 — rendered with html/template by the redirector.

Deliberate break from upstream: custom/views/*.hbs overrides no longer apply (UI is now React).

## 6. Package layout and OWNERSHIP (build agents must stay in their lane)

    internal/config    SHARED (orchestrator). Env loading, upstream-compatible.
    internal/domain    SHARED (orchestrator). Entities + cross-service interfaces. stdlib only.
    internal/store     SHARED interface (orchestrator); internal/store/sqlstore → AGENT A
    internal/cache     AGENT A  LRU + Redis + singleflight + pub/sub invalidation; LinkResolver/DomainResolver impls
    internal/events    AGENT A  VisitPublisher/VisitConsumer: channel impl + Redis Streams impl
    internal/worker    AGENT A  batch folder → VisitStore.ApplyDeltas; expired sweeper
    internal/geo       AGENT A  GeoResolver impls + Cloudflare IP ranges + client-IP extraction
    internal/uaparse   AGENT A  upstream browser/os buckets (utils.js getUseragentBrowser/OS) + isbot
    internal/redirect  AGENT B  redirector HTTP handler (hot path) + protected/banned/404 pages
    internal/auth      AGENT B  bcrypt, JWT (HS256, iss ApiAuth, sub=id, 7d), API key, OIDC, middleware
    internal/api       AGENT B  /api/v2 routes, validation, rate limits, JSON shapes, admin
    internal/mail      AGENT B  SMTP + templates
    internal/httpx     AGENT B  middleware: real IP, security headers, logging, recover, metrics
    cmd/*              AGENT B  wiring (after A's packages exist; use constructors from A)
    web/               AGENT C  React + shadcn SPA
    internal/webui     AGENT C  embed.FS + custom/ override file server + SPA fallback
    deploy/, Dockerfile, docker-compose*.yml, .github/workflows  AGENT C

## 7. Compatibility requirements (acceptance gates)

C1  Runs on an existing upstream DB (pg, mysql, sqlite) with no migration beyond verifying knex_migrations.
C2  bcryptjs $2a$ hashes verify; new hashes cost 12.
C3  JWT cookie "token" from upstream validates in Tusk and vice versa (same JWT_SECRET).
C4  X-API-KEY auth works with existing keys.
C5  API v2 JSON field names/shapes identical (sanitize.link: id=uuid, password=bool, link=short URL…).
C6  visits rows written by Tusk are readable by upstream's stats code and vice versa.
C7  Redis cache keys compatible or namespaced so both can run side by side during cut-over.
