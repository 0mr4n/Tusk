package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/Hulul-Group/Tusk/internal/auth"
	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/redirect"
	"github.com/Hulul-Group/Tusk/internal/store"
)

// Platform routing API (Tusk extension, docs/UNIVERSAL-LINKS.md §8).

const (
	msgRoutingDisabled   = "Platform routing is disabled."
	msgRoutingNotAllowed = "Routing mode not allowed."
	msgRoutingNone       = "Routing is not configured for this link."
	msgRoutingObject     = "Routing must be an object."
	msgRoutingMode       = "Routing mode must be one of redirect, deeplink, smart."
	msgRoutingEnabled    = "Routing enabled must be boolean."
	msgRoutingRules      = "Routing rules must be an object."
	msgRuleObject        = "Each routing rule must be an object."
	msgRuleEnabled       = "Routing rule enabled must be boolean."
	msgRuleURL           = "Routing rule url is not valid."
	msgRuleAppURL        = "Routing rule app_url is not valid."
)

// ---------- permissions ----------

// effectiveModes: admins get every mode; others get their admin-set list or the
// instance default (ROUTING_DEFAULT_MODES).
func (s *Server) effectiveModes(u *domain.UserRouting, isAdmin bool) []domain.RoutingMode {
	if isAdmin {
		return slices.Clone(domain.RoutingModes)
	}
	if u != nil && u.AllowedModes != nil {
		return u.AllowedModes
	}
	out := []domain.RoutingMode{}
	for _, m := range s.cfg.RoutingDefaultModes {
		out = append(out, domain.RoutingMode(m))
	}
	return out
}

func (s *Server) userRouting(ctx context.Context, p *auth.Principal) (*domain.UserRouting, []domain.RoutingMode, error) {
	u, err := s.st.Routing().GetUser(ctx, p.User.ID)
	if err != nil {
		return nil, nil, err
	}
	return u, s.effectiveModes(u, p.Admin), nil
}

// allowRouting checks the instance switch and the caller's modes for r.
func (s *Server) allowRouting(ctx context.Context, p *auth.Principal, r *domain.Routing) error {
	if !s.cfg.RoutingEnabled {
		return errBody(http.StatusBadRequest, msgRoutingDisabled)
	}
	if r == nil {
		return nil
	}
	_, modes, err := s.userRouting(ctx, p)
	if err != nil {
		return err
	}
	if !slices.Contains(modes, r.Mode) {
		return errBody(http.StatusForbidden, msgRoutingNotAllowed)
	}
	return nil
}

// ---------- parsing / validation ----------

