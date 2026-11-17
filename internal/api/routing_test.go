package api

import (
	"strings"
	"testing"

	"github.com/0mr4n/Tusk/internal/config"
	"github.com/0mr4n/Tusk/internal/domain"
)

func sampleRoutingBody() map[string]any {
	return map[string]any{
		"mode": "redirect",
		"rules": map[string]any{
			"ios":     map[string]any{"url": "https://apps.apple.com/app/id1", "app_url": "myapp://x"},
			"android": map[string]any{"url": "play.google.com/store/apps/details?id=x"},
			"desktop": map[string]any{"enabled": false, "url": "https://example.com/d"},
		},
	}
}

func TestCreateLinkWithRouting(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com", "routing": sampleRoutingBody()})
	if r.code != 201 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	want := `"routing":{"enabled":true,"mode":"redirect","rules":{"ios":{"enabled":true,"url":"https://apps.apple.com/app/id1","app_url":"myapp://x"},"android":{"enabled":true,"url":"http://play.google.com/store/apps/details?id=x","app_url":null},"desktop":{"enabled":false,"url":"https://example.com/d","app_url":null}}}}`
	if !strings.HasSuffix(r.body, want) {
		t.Fatalf("create body:\n%s", r.body)
	}
	addr := r.json(t)["address"].(string)
	rt, err := e.res.Resolve(t.Context(), addr, nil)
	if err != nil || rt.Routing == nil || rt.Routing.Rules[domain.PlatformIOS].AppURL != "myapp://x" {
		t.Fatalf("resolver sees %+v %v", rt, err)
	}
	// Listed with routing; a link without routing has no "routing" key at all.
	e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://plain.example"})
	list := e.do("GET", "/api/v2/links", "user", nil).body
	if strings.Count(list, `"routing":`) != 1 {
		t.Fatalf("list: %s", list)
	}
}

func TestPlainLinkJSONUnchanged(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com"})
	if r.code != 201 || strings.Contains(r.body, "routing") {
		t.Fatalf("%d %s", r.code, r.body)
	}
	id := r.json(t)["id"].(string)
	r = e.do("PATCH", "/api/v2/links/"+id, "user", map[string]any{"target": "https://example.org"})
	if r.code != 200 || strings.Contains(r.body, "routing") {
		t.Fatalf("edit %d %s", r.code, r.body)
	}
}

func TestRoutingValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		routing any
		code    int
		msg     string
	}{
		{"x", 400, msgRoutingObject},
		{map[string]any{"mode": "magic"}, 400, msgRoutingMode},
		{map[string]any{}, 400, msgRoutingMode},
		{map[string]any{"mode": "redirect", "enabled": "yes"}, 400, msgRoutingEnabled},
		{map[string]any{"mode": "redirect", "rules": []any{}}, 400, msgRoutingRules},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"windows": map[string]any{"url": "https://x.com"}}}, 400, `Unknown routing platform "windows". Use ios, android or desktop.`},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"ios": "https://x.com"}}, 400, msgRuleObject},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"ios": map[string]any{}}}, 400, msgRuleURL},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"ios": map[string]any{"url": "https://kutt.test/x"}}}, 400, msgRuleURL},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"ios": map[string]any{"url": "javascript:alert(1)"}}}, 400, msgRuleURL},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"ios": map[string]any{"url": "tel:+123"}}}, 400, msgRuleURL},
		{map[string]any{"mode": "redirect", "rules": map[string]any{"ios": map[string]any{"url": "//x.com"}}}, 400, msgRuleURL},
		{map[string]any{"mode": "smart", "rules": map[string]any{"ios": map[string]any{"url": "https://x.com", "app_url": "javascript:alert(1)"}}}, 400, msgRuleAppURL},
		{map[string]any{"mode": "smart", "rules": map[string]any{"ios": map[string]any{"url": "https://x.com", "enabled": 1}}}, 400, msgRuleEnabled},
	}
	for _, c := range cases {
		r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com", "routing": c.routing})
		if r.code != c.code || r.json(t)["error"] != c.msg {
			t.Errorf("routing %v: %d %s, want %d %q", c.routing, r.code, r.body, c.code, c.msg)
		}
	}
	// Banned hosts are rejected in rules like in targets.
	e.st.SeedDomain(domain.Domain{Address: "evil.example", Banned: true})
	r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com",
		"routing": map[string]any{"mode": "redirect", "rules": map[string]any{"ios": map[string]any{"url": "https://evil.example/x"}}}})
	if r.code != 400 {
		t.Fatalf("banned rule url: %d %s", r.code, r.body)
	}
	// Anonymous users can't set routing.
	r = e.do("POST", "/api/v2/links", "", map[string]any{"target": "https://example.com", "routing": sampleRoutingBody()})
	if r.code != 400 || r.json(t)["error"] != "Only users can use this field." {
		t.Fatalf("anon: %d %s", r.code, r.body)
	}
}

