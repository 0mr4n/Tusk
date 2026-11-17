package redirect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/0mr4n/Tusk/internal/domain"
	"github.com/0mr4n/Tusk/internal/uaparse"
)

// Platform routing ("universal links", docs/UNIVERSAL-LINKS.md §1, §4, §6).

// AppsSource loads a domain's app association (store.RoutingStore satisfies it).
// domainID 0 = default domain. domain.ErrNotFound when not configured.
type AppsSource interface {
	GetDomainApps(ctx context.Context, domainID int64) (*domain.DomainApps, error)
}

// choice is the outcome of evaluating a link's routing for one request.
type choice struct {
	dest  string // where a plain 302 goes
	route string // stats key: ios|android|desktop|fallback
	rule  domain.RouteRule
	ok    bool // a platform rule matched
	pi    uaparse.PlatformInfo
}

func (h *Handler) routingActive(t *domain.RedirectTarget) bool {
	return h.o.RoutingEnabled && t.Routing != nil && t.Routing.Enabled
}

// choose picks the destination for ua; the link target is the fallback.
func choose(t *domain.RedirectTarget, ua string) choice {
	c := choice{dest: t.Target, route: domain.RouteFallback, pi: uaparse.Platform(ua)}
	if rule, ok := t.Routing.Rule(c.pi.Route); ok {
		c.dest, c.route, c.rule, c.ok = rule.URL, string(c.pi.Route), rule, true
	}
	return c
}

// serveRouted answers a human request for a routed link (not bots, not protected).
func (h *Handler) serveRouted(w http.ResponseWriter, r *http.Request, t *domain.RedirectTarget, ua string) {
	c := choose(t, ua)
	if t.UserID != nil {
		h.publishRouted(r, t.LinkID, t.UserID, ua, &c)
	}
	switch t.Routing.Mode {
	case domain.ModeSmart:
		if page, ok := h.smartData(t, &c); ok {
			h.count("routed_smart")
			h.pages.smart(w, page)
			return
		}
	case domain.ModeDeepLink:
		if c.ok && c.pi.Route == domain.PlatformAndroid {
			if intent, ok := IntentURL(c.rule.AppURL, c.rule.URL); ok {
				h.count("routed_intent")
				redirect302(w, intent)
				return
			}
		}
	}
	h.count("routed")
	redirect302(w, c.dest)
}

// routedTarget is the destination after a password unlock: the platform rule's
// URL (no interstitial, the visitor already interacted with a page).
func (h *Handler) routedTarget(t *domain.RedirectTarget, ua string) (string, *choice) {
	if t == nil || !h.routingActive(t) || h.o.IsBot(ua) {
		if t == nil {
			return "", nil
		}
		return t.Target, nil
	}
	c := choose(t, ua)
	return c.dest, &c
}

func (h *Handler) publishRouted(r *http.Request, linkID int64, userID *int64, ua string, c *choice) {
	h.publishEvent(r, linkID, userID, ua, c)
}

// ---------- deeplink: Android intent URLs ----------

// IntentURL converts a custom-scheme app URL ("myapp://path?q") into a Chrome
// intent URL that falls back to fallback when the app is not installed. http(s)
// app URLs are Android App Links and need no intent, so ok is false.
func IntentURL(appURL, fallback string) (string, bool) {
	u, err := url.Parse(appURL)
	if err != nil || !SafeAppURL(appURL) || u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "intent" {
		return "", false
	}
	rest := strings.TrimPrefix(appURL[len(u.Scheme)+1:], "//")
	var b strings.Builder
	b.WriteString("intent://")
	b.WriteString(rest)
	b.WriteString("#Intent;scheme=")
	b.WriteString(u.Scheme)
	b.WriteString(";")
	if fallback != "" {
		b.WriteString("S.browser_fallback_url=")
		b.WriteString(url.QueryEscape(fallback))
		b.WriteString(";")
	}
	b.WriteString("end")
	return b.String(), true
}

// SafeAppURL rejects schemes that would execute in the page or read local data.
// The API validates the same rule on write; this is defence in depth.
func SafeAppURL(s string) bool {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return false
	}
	scheme := strings.ToLower(s[:i])
	for j := 0; j < len(scheme); j++ {
		c := scheme[j]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			return false
		}
	}
	switch scheme {
	case "javascript", "vbscript", "data", "file", "blob", "about", "filesystem", "chrome", "view-source":
		return false
	}
	return !strings.ContainsAny(s, "\x00\r\n")
}

// ---------- smart interstitial ----------