// parseRouting validates a routing object from a JSON body. base (optional) is
// the current value for partial (PATCH) updates: absent fields keep it, a rule
// set to null is removed, and rule objects are merged field by field.
func (s *Server) parseRouting(ctx context.Context, raw any, base *domain.Routing) (*domain.Routing, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errBody(http.StatusBadRequest, msgRoutingObject)
	}
	r := &domain.Routing{Enabled: true, Rules: map[domain.Platform]domain.RouteRule{}}
	if base != nil {
		r.Enabled, r.Mode = base.Enabled, base.Mode
		for k, v := range base.Rules {
			r.Rules[k] = v
		}
	}
	if v, has := m["enabled"]; has {
		b, ok := v.(bool)
		if !ok {
			return nil, errBody(http.StatusBadRequest, msgRoutingEnabled)
		}
		r.Enabled = b
	}
	if v, has := m["mode"]; has || base == nil {
		mode, _ := v.(string)
		r.Mode = domain.RoutingMode(strings.ToLower(strings.TrimSpace(mode)))
	}
	if !r.Mode.Valid() {
		return nil, errBody(http.StatusBadRequest, msgRoutingMode)
	}
	if v, has := m["rules"]; has && v != nil {
		rm, ok := v.(map[string]any)
		if !ok {
			return nil, errBody(http.StatusBadRequest, msgRoutingRules)
		}
		for k, rv := range rm {
			pl := domain.Platform(strings.ToLower(k))
			if !pl.Valid() {
				return nil, errBody(http.StatusBadRequest, "Unknown routing platform \""+k+"\". Use ios, android or desktop.")
			}
			if rv == nil {
				delete(r.Rules, pl)
				continue
			}
			ro, ok := rv.(map[string]any)
			if !ok {
				return nil, errBody(http.StatusBadRequest, msgRuleObject)
			}
			rule, existed := r.Rules[pl]
			if base == nil || !existed {
				rule = domain.RouteRule{Enabled: true}
			}
			if v, has := ro["enabled"]; has {
				b, ok := v.(bool)
				if !ok {
					return nil, errBody(http.StatusBadRequest, msgRuleEnabled)
				}
				rule.Enabled = b
			}
			if v, has := ro["url"]; has {
				u, ok := v.(string)
				if !ok {
					return nil, errBody(http.StatusBadRequest, msgRuleURL)
				}
				rule.URL = u
			}
			if v, has := ro["app_url"]; has {
				switch a := v.(type) {
				case nil:
					rule.AppURL = ""
				case string:
					rule.AppURL = strings.TrimSpace(a)
				default:
					return nil, errBody(http.StatusBadRequest, msgRuleAppURL)
				}
			}
			r.Rules[pl] = rule
		}
	}
	// Validate every rule (including ones kept from base) the same way as a target.
	for _, pl := range domain.Platforms {
		rule, ok := r.Rules[pl]
		if !ok {
			continue
		}
		u, err := s.validRuleURL(ctx, rule.URL)
		if err != nil {
			return nil, err
		}
		rule.URL = u
		if rule.AppURL != "" && (len(rule.AppURL) > 2040 || !redirect.SafeAppURL(rule.AppURL)) {
			return nil, errBody(http.StatusBadRequest, msgRuleAppURL)
		}
		r.Rules[pl] = rule
	}
	return r, nil
}

// validRuleURL applies the link-target rules (validators.js createLink target)
// and the banned domain/host checks.
func (s *Server) validRuleURL(ctx context.Context, raw string) (string, error) {
	u := jsTrim(raw)
	if u == "" || jsLen(u) > 2040 {
		return "", errBody(http.StatusBadRequest, msgRuleURL)
	}
	u = addProtocol(u)
	// Unlike link targets (Kutt accepts any scheme), rule URLs are web or store
	// pages: the smart page navigates to them from script.
	if lu := strings.ToLower(u); !strings.HasPrefix(lu, "http://") && !strings.HasPrefix(lu, "https://") {
		return "", errBody(http.StatusBadRequest, msgRuleURL)
	}
	if !validTarget(u) || removeWww(parseLegacyURL(u).Host) == s.cfg.DefaultDomain {
		return "", errBody(http.StatusBadRequest, msgRuleURL)
	}
	host := removeWww(parseLegacyURL(u).Hostname)
	if err := s.bannedDomain(ctx, host); err != nil {
		return "", err
	}
	if err := s.bannedHost(ctx, host); err != nil {
		return "", err
	}
	return u, nil
}

// ---------- JSON ----------

func routingJSON(r *domain.Routing) any {
	if r == nil {
		return nil
	}
	rules := obj{}
	for _, pl := range domain.Platforms {
		rule, ok := r.Rules[pl]
		if !ok {
			continue
		}
		ro := obj{{"enabled", rule.Enabled}, {"url", rule.URL}, {"app_url", strOrNil(nilIfEmpty(rule.AppURL))}}
		rules = append(rules, field{string(pl), ro})
	}
	return obj{{"enabled", r.Enabled}, {"mode", string(r.Mode)}, {"rules", rules}}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// withRouting appends "routing" to a link object only when the link has it, so
// links without routing stay byte-identical to Kutt (C5).
func withRouting(o obj, r *domain.Routing) obj {
	if r == nil {
		return o
	}
	return append(o, field{"routing", routingJSON(r)})
}

// routingFor loads routing for a list of link ids (nil map on failure: list
// endpoints must not fail because of a Tusk-only table).
func (s *Server) routingFor(ctx context.Context, ids []int64) map[int64]*domain.Routing {
	m, err := s.st.Routing().GetMany(ctx, ids)
	if err != nil {
		s.log.Warn("routing lookup", "err", err)
		return nil
	}
	return m
}

func (s *Server) routingOf(ctx context.Context, linkID int64) *domain.Routing {
	r, err := s.st.Routing().Get(ctx, linkID)
	if err != nil {
		return nil
	}
	return r
}

// ---------- link routing endpoints ----------

// ownedLink finds a link by uuid, restricted to the caller unless admin.
func (s *Server) ownedLink(r *http.Request, p *auth.Principal) (*domain.Link, error) {
	if err := verify(uuidParam(r)); err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	f := store.LinkFilter{UUID: &id}
	if !p.Admin {
		uid := p.User.ID
		f.UserID = &uid
	}
	l, err := s.st.Links().Find(r.Context(), f)
	if err != nil || l == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		return nil, errBody(http.StatusNotFound, "Link was not found.")
	}
	return l, nil
}