func TestRoutingSubresource(t *testing.T) {
	e := newEnv(t)
	id := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com"}).json(t)["id"].(string)
	base := "/api/v2/links/" + id + "/routing"
	if r := e.do("GET", base, "user", nil); r.code != 404 {
		t.Fatalf("get none: %d %s", r.code, r.body)
	}
	if r := e.do("PUT", base, "user", sampleRoutingBody()); r.code != 200 || !strings.Contains(r.body, `"mode":"redirect"`) {
		t.Fatalf("put: %d %s", r.code, r.body)
	}
	before := len(e.res.Invalidations())
	// PATCH merges: toggle the link, disable iOS, change android url, remove desktop.
	r := e.do("PATCH", base, "user", map[string]any{"enabled": false, "rules": map[string]any{
		"ios":     map[string]any{"enabled": false},
		"android": map[string]any{"url": "https://play.google.com/b"},
		"desktop": nil,
	}})
	want := `{"enabled":false,"mode":"redirect","rules":{"ios":{"enabled":false,"url":"https://apps.apple.com/app/id1","app_url":"myapp://x"},"android":{"enabled":true,"url":"https://play.google.com/b","app_url":null}}}`
	if r.code != 200 || r.body != want {
		t.Fatalf("patch: %d\n got  %s\n want %s", r.code, r.body, want)
	}
	if len(e.res.Invalidations()) <= before {
		t.Fatal("routing change did not invalidate the redirect cache")
	}
	if r := e.do("GET", base, "user", nil); r.body != want {
		t.Fatalf("get after patch: %s", r.body)
	}
	// Other users can't see or change it; admins can.
	other := e.st.SeedUser(domain.User{Email: "o@x.com", Password: hash(t, "pw"), Verified: true, APIKey: ptr("other-key-00000000000000000000000000000")})
	_ = other
	if r := e.do("GET", base, "key:other-key-00000000000000000000000000000", nil); r.code != 404 {
		t.Fatalf("other user: %d", r.code)
	}
	if r := e.do("GET", base, "admin", nil); r.code != 200 {
		t.Fatalf("admin: %d", r.code)
	}
	if r := e.do("GET", base+"/stats", "user", nil); r.code != 200 || !strings.Contains(r.body, `"route":[`) || !strings.Contains(r.body, `"id":"`+id+`"`) {
		t.Fatalf("stats: %d %s", r.code, r.body)
	}
	if r := e.do("DELETE", base, "user", nil); r.code != 200 {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	if r := e.do("DELETE", base, "user", nil); r.code != 404 {
		t.Fatalf("delete again: %d", r.code)
	}
}

func TestEditLinkRoutingInline(t *testing.T) {
	e := newEnv(t)
	id := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com"}).json(t)["id"].(string)
	// Routing-only edit is a valid change (Kutt would say "Should at least update one field.").
	r := e.do("PATCH", "/api/v2/links/"+id, "user", map[string]any{"routing": sampleRoutingBody()})
	if r.code != 200 || !strings.Contains(r.body, `"routing":{"enabled":true`) {
		t.Fatalf("routing-only edit: %d %s", r.code, r.body)
	}
	// Other edits keep routing in the response.
	r = e.do("PATCH", "/api/v2/links/"+id, "user", map[string]any{"description": "hello"})
	if r.code != 200 || !strings.Contains(r.body, `"routing":{`) {
		t.Fatalf("edit keeps routing: %d %s", r.code, r.body)
	}
	// routing: null removes it.
	r = e.do("PATCH", "/api/v2/links/"+id, "user", map[string]any{"routing": nil})
	if r.code != 200 || strings.Contains(r.body, "routing") {
		t.Fatalf("remove: %d %s", r.code, r.body)
	}
	// The admin edit route still works through the shared dispatcher.
	if r := e.do("PATCH", "/api/v2/links/admin/"+id, "admin", map[string]any{"description": "by admin"}); r.code != 200 {
		t.Fatalf("admin edit: %d %s", r.code, r.body)
	}
	if r := e.do("PATCH", "/api/v2/links/"+id+"/nope", "user", map[string]any{}); r.code != 404 {
		t.Fatalf("unknown sub: %d", r.code)
	}
}

func TestRoutingInstanceSwitch(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.RoutingEnabled = false })
	r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com", "routing": sampleRoutingBody()})
	if r.code != 400 || r.json(t)["error"] != msgRoutingDisabled {
		t.Fatalf("%d %s", r.code, r.body)
	}
	// Kutt clients that never send routing are unaffected.
	if r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com"}); r.code != 201 {
		t.Fatalf("plain create: %d", r.code)
	}
}