type smartConfig struct {
	App     string     `json:"app,omitempty"`  // app URL to try (intent:// on Android)
	Web     string     `json:"web"`            // fallback after the timeout
	IPad    *smartPair `json:"ipad,omitempty"` // iOS rule, applied when a "Mac" has touch
	InApp   string     `json:"inapp,omitempty"`
	Timeout int64      `json:"timeout"` // ms
}

type smartPair struct {
	App string `json:"app,omitempty"`
	Web string `json:"web"`
}

type smartPage struct {
	SiteName string
	Nonce    string
	Config   smartConfig
	App      template.URL // validated by SafeAppURL
	Web      string
	InApp    string
}

// smartData decides whether the interstitial adds anything over a plain 302:
// an app URL to try, an in-app webview to warn about, or a possible iPad
// (iPadOS sends a macOS UA; only the page can check for touch).
func (h *Handler) smartData(t *domain.RedirectTarget, c *choice) (*smartPage, bool) {
	// The page navigates from script, so it only ever uses http(s) web URLs. A
	// fallback target with another scheme (Kutt allows any) gets a plain 302.
	if !isWebURL(c.dest) {
		return nil, false
	}
	cfg := smartConfig{Web: c.dest, Timeout: h.o.SmartTimeout.Milliseconds()}
	if c.ok && c.rule.AppURL != "" && SafeAppURL(c.rule.AppURL) {
		cfg.App = c.rule.AppURL
		if c.pi.Route == domain.PlatformAndroid {
			if intent, ok := IntentURL(c.rule.AppURL, c.rule.URL); ok {
				cfg.App = intent
			}
		}
	}
	if c.pi.OS == uaparse.OSMacOS {
		if ios, ok := t.Routing.Rule(domain.PlatformIOS); ok && isWebURL(ios.URL) {
			p := &smartPair{Web: ios.URL}
			if ios.AppURL != "" && SafeAppURL(ios.AppURL) {
				p.App = ios.AppURL
			}
			cfg.IPad = p
		}
	}
	if c.ok && c.pi.InApp != "" {
		cfg.InApp = c.pi.InApp
	}
	if cfg.App == "" && cfg.IPad == nil && cfg.InApp == "" {
		return nil, false
	}
	return &smartPage{Config: cfg, App: template.URL(cfg.App), Web: cfg.Web, InApp: cfg.InApp}, true
}

func isWebURL(s string) bool {
	if len(s) < 8 {
		return false
	}
	p := strings.ToLower(s[:8])
	return strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "http://")
}

func (p *pages) smart(w http.ResponseWriter, d *smartPage) {
	var nb [16]byte
	_, _ = rand.Read(nb[:])
	d.Nonce = hex.EncodeToString(nb[:]) // hex: no characters html/template would escape
	d.SiteName = p.siteName
	var b strings.Builder
	if err := p.t.ExecuteTemplate(&b, "smart.html", d); err != nil {
		redirect302(w, d.Web)
		return
	}
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+d.Nonce+"'; style-src 'unsafe-inline'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	hd.Set("Referrer-Policy", "no-referrer-when-downgrade")
	writeHTML(w, http.StatusOK, []byte(b.String()))
}

// ---------- deeplink: app association files ----------

// Association files change rarely and Apple/Google cache them for hours anyway.
// "Not configured" is cached briefly so a newly saved config shows up quickly on
// redirectors in other processes; in-process saves call InvalidateApps.
const (
	appsCacheTTL    = time.Minute
	appsNegativeTTL = 10 * time.Second
)

type appsEntry struct {
	apps *domain.DomainApps
	exp  time.Time
}

type appsCache struct {
	mu sync.Mutex
	m  map[int64]appsEntry
}

