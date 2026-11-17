# Universal links (platform routing): design plan

Status: **implemented** (all six phases). Differences from the original draft are listed in §12.

## 1. Goal

A short link can send visitors to a different destination depending on their platform, which is
detected from the `User-Agent` at redirect time. The feature can be controlled at four levels:
instance, user, link and rule. It is fully configurable through API v2, and it records its own
statistics.

v1 platform buckets: **`ios`**, **`android`**, **`desktop`**, plus the **fallback**, which is the
link's existing `target`. The classifier and the storage are built so that finer OS families
(`ipados`, `windows`, `macos`, `linux`, `chromeos`) can become rule keys later without a migration.

### Mode presets (chosen per link)

| Mode | What the visitor gets | Needs |
|---|---|---|
| `redirect` | A server-side 302 to the URL of the matching rule. | Nothing else. |
| `deeplink` | The same 302, but the short domain also serves `apple-app-site-association` and `assetlinks.json`, so an installed app opens the short URL directly (iOS Universal Links and Android App Links). Android rules can also go to an `intent://` URL with `browser_fallback_url`. | App association set on the domain (§6). |
| `smart` | A small interstitial HTML page. It tries `app_url` (a custom scheme), falls back to the store or web URL after a timeout, finds iPadOS by touch points, and shows an "open in browser" hint inside in-app webviews. A `<noscript>` meta-refresh goes to the fallback. | Nothing else. |

Rules for all modes:
- **Bots and crawlers** always get the plain fallback `target`, so link previews and unfurls don't change.
- **iPadOS 13+** reports a macOS User-Agent. `redirect` and `deeplink` treat it as `desktop`. `smart` corrects it on the client.
- **Password-protected links** apply routing *after* unlock, both for Basic auth and for `POST /links/{id}/protected`.
- **The info page (`/{id}+`)** keeps showing the fallback `target`.

## 2. Compatibility constraints this design respects

- **C1 / C6 (no Kutt schema changes):** every new piece of data is in **additive `tusk_*` tables** with their own migration ledger. Kutt's tables, rows and `knex_migrations` stay untouched. Node Kutt can still run on the same database. It ignores routing and redirects to `target`.
- **C5 (API shapes):** a link that has no routing serialises **byte-identically** to today. The `routing` key appears only when the link has routing configured.
- **C7 (cache):** `RedirectTarget` gains an `omitempty` field under Tusk's own Redis key prefix. Old cache entries decode as "no routing".
- **Hot path:** a link without routing gets one nil check and no extra allocation. Routing data travels inside the cached `RedirectTarget`, so it adds no extra cache or DB round trip.

## 3. Data model (new tables, all three dialects)

The migration ledger is `tusk_migrations(name PK, applied_at)`. `sqlstore.Migrate` first runs the
existing Kutt check, then applies the pending Tusk migrations while holding the **same** advisory
lock.

```
tusk_link_routing                   -- one row per link that has routing
  link_id     PK, FK links.id ON DELETE CASCADE (pg/mysql)
  enabled     bool                  -- per-link master toggle
  mode        varchar(16)           -- redirect | deeplink | smart
  rules       json/text             -- {"ios":{enabled,url,app_url}, "android":{...}, "desktop":{...}}
  created_at, updated_at

tusk_user_routing                   -- per-user controls (no row = instance defaults)
  user_id         PK, FK users.id ON DELETE CASCADE
  allowed_modes   varchar(64) NULL  -- admin-set CSV; NULL = ROUTING_DEFAULT_MODES; '' = no access
  defaults        json/text NULL    -- user's default routing object
  auto_apply      bool              -- apply defaults to new links that send no `routing`
  updated_at

tusk_domain_apps                    -- app association, per domain
  domain_id       PK (0 = DEFAULT_DOMAIN, set by an admin)
  ios_app_ids     text              -- "TEAMID.bundle.id", comma list
  android_package varchar(255)
  android_sha256  text              -- cert fingerprints, comma list
  paths           text              -- AASA components / path prefix; default "/*" minus preserved routes
  updated_at

tusk_route_visits                   -- stats, one row per (link, hour), like Kutt's visits
  id PK, link_id FK CASCADE, user_id NULL, hour timestamp, total int
  routes   json                     -- {"ios":n,"android":n,"desktop":n,"fallback":n}
  os       json                     -- {"ios","ipados","android","windows","macos","linux","chromeos","other"}
  device   json                     -- {"mobile","tablet","desktop"}
  UNIQUE(link_id, hour)
```

**Rules are a JSON column, not a child table.** The reason is the redirect hot path. A cache miss
stays **one indexed query** (`links LEFT JOIN tusk_link_routing`), and the API still validates the
JSON against a fixed schema. Each rule has its own `enabled` flag.