// applyRouting stores (r != nil) or removes (r == nil) a link's routing and
// invalidates the redirect caches.
func (s *Server) applyRouting(ctx context.Context, l *domain.Link, r *domain.Routing) error {
	var err error
	if r == nil {
		_, err = s.st.Routing().Delete(ctx, l.ID)
	} else {
		err = s.st.Routing().Put(ctx, l.ID, *r)
	}
	if err != nil {
		return err
	}
	s.invalidateLink(ctx, l.Address, l.DomainID)
	return nil
}

// GET /links/{id}/routing
func (s *Server) getLinkRouting(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	l, err := s.ownedLink(r, p)
	if err != nil {
		return err
	}
	rt := s.routingOf(r.Context(), l.ID)
	if rt == nil {
		return errBody(http.StatusNotFound, msgRoutingNone)
	}
	writeJSON(w, http.StatusOK, routingJSON(rt))
	return nil
}

// PUT /links/{id}/routing (replace) and PATCH /links/{id}/routing (merge).
func (s *Server) putLinkRouting(w http.ResponseWriter, r *http.Request) error {
	return s.writeLinkRouting(w, r, false)
}

func (s *Server) patchLinkRouting(w http.ResponseWriter, r *http.Request) error {
	return s.writeLinkRouting(w, r, true)
}

func (s *Server) writeLinkRouting(w http.ResponseWriter, r *http.Request, merge bool) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if !s.cfg.RoutingEnabled {
		return errBody(http.StatusBadRequest, msgRoutingDisabled)
	}
	l, err := s.ownedLink(r, p)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var base *domain.Routing
	if merge {
		base = s.routingOf(ctx, l.ID)
	}
	rt, err := s.parseRouting(ctx, map[string]any(b), base)
	if err != nil {
		return err
	}
	if err := s.allowRouting(ctx, p, rt); err != nil {
		return err
	}
	if err := s.applyRouting(ctx, l, rt); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, routingJSON(rt))
	return nil
}

// DELETE /links/{id}/routing
func (s *Server) deleteLinkRouting(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	l, err := s.ownedLink(r, p)
	if err != nil {
		return err
	}
	ok, err := s.st.Routing().Delete(r.Context(), l.ID)
	if err != nil {
		return err
	}
	if !ok {
		return errBody(http.StatusNotFound, msgRoutingNone)
	}
	s.invalidateLink(r.Context(), l.Address, l.DomainID)
	return message(w, http.StatusOK, "Routing has been removed.")
}

// GET /links/{id}/routing/stats
func (s *Server) linkRoutingStats(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	l, err := s.ownedLink(r, p)
	if err != nil {
		return err
	}
	st, err := s.st.Routing().RouteStats(r.Context(), l.ID, s.o.Now())
	if err != nil {
		s.log.Error("route stats", "err", err)
		return kuttErr("Could not get the routing stats. Try again later.")
	}
	writeJSON(w, http.StatusOK, obj{
		{"lastDay", st.LastDay},
		{"lastWeek", st.LastWeek},
		{"lastMonth", st.LastMonth},
		{"lastYear", st.LastYear},
		{"updatedAt", jsDate(st.UpdatedAt)},
		{"id", l.UUID},
		{"routing", routingJSON(s.routingOf(r.Context(), l.ID))},
	})
	return nil
}

