package redirect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/0mr4n/Tusk/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

const (
	iphoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
	androidUA = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36"
	windowsUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	macUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
	instaUA   = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 307.0.0.34.111"
	kaiUA     = "Mozilla/5.0 (Mobile; Nokia_8110_4G; rv:48.0) Gecko/48.0 Firefox/48.0 KAIOS/2.5"
)

func rules() map[domain.Platform]domain.RouteRule {
	return map[domain.Platform]domain.RouteRule{
		domain.PlatformIOS:     {Enabled: true, URL: "https://apps.apple.com/app/id1", AppURL: "myapp://item/42?ref=x"},
		domain.PlatformAndroid: {Enabled: true, URL: "https://play.google.com/store/apps/details?id=com.x", AppURL: "myapp://item/42?ref=x"},
		domain.PlatformDesktop: {Enabled: true, URL: "https://www.example.com/item/42"},
	}
}

// setupRouting adds routed links to the standard fixture.
func setupRouting(t testing.TB) (*Handler, *fakePub, *http.ServeMux) {
	h, pub, mux := setup(t)
	h.o.RoutingEnabled = true
	m := h.o.Links.(*fakeLinks).m
	m[key{addr: "rd"}] = &domain.RedirectTarget{LinkID: 20, UUID: "u20", UserID: ptr[int64](7), Target: "https://fallback.example/",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeRedirect, Rules: rules()}}
	partial := rules()
	delete(partial, domain.PlatformDesktop)
	ar := partial[domain.PlatformAndroid]
	ar.Enabled = false
	partial[domain.PlatformAndroid] = ar
	m[key{addr: "part"}] = &domain.RedirectTarget{LinkID: 21, UUID: "u21", UserID: ptr[int64](7), Target: "https://fallback.example/",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeRedirect, Rules: partial}}
	m[key{addr: "dl"}] = &domain.RedirectTarget{LinkID: 22, UUID: "u22", UserID: ptr[int64](7), Target: "https://fallback.example/",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeDeepLink, Rules: rules()}}
	m[key{addr: "sm"}] = &domain.RedirectTarget{LinkID: 23, UUID: "u23", UserID: ptr[int64](7), Target: "https://fallback.example/",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeSmart, Rules: rules()}}
	m[key{addr: "anonrd"}] = &domain.RedirectTarget{LinkID: 24, UUID: "u24", Target: "https://fallback.example/",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeRedirect, Rules: rules()}}
	return h, pub, mux
}

func TestRoutedRedirect(t *testing.T) {
	_, pub, mux := setupRouting(t)
	cases := []struct {
		name, path, ua, loc, route, os, dev string
	}{
		{"ios", "/rd", iphoneUA, "https://apps.apple.com/app/id1", "ios", "ios", "mobile"},
		{"android", "/rd", androidUA, "https://play.google.com/store/apps/details?id=com.x", "android", "android", "mobile"},
		{"desktop", "/rd", windowsUA, "https://www.example.com/item/42", "desktop", "windows", "desktop"},
		{"ipados as desktop", "/rd", macUA, "https://www.example.com/item/42", "desktop", "macos", "desktop"},
		{"unknown os falls back", "/rd", kaiUA, "https://fallback.example/", "fallback", "other", "mobile"},
		{"missing rule falls back", "/part", windowsUA, "https://fallback.example/", "fallback", "windows", "desktop"},
		{"disabled rule falls back", "/part", androidUA, "https://fallback.example/", "fallback", "android", "mobile"},
		{"enabled rule in partial", "/part", iphoneUA, "https://apps.apple.com/app/id1", "ios", "ios", "mobile"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub.evs = nil
			rr := do(t, mux, "GET", "tusk.test", c.path, map[string]string{"User-Agent": c.ua}, "")
			if rr.Code != http.StatusFound || rr.Header().Get("Location") != c.loc {
				t.Fatalf("got %d %q, want 302 %q", rr.Code, rr.Header().Get("Location"), c.loc)
			}
			if len(pub.evs) != 1 {
				t.Fatalf("events = %d", len(pub.evs))
			}
			ev := pub.evs[0]
			if ev.V != 2 || ev.Route != c.route || ev.PlatformOS != c.os || ev.Device != c.dev {
				t.Fatalf("event %+v", ev)
			}
			if ev.Browser == "" || ev.OS == "" {
				t.Fatalf("legacy fields missing: %+v", ev)
			}
		})
	}
}