func (h *Handler) domainApps(ctx context.Context, domainID int64) (*domain.DomainApps, error) {
	now := h.o.Now()
	h.apps.mu.Lock()
	if e, ok := h.apps.m[domainID]; ok && now.Before(e.exp) {
		h.apps.mu.Unlock()
		return e.apps, nil
	}
	h.apps.mu.Unlock()
	a, err := h.o.Apps.GetDomainApps(ctx, domainID)
	if errors.Is(err, domain.ErrNotFound) {
		a, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.apps.mu.Lock()
	if h.apps.m == nil || len(h.apps.m) > 10_000 {
		h.apps.m = map[int64]appsEntry{}
	}
	ttl := appsCacheTTL
	if a == nil {
		ttl = appsNegativeTTL
	}
	h.apps.m[domainID] = appsEntry{apps: a, exp: now.Add(ttl)}
	h.apps.mu.Unlock()
	return a, nil
}

// InvalidateApps drops a cached association (domainID 0 = default domain).
func (h *Handler) InvalidateApps(domainID int64) {
	h.apps.mu.Lock()
	delete(h.apps.m, domainID)
	h.apps.mu.Unlock()
}

// requestApps resolves the Host to a domain and loads its association.
// Unknown hosts get nil (404), like any other path on an unknown host.
func (h *Handler) requestApps(r *http.Request) (*domain.DomainApps, error) {
	if h.o.Apps == nil {
		return nil, nil
	}
	host := RemoveWww(r.Host)
	var id int64
	if host != h.o.DefaultDomain {
		d, err := h.resolveDomain(r.Context(), r.Host)
		if err != nil || d == nil {
			return nil, err
		}
		id = d.ID
	}
	return h.domainApps(r.Context(), id)
}

func writeWellKnown(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// ServeAASA is GET /.well-known/apple-app-site-association (and the legacy
// /apple-app-site-association). Tusk's own routes are always excluded so the
// app is never handed /api, /login, ... URLs.
func (h *Handler) ServeAASA(w http.ResponseWriter, r *http.Request) {
	a, err := h.requestApps(r)
	if err != nil {
		h.serverError(w, r)
		return
	}
	if a == nil || len(a.IOSAppIDs) == 0 {
		h.ServeNotFound(w, r)
		return
	}
	writeWellKnown(w, AASA(a))
}

// AASA builds the apple-app-site-association document for a.
func AASA(a *domain.DomainApps) any {
	type component struct {
		Path    string `json:"/"`
		Exclude bool   `json:"exclude,omitempty"`
		Comment string `json:"comment,omitempty"`
	}
	var comps []component
	var legacy []string
	for _, p := range PreservedURLs {
		comps = append(comps, component{Path: "/" + p, Exclude: true}, component{Path: "/" + p + "/*", Exclude: true})
		legacy = append(legacy, "NOT /"+p, "NOT /"+p+"/*")
	}
	// Link info pages ("/abc+") stay in the browser.
	comps = append(comps, component{Path: "/*+", Exclude: true})
	legacy = append(legacy, "NOT /*+")
	paths := a.Paths
	if len(paths) == 0 {
		paths = []string{"*"}
	}
	for _, p := range paths {
		comps = append(comps, component{Path: p})
		legacy = append(legacy, p)
	}
	type detail struct {
		AppIDs     []string    `json:"appIDs"`
		Components []component `json:"components"`
		AppID      string      `json:"appID,omitempty"` // iOS 12 and earlier
		Paths      []string    `json:"paths,omitempty"`
	}
	var details []detail
	for _, id := range a.IOSAppIDs {
		details = append(details, detail{AppIDs: []string{id}, Components: comps, AppID: id, Paths: legacy})
	}
	return map[string]any{
		"applinks": map[string]any{"apps": []string{}, "details": details},
	}
}

// ServeAssetLinks is GET /.well-known/assetlinks.json (Android App Links).
// Path filtering for Android lives in the app's intent filters.
func (h *Handler) ServeAssetLinks(w http.ResponseWriter, r *http.Request) {
	a, err := h.requestApps(r)
	if err != nil {
		h.serverError(w, r)
		return
	}
	if a == nil || a.AndroidPackage == "" || len(a.AndroidSHA256) == 0 {
		h.ServeNotFound(w, r)
		return
	}
	writeWellKnown(w, AssetLinks(a))
}

// AssetLinks builds the Digital Asset Links statement list for a.
func AssetLinks(a *domain.DomainApps) any {
	return []map[string]any{{
		"relation": []string{"delegate_permission/common.handle_all_urls"},
		"target": map[string]any{
			"namespace":                "android_app",
			"package_name":             a.AndroidPackage,
			"sha256_cert_fingerprints": a.AndroidSHA256,
		},
	}}
}

// WellKnownPaths lists the association routes to mount (all roles serving
// redirects, on every host).
var WellKnownPaths = []string{
	"/.well-known/apple-app-site-association",
	"/apple-app-site-association",
	"/.well-known/assetlinks.json",
}

// RegisterWellKnown mounts the association routes on mux.
func (h *Handler) RegisterWellKnown(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/apple-app-site-association", h.ServeAASA)
	mux.HandleFunc("GET /apple-app-site-association", h.ServeAASA)
	mux.HandleFunc("GET /.well-known/assetlinks.json", h.ServeAssetLinks)
}