// ---------- inline `routing` on POST/PATCH /links ----------

// inlineRouting reads b["routing"] for link create/edit. present=false when the
// key is absent; r=nil with present=true means "remove" (JSON null).
func (s *Server) inlineRouting(ctx context.Context, p *auth.Principal, b body) (r *domain.Routing, present bool, err error) {
	raw, has := b["routing"]
	if !has {
		return nil, false, nil
	}
	if p == nil {
		return nil, true, errBody(http.StatusBadRequest, "Only users can use this field.")
	}
	if raw == nil {
		return nil, true, nil
	}
	if !s.cfg.RoutingEnabled {
		return nil, true, errBody(http.StatusBadRequest, msgRoutingDisabled)
	}
	r, err = s.parseRouting(ctx, raw, nil)
	if err != nil {
		return nil, true, err
	}
	if err := s.allowRouting(ctx, p, r); err != nil {
		return nil, true, err
	}
	return r, true, nil
}

// defaultRouting returns the user's auto-applied defaults for a new link, if
// any and still allowed.
func (s *Server) defaultRouting(ctx context.Context, p *auth.Principal) *domain.Routing {
	if p == nil || !s.cfg.RoutingEnabled {
		return nil
	}
	u, modes, err := s.userRouting(ctx, p)
	if err != nil || u == nil || !u.AutoApply || u.Defaults == nil || !slices.Contains(modes, u.Defaults.Mode) {
		return nil
	}
	return u.Defaults
}

// ---------- per-user controls ----------

func (s *Server) userRoutingJSON(u *domain.UserRouting, modes []domain.RoutingMode, withAdmin bool) obj {
	ms := make([]string, len(modes))
	for i, m := range modes {
		ms[i] = string(m)
	}
	o := obj{
		{"enabled", s.cfg.RoutingEnabled},
		{"allowed_modes", ms},
		{"defaults", routingJSON(u.Defaults)},
		{"auto_apply", u.AutoApply},
	}
	if withAdmin {
		var override any
		if u.AllowedModes != nil {
			ov := make([]string, len(u.AllowedModes))
			for i, m := range u.AllowedModes {
				ov[i] = string(m)
			}
			override = ov
		}
		o = append(o, field{"allowed_modes_override", override})
	}
	return o
}

// GET /users/routing
func (s *Server) getMyRouting(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	u, modes, err := s.userRouting(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.userRoutingJSON(u, modes, false))
	return nil
}

// PUT /users/routing {defaults: routing|null, auto_apply: bool}
func (s *Server) putMyRouting(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if !s.cfg.RoutingEnabled {
		return errBody(http.StatusBadRequest, msgRoutingDisabled)
	}
	ctx := r.Context()
	u, modes, err := s.userRouting(ctx, p)
	if err != nil {
		return err
	}
	if v, has := b["auto_apply"]; has {
		ab, ok := v.(bool)
		if !ok {
			return errBody(http.StatusBadRequest, "auto_apply must be boolean.")
		}
		u.AutoApply = ab
	}
	if v, has := b["defaults"]; has {
		if v == nil {
			u.Defaults = nil
		} else {
			d, err := s.parseRouting(ctx, v, nil)
			if err != nil {
				return err
			}
			if !slices.Contains(modes, d.Mode) {
				return errBody(http.StatusForbidden, msgRoutingNotAllowed)
			}
			u.Defaults = d
		}
	}
	if err := s.st.Routing().PutUser(ctx, *u); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.userRoutingJSON(u, modes, false))
	return nil
}

func (s *Server) adminTargetUser(r *http.Request) (*domain.User, error) {
	if err := verify(numericParam(r)); err != nil {
		return nil, err
	}
	n, _ := parseIntJS(r.PathValue("id"))
	id := int64(n)
	u, err := s.st.Users().Find(r.Context(), store.UserFilter{ID: &id})
	if err != nil || u == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		return nil, errBody(http.StatusNotFound, "User was not found.")
	}
	return u, nil
}