func TestRoutedBotsAndSwitch(t *testing.T) {
	h, pub, mux := setupRouting(t)
	rr := do(t, mux, "GET", "tusk.test", "/rd", map[string]string{"User-Agent": "Twitterbot/1.0"}, "")
	if rr.Header().Get("Location") != "https://fallback.example/" || len(pub.evs) != 0 {
		t.Fatalf("bot: %q events=%d", rr.Header().Get("Location"), len(pub.evs))
	}
	// Anonymous routed links route but record nothing (no owner, like upstream).
	rr = do(t, mux, "GET", "tusk.test", "/anonrd", map[string]string{"User-Agent": iphoneUA}, "")
	if rr.Header().Get("Location") != "https://apps.apple.com/app/id1" || len(pub.evs) != 0 {
		t.Fatalf("anon: %q events=%d", rr.Header().Get("Location"), len(pub.evs))
	}
	// ROUTING_ENABLED=false: exactly the upstream behaviour.
	h.o.RoutingEnabled = false
	rr = do(t, mux, "GET", "tusk.test", "/rd", map[string]string{"User-Agent": iphoneUA}, "")
	if rr.Header().Get("Location") != "https://fallback.example/" || len(pub.evs) != 1 || pub.evs[0].V != 1 || pub.evs[0].Route != "" {
		t.Fatalf("switch off: %q %+v", rr.Header().Get("Location"), pub.evs)
	}
}

func TestDeepLinkIntent(t *testing.T) {
	_, _, mux := setupRouting(t)
	rr := do(t, mux, "GET", "tusk.test", "/dl", map[string]string{"User-Agent": androidUA}, "")
	want := "intent://item/42?ref=x#Intent;scheme=myapp;S.browser_fallback_url=" +
		url.QueryEscape("https://play.google.com/store/apps/details?id=com.x") + ";end"
	if got, _ := url.PathUnescape(rr.Header().Get("Location")); rr.Code != 302 || got != mustUnescape(want) {
		t.Fatalf("intent:\n got  %s\n want %s", rr.Header().Get("Location"), want)
	}
	// iOS: universal links are handled by the OS before the request; the
	// browser that does reach us goes to the store.
	rr = do(t, mux, "GET", "tusk.test", "/dl", map[string]string{"User-Agent": iphoneUA}, "")
	if rr.Header().Get("Location") != "https://apps.apple.com/app/id1" {
		t.Fatalf("ios deeplink: %q", rr.Header().Get("Location"))
	}
}

func mustUnescape(s string) string { u, _ := url.PathUnescape(s); return u }