func TestRoutingPermissions(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.RoutingDefaultModes = []string{"redirect"} })
	smart := map[string]any{"mode": "smart", "rules": map[string]any{"ios": map[string]any{"url": "https://x.com"}}}
	r := e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com", "routing": smart})
	if r.code != 403 || r.json(t)["error"] != msgRoutingNotAllowed {
		t.Fatalf("smart by default user: %d %s", r.code, r.body)
	}
	// Admins always have every mode.
	if r := e.do("POST", "/api/v2/links", "admin", map[string]any{"target": "https://example.com", "routing": smart}); r.code != 201 {
		t.Fatalf("admin smart: %d %s", r.code, r.body)
	}
	uid := itoa(e.user.ID)
	r = e.do("PATCH", "/api/v2/users/admin/"+uid+"/routing", "admin", map[string]any{"allowed_modes": []string{"redirect", "smart"}})
	if r.code != 200 || !strings.Contains(r.body, `"allowed_modes":["redirect","smart"]`) {
		t.Fatalf("grant: %d %s", r.code, r.body)
	}
	r = e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com", "routing": smart})
	if r.code != 201 {
		t.Fatalf("smart after grant: %d %s", r.code, r.body)
	}
	linkID := r.json(t)["id"].(string)
	// Revoking smart disables that link's routing and reports it.
	r = e.do("PATCH", "/api/v2/users/admin/"+uid+"/routing", "admin", map[string]any{"allowed_modes": []string{"redirect"}})
	if r.code != 200 || !strings.Contains(r.body, `"disabled_links":1`) {
		t.Fatalf("revoke: %d %s", r.code, r.body)
	}
	if r := e.do("GET", "/api/v2/links/"+linkID+"/routing", "user", nil); !strings.HasPrefix(r.body, `{"enabled":false,"mode":"smart"`) {
		t.Fatalf("after revoke: %s", r.body)
	}
	// Re-enabling a revoked mode is refused.
	if r := e.do("PATCH", "/api/v2/links/"+linkID+"/routing", "user", map[string]any{"enabled": true}); r.code != 403 {
		t.Fatalf("re-enable revoked: %d %s", r.code, r.body)
	}
	// null resets to the instance default; [] removes all access.
	r = e.do("PATCH", "/api/v2/users/admin/"+uid+"/routing", "admin", map[string]any{"allowed_modes": []string{}})
	if r.code != 200 || !strings.Contains(r.body, `"allowed_modes":[]`) {
		t.Fatalf("none: %d %s", r.code, r.body)
	}
	r = e.do("GET", "/api/v2/users/admin/"+uid+"/routing", "admin", nil)
	if !strings.Contains(r.body, `"allowed_modes_override":[]`) {
		t.Fatalf("admin get: %s", r.body)
	}
	r = e.do("PATCH", "/api/v2/users/admin/"+uid+"/routing", "admin", map[string]any{"allowed_modes": nil})
	if r.code != 200 || !strings.Contains(r.body, `"allowed_modes":["redirect"]`) || !strings.Contains(r.body, `"allowed_modes_override":null`) {
		t.Fatalf("reset: %d %s", r.code, r.body)
	}
	if r := e.do("PATCH", "/api/v2/users/admin/"+uid+"/routing", "user", map[string]any{"allowed_modes": nil}); r.code != 401 {
		t.Fatalf("non-admin: %d", r.code)
	}
	if r := e.do("PATCH", "/api/v2/users/admin/"+uid+"/routing", "admin", map[string]any{"allowed_modes": []string{"teleport"}}); r.code != 400 {
		t.Fatalf("bad mode: %d", r.code)
	}
}