// GET /users/admin/{id}/routing
func (s *Server) getUserRoutingAdmin(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	tu, err := s.adminTargetUser(r)
	if err != nil {
		return err
	}
	u, err := s.st.Routing().GetUser(r.Context(), tu.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.userRoutingJSON(u, s.effectiveModes(u, tu.IsAdmin()), true))
	return nil
}

// PATCH /users/admin/{id}/routing {allowed_modes: [...] | null}
// Revoking a mode turns routing off (keeps the rules) on that user's links
// using it, and drops them from every redirect cache.
func (s *Server) patchUserRoutingAdmin(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if err := s.admin(p); err != nil {
		return err
	}
	tu, err := s.adminTargetUser(r)
	if err != nil {
		return err
	}
	v, has := b["allowed_modes"]
	if !has {
		return errBody(http.StatusBadRequest, "allowed_modes is required (an array of modes, or null for the instance default).")
	}
	var modes []domain.RoutingMode
	if v != nil {
		arr, ok := v.([]any)
		if !ok {
			return errBody(http.StatusBadRequest, "allowed_modes must be an array or null.")
		}
		modes = []domain.RoutingMode{}
		for _, x := range arr {
			sm, _ := x.(string)
			m := domain.RoutingMode(strings.ToLower(sm))
			if !m.Valid() {
				return errBody(http.StatusBadRequest, msgRoutingMode)
			}
			if !slices.Contains(modes, m) {
				modes = append(modes, m)
			}
		}
	}
	ctx := r.Context()
	u, err := s.st.Routing().GetUser(ctx, tu.ID)
	if err != nil {
		return err
	}
	u.AllowedModes = modes
	if err := s.st.Routing().PutUser(ctx, *u); err != nil {
		return err
	}
	eff := s.effectiveModes(u, tu.IsAdmin())
	keys, err := s.st.Routing().DisableModes(ctx, tu.ID, eff)
	if err != nil {
		return err
	}
	for _, k := range keys {
		s.invalidateLink(ctx, k.Address, k.DomainID)
	}
	o := s.userRoutingJSON(u, eff, true)
	writeJSON(w, http.StatusOK, append(o, field{"disabled_links", len(keys)}))
	return nil
}

// ---------- domain app association ----------