**Cleaning up orphans:** SQLite runs without `PRAGMA foreign_keys`, and Kutt can delete links
itself, so cascades are not enough. `LinkStore.Remove` and `RemoveExpired` therefore delete the
`tusk_*` rows explicitly, and the worker's sweeper also deletes orphaned `tusk_*` rows at a slow
interval.

## 4. Redirect hot path changes (`internal/redirect`, `internal/uaparse`, `internal/cache`)

1. `domain.RedirectTarget` gets `Routing *Routing \`json:"routing,omitempty"\``. It is filled by `sqlstore.Resolve` through the LEFT JOIN, and only when a routing row exists and `enabled` is true.
2. A new function `uaparse.Platform(ua) PlatformInfo{Route, OS, Device, InApp}` uses no regexp and makes no allocations, like the existing parser. It is **separate** from Kutt's `Browser`/`OS` buckets, which have to keep matching Kutt exactly for C6.
3. In `ServeRedirect`, after the banned, expired and protected checks:
   `if cfg.RoutingEnabled && t.Routing != nil && !isBot` → classify → choose the rule (if the rule is missing or disabled, use the fallback) → dispatch by mode:
   - `redirect` / `deeplink`: `redirect302(rule.URL)`. A `deeplink` Android rule that has `app_url` gets an `intent://…;S.browser_fallback_url=…;end`.
   - `smart`: render `templates/smart.html` using html/template, a CSP nonce, and `Cache-Control: no-store`.
4. The visit event carries `Route`, `PlatformOS` and `Device` (§7).
5. Mode permissions are checked **when the routing is written** (API), not on redirect. If an admin later revokes a mode, the API updates the stored routing rows, which invalidates the cache, so the redirector never checks permissions.
6. Benchmarks: the existing zero-alloc cache-hit benchmark has to stay unchanged for links without routing. A new benchmark covers a routed `redirect`-mode hit.

## 5. Controls

| Level | Mechanism |
|---|---|
| Instance | `ROUTING_ENABLED` (default `true`): when false, the redirector skips evaluation entirely and the API rejects writes with 400. `ROUTING_DEFAULT_MODES` (default `redirect,deeplink,smart`): the modes users get unless an admin overrides them. `ROUTING_SMART_TIMEOUT` (default `1500ms`). |
| User (admin) | `tusk_user_routing.allowed_modes`, set through the admin API and UI. Revoking a mode turns off (does not delete) that user's links that use it, and invalidates their cache entries. |
| User (self) | Default routing object + `auto_apply`. |
| Link | `routing.enabled` + `routing.mode`. |
| Rule | `rules.<platform>.enabled`. |

## 6. App association (`deeplink` mode)

- `GET /.well-known/apple-app-site-association` and `GET /.well-known/assetlinks.json` are served by the **redirector**, depending on `Host`. Domains that have no config get a 404. The response is cached together with the domain in `DomainResolver`.
- The AASA `components` include `paths` and always exclude Tusk's preserved routes (`/api`, `/login`, …).
- The configuration covers the **whole domain**: once an app claims `/*`, the OS hands it *every* link on that domain. The UI and docs recommend a dedicated custom domain for app links, and say that the app has to forward unknown links to the browser.
- Apple fetches the AASA through its CDN, so changes can take up to about 24 h to reach devices. The UI says so.
- Domain owners configure their own custom domains. Admins configure the default domain (`domain_id = 0`).

## 7. Statistics

- `VisitEvent` moves to **`V: 2`** with new optional fields `route`, `pos` (platform OS) and `device`. A v1 worker ignores the unknown JSON fields, and a v2 worker treats v1 events as unrouted, so rolling deploys are safe.
- `worker.Fold` produces a second kind of delta, `RouteDelta` per (link, hour), **only for events that went through routing**. `VisitStore.ApplyRouteDeltas` upserts `tusk_route_visits` using the same per-delta transaction pattern as Kutt's buckets. The existing `visits` rows and `links.visit_count` are written exactly as they are today.
- `GET /links/{id}/routing/stats` uses the shape of Kutt's stats (`lastDay`, `lastWeek`, `lastMonth`, `allTime`, each with `views[]`). Each period also has `routes[]`, `os[]` and `device[]` as `{name, value}`, and the result goes through the existing `statsCache`.

## 8. API v2 additions (documented in `docs/API-EXTENSIONS.md`)

The routing object:

```json
{
  "enabled": true,
  "mode": "redirect",
  "rules": {
    "ios":     { "enabled": true, "url": "https://apps.apple.com/app/id123", "app_url": "myapp://p/42" },
    "android": { "enabled": true, "url": "https://play.google.com/store/apps/details?id=x", "app_url": "myapp://p/42" },
    "desktop": { "enabled": true, "url": "https://example.com/p/42" }
  }
}
```