func TestSmartPage(t *testing.T) {
	_, pub, mux := setupRouting(t)
	rr := do(t, mux, "GET", "tusk.test", "/sm", map[string]string{"User-Agent": iphoneUA}, "")
	body := rr.Body.String()
	if rr.Code != 200 || !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("smart: %d %s", rr.Code, rr.Header().Get("Content-Type"))
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("csp %q", csp)
	}
	nonce := csp[strings.Index(csp, "nonce-")+6 : strings.Index(csp, "'; style")]
	if !strings.Contains(body, `<script nonce="`+nonce+`">`) {
		t.Fatal("script nonce mismatch")
	}
	for _, s := range []string{`href="myapp://item/42?ref=x"`, `href="https://apps.apple.com/app/id1"`, `"timeout":1500`, `url=https://apps.apple.com/app/id1`} {
		if !strings.Contains(body, s) {
			t.Fatalf("smart body lacks %s:\n%s", s, body)
		}
	}
	if rr.Header().Get("Cache-Control") != "no-store" || len(pub.evs) != 1 || pub.evs[0].Route != "ios" {
		t.Fatalf("cache/events: %q %+v", rr.Header().Get("Cache-Control"), pub.evs)
	}
	// Android gets the intent URL as the app link.
	rr = do(t, mux, "GET", "tusk.test", "/sm", map[string]string{"User-Agent": androidUA}, "")
	if !strings.Contains(rr.Body.String(), `href="intent://item/42?ref=x#Intent;scheme=myapp;`) {
		t.Fatalf("android smart:\n%s", rr.Body.String())
	}
	// Desktop without app URL: no interstitial.
	rr = do(t, mux, "GET", "tusk.test", "/sm", map[string]string{"User-Agent": windowsUA}, "")
	if rr.Code != 302 || rr.Header().Get("Location") != "https://www.example.com/item/42" {
		t.Fatalf("desktop smart: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	// A Mac may be an iPad: page with the iOS rule for touch devices.
	rr = do(t, mux, "GET", "tusk.test", "/sm", map[string]string{"User-Agent": macUA}, "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"ipad":{"app":"myapp://item/42?ref=x","web":"https://apps.apple.com/app/id1"}`) {
		t.Fatalf("mac smart: %d\n%s", rr.Code, rr.Body.String())
	}
	// In-app browser: hint, no auto navigation flag.
	rr = do(t, mux, "GET", "tusk.test", "/sm", map[string]string{"User-Agent": instaUA}, "")
	if !strings.Contains(rr.Body.String(), "Open in browser") || !strings.Contains(rr.Body.String(), `"inapp":"instagram"`) {
		t.Fatalf("inapp smart:\n%s", rr.Body.String())
	}
}

func TestSmartPageEscaping(t *testing.T) {
	h, _, mux := setupRouting(t)
	evil := rules()
	evil[domain.PlatformIOS] = domain.RouteRule{Enabled: true, URL: `https://x.example/"><script>alert(1)</script>`, AppURL: "javascript:alert(1)"}
	h.o.Links.(*fakeLinks).m[key{addr: "evil"}] = &domain.RedirectTarget{LinkID: 30, UUID: "u30", Target: "https://t.example/",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeSmart, Rules: evil}}
	rr := do(t, mux, "GET", "tusk.test", "/evil", map[string]string{"User-Agent": macUA}, "")
	body := rr.Body.String()
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "javascript:alert") {
		t.Fatalf("unescaped content:\n%s", body)
	}
	// Upstream accepts any target scheme; the smart page must never script-navigate to one.
	h.o.Links.(*fakeLinks).m[key{addr: "jsfb"}] = &domain.RedirectTarget{LinkID: 32, UUID: "u32", Target: "javascript:alert(document.domain)",
		Routing: &domain.Routing{Enabled: true, Mode: domain.ModeSmart, Rules: map[domain.Platform]domain.RouteRule{
			domain.PlatformIOS: {Enabled: true, URL: "https://apps.apple.com/app/id1", AppURL: "myapp://x"}}}}
	for _, ua := range []string{androidUA, macUA, kaiUA} {
		rr = do(t, mux, "GET", "tusk.test", "/jsfb", map[string]string{"User-Agent": ua}, "")
		if rr.Code != http.StatusFound || strings.Contains(rr.Body.String(), "<script") {
			t.Fatalf("%s: non-web fallback rendered the smart page: %d", ua, rr.Code)
		}
	}
}

func TestProtectedRouted(t *testing.T) {
	h, pub, mux := setupRouting(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), 4)
	const id = "33333333-3333-3333-3333-333333333333"
	h.o.Links.(*fakeLinks).m[key{addr: "pwr"}] = &domain.RedirectTarget{LinkID: 31, UUID: id, UserID: ptr[int64](7), Target: "https://fallback.example/",
		HasPassword: true, Routing: &domain.Routing{Enabled: true, Mode: domain.ModeSmart, Rules: rules()}}
	h.o.Finder.(fakeFinder)[id] = &domain.Link{ID: 31, UUID: id, Address: "pwr", Target: "https://fallback.example/", UserID: ptr[int64](7), Password: ptr(string(hash))}
	rr := do(t, mux, "POST", "tusk.test", "/api/v2/links/"+id+"/protected",
		map[string]string{"Content-Type": "application/json", "User-Agent": androidUA}, `{"password":"secret"}`)
	var out struct{ Target string }
	json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != 200 || out.Target != "https://play.google.com/store/apps/details?id=com.x" {
		t.Fatalf("protected routed: %d %s", rr.Code, rr.Body.String())
	}
	if len(pub.evs) != 1 || pub.evs[0].Route != "android" {
		t.Fatalf("events %+v", pub.evs)
	}
}

type fakeApps map[int64]*domain.DomainApps

