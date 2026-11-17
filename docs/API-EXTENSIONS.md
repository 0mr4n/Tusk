# Tusk API extensions

Every upstream route (`server/routes/*.routes.js`) is mounted under both `/api/v2` and
`/api` (server.js:78-79) with upstream's validation messages, status codes and JSON
shapes. The endpoints and fields below are **Tusk additions**, needed because
Tusk's SPA replaces upstream's server-rendered/htmx pages. Existing upstream API clients
are unaffected.

| Method & path (`/api/v2` and `/api`) | Auth | Response | Replaces upstream |
|---|---|---|---|
| `GET /config` | none | `{site_name, default_domain, link_length, disallow_registration, disallow_anonymous_links, disallow_login_form, oidc_enabled, oidc_button_text, mail_enabled, report_email, contact_email, server_ip_address, server_cname_address, custom_domain_use_https, needs_admin_setup}` (`Cache-Control: no-store`) | `locals.config` / `res.locals` (locals.handler.js) |
| `POST /auth/logout` | none | 200 `{message:"Logged out."}`, clears the `token` cookie | `GET /logout` render (still served as `GET /logout` → 302 `/`) |
| `POST /auth/verify/{token}` | none | 200 `{token, message}` + cookie; 400 `{error:"Invalid or expired verification link."}` | `GET /verify/:token` render (auth.handler.js verify) |
| `POST /auth/verify-email/{token}` | none | 200 `{token, message}` + cookie; 400 `{error}` | `GET /verify-email/:token` render (auth.handler.js verifyChangeEmail) |
| `POST /links/{id}/protected` | none | JSON clients: `{target}` / `{error}`; HTML form posts: 302 to the target, or the protected page with the error | Upstream `POST /api/v2/links/:id/protected` (same path; the HTML branch renders Tusk's own template) |

## Extra fields and behaviour

- `GET /users` adds `id`, `role` (`"ADMIN"|"USER"`) and `verified` after upstream's `{apikey, email, domains}`.
- `POST /auth/login`, `/auth/create-admin` and the verify endpoints set the httpOnly `token` cookie for JSON clients as well (upstream: `utils.setToken`).
- `POST /users/delete` returns 200 `{message:"Account has been deleted."}` and clears the cookie. Upstream returns the text `OK`.
- `POST /links/report` returns 200 `{message}`. Upstream only renders a partial here.
- Unknown `/api/*` paths return 404 `{error:"Not found."}`. Upstream renders the HTML 404 page.
- `POST /users/admin` with `verification_email` issues a real verification token. Upstream sends a link containing `undefined` (users.handler.js create).
- `DELETE /domains/admin/{id}?links=false` keeps the links. Upstream's `if (links)` treats any non-empty string as true.
- `reuse:"false"` (string) counts as false. Upstream tests the raw value for truthiness.
- Mail text bodies use the correct sentences. Upstream's mail/text.js:1-11 swaps the verify and change-email texts.
- Every service answers `GET /api/v2/health` and `GET /api/health` with `OK`. Prometheus metrics are served on `METRICS_PORT` at `/metrics`.

## Platform routing (universal links)

Full design: [UNIVERSAL-LINKS.md](UNIVERSAL-LINKS.md). All routes exist under `/api/v2` and `/api`.

| Method & path | Auth | Notes |
|---|---|---|
| `POST /links`, `PATCH /links/{id}` | user | Optional `routing` object; `null` removes it (or, on create, skips the user's auto-applied defaults). A routing-only `PATCH` counts as a change. |
| `GET /links`, `GET /links/admin`, link responses, `GET /links/{id}/stats` | user | `routing` key appears only on links that have routing; other links are byte-identical to upstream. |
| `GET` / `PUT` / `PATCH` / `DELETE /links/{id}/routing` | owner / admin | `PATCH` merges: absent fields are kept, a rule set to `null` is removed. |
| `GET /links/{id}/routing/stats` | owner / admin | Upstream stats envelope with `stats: {route, os, device}` per period. |
| `GET` / `PUT /users/routing` | user | `{enabled, allowed_modes, defaults, auto_apply}`. |
| `GET` / `PATCH /users/admin/{id}/routing` | admin | `{allowed_modes: [...] \| null}`; revoking a mode turns routing off on that user's affected links (`disabled_links`). |
| `GET` / `PUT` / `DELETE /domains/{id}/apps` | owner / admin | App association; `{id}` is a domain uuid or `default` (admin). |
| `GET /.well-known/apple-app-site-association`, `/apple-app-site-association`, `/.well-known/assetlinks.json` | none | Per `Host`; 404 when not configured. |
| `GET /config` | none | Adds `routing_enabled`, `routing_smart_timeout_ms`. |

Routing object: `{"enabled": bool, "mode": "redirect"|"deeplink"|"smart", "rules": {"ios"|"android"|"desktop": {"enabled": bool, "url": "https://…", "app_url": "myapp://…"|null}}}`. Rule `url`s must be http(s) and pass the same checks as targets (length, banned hosts, not the default domain); `app_url` rejects `javascript:`, `data:`, `file:`, etc. Errors are upstream-style `{error}` with 400, 403 (`Routing mode not allowed.`) or 404.

`PATCH /links/admin/{id}` and `DELETE /domains/admin/{id}` are now served through a shared `{id}/{sub}` pattern (Go's ServeMux rejects the overlap with `/links/{id}/routing` and `/domains/{id}/apps`); behaviour is unchanged.