Fallback = the link's `target`. `url` goes through the same validation as `target` (http/https,
length, banned-host check). `app_url` accepts custom schemes but rejects `javascript:`, `data:`,
`file:`, `vbscript:` and `blob:`.

| Method & path | Auth | Notes |
|---|---|---|
| `POST /links`, `PATCH /links/{id}` | user | Optional `routing` (object, or `null` to remove). If it's left out on create and the user has `auto_apply`, the user's defaults are used. |
| `GET /links`, `GET /links/{id}` … | user | `routing` appears only on links that have it. |
| `GET` / `PUT` / `DELETE /links/{id}/routing` | user | Targeted control; `PUT` replaces the whole object. |
| `PATCH /links/{id}/routing` | user | Partial update, e.g. `{"enabled":false}` or `{"rules":{"ios":{"enabled":false}}}`. |
| `GET /links/{id}/routing/stats` | user | §7. |
| `GET` / `PUT /users/routing` | user | `{allowed_modes (read-only), defaults, auto_apply}`. |
| `GET` / `PATCH /users/admin/{id}/routing` | admin | Sets `allowed_modes`. |
| `GET` / `PUT` / `DELETE /domains/{id}/apps` | owner / admin | App association; `{id}` = `default` for DEFAULT_DOMAIN (admin only). |
| `GET /config` | none | Adds `routing_enabled`, `routing_smart_timeout_ms`. |

Errors use Kutt's `{error}` with status 400 or 403. A mode the user isn't allowed to use returns 403 `{error:"Routing mode not allowed."}`.

## 9. Web UI (`web/`)

- **Edit / create link dialog:** a "Platform routing" section with an on/off switch, a mode preset (segmented control with a one-line explanation for each), and a row per platform (iOS, Android, Desktop) with an enable toggle, URL and optional app URL. Fallback = target is shown read-only.
- **Links table:** a small routing badge showing the mode.
- **Stats page:** a "Platforms" tab with route hits over time and the OS and device breakdown.
- **Settings:** routing defaults + auto-apply. App association for the user's custom domains.
- **Admin:** allowed modes per user. App association for the default domain.

## 10. Delivery phases

Each phase can ship on its own and is gated by `make test`.

1. **Foundations:** the `tusk_migrations` ledger and the 4 tables (pg/mysql/sqlite DDL + tests on all three); `uaparse.Platform` with a UA corpus test (`testdata/`), including in-app browsers and iPadOS; domain types and config vars; record contract changes in `docs/CONTRACTS.md`.
2. **`redirect` mode end to end:** Resolve JOIN → cache → hot path; API inline field + `/links/{id}/routing`; validation; instance switch, per-user `allowed_modes`, per-rule flags; benchmarks; Kutt-compat test (a link without routing gives byte-identical JSON, and a Kutt schema plus `tusk_*` tables still passes `Migrate`).
3. **Statistics:** event v2, worker fold, `ApplyRouteDeltas`, stats endpoint, UI Platforms tab.
4. **`deeplink` mode:** `tusk_domain_apps`, `/.well-known/*` on the redirector, `intent://` building, domain settings UI.
5. **`smart` mode:** interstitial template (CSP, no-JS fallback, iPadOS and in-app handling), timeout config.
6. **User defaults + polish:** `/users/routing`, auto-apply, admin UI, README env table, API-EXTENSIONS.

## 11. Decisions to confirm

- **Who gets routing by default:** all users get `ROUTING_DEFAULT_MODES` unless an admin restricts them. The alternative is opt-in, where an admin grants each user.
- **Where finer platform stats come from:** recorded only for **routed** clicks. The alternative is recording OS and device for every link (still about 1 row update per link per hour).
- **`smart` page caching:** `no-store`, so every click is counted.

## 12. Implementation notes (deviations from the draft)

- **Route stats use fixed counter columns** (`r_*`, `os_*`, `dev_*`) instead of JSON maps, so the worker upserts with atomic increments (`ON CONFLICT … DO UPDATE` / `ON DUPLICATE KEY UPDATE`) and no read-modify-write. Adding a platform later needs a small `tusk_*` migration.
- **Rule URLs must be http(s)**, stricter than link targets (Kutt accepts any scheme). The smart page navigates from script, so it is also never rendered when the fallback target is not http(s); those visitors get a plain 302.
- **Admins always have every mode.** `ROUTING_DEFAULT_MODES=none` makes routing opt-in per user.
- **Rollout safety:** only the api role migrates. A redirector or worker that starts first keeps using the original Kutt query and re-probes for the `tusk_*` tables every 15 s.
- **Orphans** (rows left when Node Kutt deletes a link, user or domain) are removed by the worker every hour.
- **Smart page** is only rendered when it adds something: an app URL to try, an in-app browser to warn about, or a possible iPad (macOS UA with an iOS rule). Otherwise the visitor gets a 302.
- Finer platform stats are recorded for routed clicks only (decision §11, default kept).