func TestUserRoutingDefaults(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/api/v2/users/routing", "user", nil)
	if r.body != `{"enabled":true,"allowed_modes":["redirect","deeplink","smart"],"defaults":null,"auto_apply":false}` {
		t.Fatalf("initial: %s", r.body)
	}
	r = e.do("PUT", "/api/v2/users/routing", "user", map[string]any{"defaults": sampleRoutingBody(), "auto_apply": true})
	if r.code != 200 || !strings.Contains(r.body, `"auto_apply":true`) {
		t.Fatalf("put: %d %s", r.code, r.body)
	}
	// New links get the defaults unless they send routing.
	r = e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com"})
	if !strings.Contains(r.body, `"routing":{"enabled":true,"mode":"redirect"`) {
		t.Fatalf("auto-applied: %s", r.body)
	}
	r = e.do("POST", "/api/v2/links", "user", map[string]any{"target": "https://example.com/2", "routing": nil})
	if strings.Contains(r.body, "routing") {
		t.Fatalf("explicit null must opt out: %s", r.body)
	}
	if r := e.do("PUT", "/api/v2/users/routing", "user", map[string]any{"auto_apply": "yes"}); r.code != 400 {
		t.Fatalf("bad auto_apply: %d", r.code)
	}
}

func TestDomainApps(t *testing.T) {
	e := newEnv(t)
	d := e.st.SeedDomain(domain.Domain{Address: "go.mine.link", UserID: &e.user.ID, UUID: "dddddddd-dddd-dddd-dddd-dddddddddddd"})
	_ = d
	base := "/api/v2/domains/dddddddd-dddd-dddd-dddd-dddddddddddd/apps"
	good := map[string]any{
		"ios_app_ids":     []string{"ABCDE12345.com.mine.app"},
		"android_package": "com.mine.app",
		"android_sha256":  []string{strings.Repeat("ab:", 31) + "ab"},
		"paths":           []string{"/p/*"},
	}
	if r := e.do("GET", base, "user", nil); r.code != 404 {
		t.Fatalf("none: %d", r.code)
	}
	r := e.do("PUT", base, "user", good)
	if r.code != 200 || !strings.Contains(r.body, `"android_sha256":["`+strings.Repeat("AB:", 31)+`AB"]`) ||
		!strings.Contains(r.body, `"aasa_url":"https://go.mine.link/.well-known/apple-app-site-association"`) {
		t.Fatalf("put: %d %s", r.code, r.body)
	}
	if r := e.do("GET", base, "user", nil); r.code != 200 || !strings.Contains(r.body, `"ios_app_ids":["ABCDE12345.com.mine.app"]`) {
		t.Fatalf("get: %d %s", r.code, r.body)
	}
	bad := []map[string]any{
		{"ios_app_ids": []string{"com.mine.app"}},
		{"android_package": "not a package", "android_sha256": []string{strings.Repeat("AB:", 31) + "AB"}},
		{"android_package": "com.mine.app", "android_sha256": []string{"AB:CD"}},
		{"android_package": "com.mine.app"},
		{},
		{"ios_app_ids": []string{"ABCDE12345.com.x"}, "paths": []string{"no-slash"}},
	}
	for _, b := range bad {
		if r := e.do("PUT", base, "user", b); r.code != 400 {
			t.Errorf("bad %v: %d %s", b, r.code, r.body)
		}
	}
	// Default domain: admins only.
	if r := e.do("PUT", "/api/v2/domains/default/apps", "user", good); r.code != 401 {
		t.Fatalf("default by user: %d", r.code)
	}
	if r := e.do("PUT", "/api/v2/domains/default/apps", "admin", good); r.code != 200 || !strings.Contains(r.body, `"domain":"kutt.test"`) {
		t.Fatalf("default by admin: %d %s", r.code, r.body)
	}
	if r := e.do("DELETE", base, "user", nil); r.code != 200 {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	// The admin domain delete route still works through the shared dispatcher.
	if r := e.do("DELETE", "/api/v2/domains/admin/"+itoa(d.ID), "admin", nil); r.code == 404 {
		t.Fatalf("admin domain delete routed to 404: %s", r.body)
	}
}