var (
	iosAppIDRe     = regexp.MustCompile(`^[A-Z0-9]{10}\.[A-Za-z0-9][A-Za-z0-9.\-]*$`)
	androidPkgRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	sha256FingerRe = regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`)
)

// appsDomain resolves {id} = a domain uuid owned by the caller (or any, for
// admins) or "default" (admins only). Returns the tusk_domain_apps key and address.
func (s *Server) appsDomain(r *http.Request, p *auth.Principal) (int64, string, error) {
	id := r.PathValue("id")
	if id == "default" {
		if err := s.admin(p); err != nil {
			return 0, "", err
		}
		return 0, s.cfg.DefaultDomain, nil
	}
	if err := verify(uuidParam(r)); err != nil {
		return 0, "", err
	}
	f := store.DomainFilter{UUID: &id}
	if !p.Admin {
		uid := p.User.ID
		f.UserID = &uid
	}
	d, err := s.st.Domains().Find(r.Context(), f)
	if err != nil || d == nil {
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return 0, "", err
		}
		return 0, "", errBody(http.StatusNotFound, "Domain was not found.")
	}
	return d.ID, d.Address, nil
}

func (s *Server) appsJSON(a *domain.DomainApps, address string) obj {
	scheme := "https://"
	if s.o.Mode.Dev {
		scheme = "http://"
	}
	list := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	return obj{
		{"domain", address},
		{"ios_app_ids", list(a.IOSAppIDs)},
		{"android_package", a.AndroidPackage},
		{"android_sha256", list(a.AndroidSHA256)},
		{"paths", list(a.Paths)},
		{"updated_at", jsDate(a.UpdatedAt)},
		{"aasa_url", scheme + address + "/.well-known/apple-app-site-association"},
		{"assetlinks_url", scheme + address + "/.well-known/assetlinks.json"},
	}
}

func (s *Server) appsChanged(domainID int64) {
	if s.o.AppsChanged != nil {
		s.o.AppsChanged(domainID)
	}
}

// GET /domains/{id}/apps
func (s *Server) getDomainApps(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	id, addr, err := s.appsDomain(r, p)
	if err != nil {
		return err
	}
	a, err := s.st.Routing().GetDomainApps(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		return errBody(http.StatusNotFound, "App association is not configured for this domain.")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.appsJSON(a, addr))
	return nil
}

func strList(v any, max int, item func(string) (string, bool), msg string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok || len(arr) > max {
		return nil, errBody(http.StatusBadRequest, msg)
	}
	var out []string
	for _, x := range arr {
		sx, ok := x.(string)
		if !ok {
			return nil, errBody(http.StatusBadRequest, msg)
		}
		sx, ok = item(strings.TrimSpace(sx))
		if !ok {
			return nil, errBody(http.StatusBadRequest, msg)
		}
		if !slices.Contains(out, sx) {
			out = append(out, sx)
		}
	}
	return out, nil
}

// PUT /domains/{id}/apps
func (s *Server) putDomainApps(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	p, err := s.authn(r, b, true, true)
	if err != nil {
		return err
	}
	if !s.cfg.RoutingEnabled {
		return errBody(http.StatusBadRequest, msgRoutingDisabled)
	}
	id, addr, err := s.appsDomain(r, p)
	if err != nil {
		return err
	}
	a := domain.DomainApps{DomainID: id}
	if a.IOSAppIDs, err = strList(b["ios_app_ids"], 10, func(x string) (string, bool) { return x, iosAppIDRe.MatchString(x) },
		"ios_app_ids must be a list of TEAMID.bundle.id values."); err != nil {
		return err
	}
	if v, has := b["android_package"]; has && v != nil {
		pkg, ok := v.(string)
		pkg = strings.TrimSpace(pkg)
		if !ok || (pkg != "" && (len(pkg) > 255 || !androidPkgRe.MatchString(pkg))) {
			return errBody(http.StatusBadRequest, "android_package is not a valid package name.")
		}
		a.AndroidPackage = pkg
	}
	if a.AndroidSHA256, err = strList(b["android_sha256"], 10, func(x string) (string, bool) {
		x = strings.ToUpper(x)
		return x, sha256FingerRe.MatchString(x)
	}, "android_sha256 must be a list of SHA-256 fingerprints (AA:BB:...)."); err != nil {
		return err
	}
	if a.Paths, err = strList(b["paths"], 50, func(x string) (string, bool) {
		return x, x != "" && len(x) <= 255 && (x[0] == '/' || x[0] == '*') && !strings.ContainsAny(x, "\r\n\"\\")
	}, "paths must be a list of path patterns starting with / or *."); err != nil {
		return err
	}
	if len(a.IOSAppIDs) == 0 && a.AndroidPackage == "" {
		return errBody(http.StatusBadRequest, "Set ios_app_ids or android_package.")
	}
	if (a.AndroidPackage == "") != (len(a.AndroidSHA256) == 0) {
		return errBody(http.StatusBadRequest, "android_package and android_sha256 must be set together.")
	}
	if err := s.st.Routing().PutDomainApps(r.Context(), a); err != nil {
		return err
	}
	s.appsChanged(id)
	a.UpdatedAt = s.o.Now()
	writeJSON(w, http.StatusOK, s.appsJSON(&a, addr))
	return nil
}

// DELETE /domains/{id}/apps
func (s *Server) deleteDomainApps(w http.ResponseWriter, r *http.Request) error {
	p, err := s.authn(r, body{}, true, true)
	if err != nil {
		return err
	}
	id, _, err := s.appsDomain(r, p)
	if err != nil {
		return err
	}
	ok, err := s.st.Routing().DeleteDomainApps(r.Context(), id)
	if err != nil {
		return err
	}
	if !ok {
		return errBody(http.StatusNotFound, "App association is not configured for this domain.")
	}
	s.appsChanged(id)
	return message(w, http.StatusOK, "App association has been removed.")
}