func (f fakeApps) GetDomainApps(_ context.Context, id int64) (*domain.DomainApps, error) {
	if a, ok := f[id]; ok {
		return a, nil
	}
	return nil, domain.ErrNotFound
}

func TestWellKnown(t *testing.T) {
	h, _, _ := setupRouting(t)
	h.o.Apps = fakeApps{
		0:  {IOSAppIDs: []string{"ABCDE12345.com.x"}, AndroidPackage: "com.x", AndroidSHA256: []string{"AA:BB"}},
		10: {IOSAppIDs: []string{"ABCDE12345.com.y"}, Paths: []string{"/p/*"}},
	}
	mux := http.NewServeMux()
	h.RegisterWellKnown(mux)
	rr := do(t, mux, "GET", "tusk.test", "/.well-known/apple-app-site-association", nil, "")
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("aasa: %d %s", rr.Code, rr.Header().Get("Content-Type"))
	}
	var aasa struct {
		Applinks struct {
			Details []struct {
				AppIDs     []string
				Components []map[string]any
			}
		}
	}
	json.Unmarshal(rr.Body.Bytes(), &aasa)
	d := aasa.Applinks.Details
	if len(d) != 1 || d[0].AppIDs[0] != "ABCDE12345.com.x" {
		t.Fatalf("aasa details %s", rr.Body.String())
	}
	comps := d[0].Components
	if comps[0]["/"] != "/login" || comps[0]["exclude"] != true || comps[len(comps)-1]["/"] != "*" {
		t.Fatalf("components %v", comps)
	}
	if !strings.Contains(rr.Body.String(), `{"/":"/api/*","exclude":true}`) {
		t.Fatal("api not excluded")
	}
	rr = do(t, mux, "GET", "go.custom.io", "/.well-known/apple-app-site-association", nil, "")
	if !strings.Contains(rr.Body.String(), `{"/":"/p/*"}`) || strings.Contains(rr.Body.String(), `{"/":"*"}`) {
		t.Fatalf("custom domain aasa %s", rr.Body.String())
	}
	rr = do(t, mux, "GET", "tusk.test", "/.well-known/assetlinks.json", nil, "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"package_name":"com.x"`) || !strings.Contains(rr.Body.String(), `"sha256_cert_fingerprints":["AA:BB"]`) {
		t.Fatalf("assetlinks %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range []struct{ host, path string }{
		{"go.custom.io", "/.well-known/assetlinks.json"},              // no android config
		{"bare.custom.io", "/.well-known/apple-app-site-association"}, // no config at all
		{"unknown.example", "/.well-known/apple-app-site-association"},
	} {
		if rr := do(t, mux, "GET", c.host, c.path, nil, ""); rr.Code != 404 {
			t.Fatalf("%s%s: %d", c.host, c.path, rr.Code)
		}
	}
}

func TestIntentURLAndSafeAppURL(t *testing.T) {
	if _, ok := IntentURL("https://x.example/a", "https://f"); ok {
		t.Fatal("https app url must not become an intent")
	}
	if got, _ := IntentURL("fb://profile/1", ""); got != "intent://profile/1#Intent;scheme=fb;end" {
		t.Fatalf("intent %q", got)
	}
	for _, s := range []string{"javascript:alert(1)", "JavaScript:x", "data:text/html,x", "file:///etc/passwd", "vbscript:x", "noscheme", ":x", "my app://x", "a://b\nc"} {
		if SafeAppURL(s) {
			t.Fatalf("SafeAppURL(%q) = true", s)
		}
	}
	for _, s := range []string{"myapp://x", "fb://profile", "com.example.app:/path", "https://x"} {
		if !SafeAppURL(s) {
			t.Fatalf("SafeAppURL(%q) = false", s)
		}
	}
}

func BenchmarkRedirectRouted(b *testing.B) {
	h, _, _ := setupRouting(b)
	h.o.Metrics = nil
	h.o.Publisher = discardPub{}
	req := httptest.NewRequest("GET", "/rd", nil)
	req.Host = "tusk.test"
	req.Header.Set("User-Agent", iphoneUA)
	req.SetPathValue("id", "rd")
	w := &nopWriter{h: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		clear(w.h)
		h.ServeRedirect(w, req)
	}
}
