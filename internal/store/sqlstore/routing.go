package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/0mr4n/Tusk/internal/domain"
	"github.com/0mr4n/Tusk/internal/store"
)

// routingStore implements store.RoutingStore over the tusk_* tables
// (docs/UNIVERSAL-LINKS.md §3).
type routingStore struct{ s *DB }

var _ store.RoutingStore = (*routingStore)(nil)

var errRoutingNotMigrated = errors.New("sqlstore: platform routing tables are missing; start the api role once to migrate")

func (rs *routingStore) ready(ctx context.Context) error {
	if !rs.s.tuskReady(ctx) {
		return errRoutingNotMigrated
	}
	return nil
}

// upsert builds an INSERT that updates updateCols on a key conflict. inc lists
// columns to increment instead of overwrite.
func (s *DB) upsert(table string, cols []string, key []string, set []string, inc []string) string {
	ph := placeholders(len(cols))
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + ph + ")"
	var parts []string
	if s.d == dMySQL {
		for _, c := range set {
			parts = append(parts, c+" = VALUES("+c+")")
		}
		for _, c := range inc {
			parts = append(parts, c+" = "+c+" + VALUES("+c+")")
		}
		return q + " ON DUPLICATE KEY UPDATE " + strings.Join(parts, ", ")
	}
	for _, c := range set {
		parts = append(parts, c+" = excluded."+c)
	}
	for _, c := range inc {
		parts = append(parts, c+" = "+table+"."+c+" + excluded."+c)
	}
	return q + " ON CONFLICT (" + strings.Join(key, ", ") + ") DO UPDATE SET " + strings.Join(parts, ", ")
}

func isFKViolation(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && (me.Number == 1452 || me.Number == 1216) {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "SQLSTATE 23503") || strings.Contains(m, "FOREIGN KEY constraint failed")
}

// ---------- link routing ----------

func decodeRouting(enabled bool, mode string, rules []byte) (*domain.Routing, error) {
	r := &domain.Routing{Enabled: enabled, Mode: domain.RoutingMode(mode)}
	if len(rules) > 0 {
		if err := json.Unmarshal(rules, &r.Rules); err != nil {
			return nil, fmt.Errorf("sqlstore: tusk_link_routing.rules: %w", err)
		}
	}
	if r.Rules == nil {
		r.Rules = map[domain.Platform]domain.RouteRule{}
	}
	return r, nil
}

func (rs *routingStore) Get(ctx context.Context, linkID int64) (*domain.Routing, error) {
	if err := rs.ready(ctx); err != nil {
		return nil, err
	}
	s := rs.s
	var (
		enabled boolScan
		mode    string
		rules   []byte
	)
	err := s.db.QueryRowContext(ctx, s.q("SELECT enabled, mode, rules FROM tusk_link_routing WHERE link_id = ?"), linkID).Scan(&enabled, &mode, &rules)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeRouting(bool(enabled), mode, rules)
}

func (rs *routingStore) GetMany(ctx context.Context, linkIDs []int64) (map[int64]*domain.Routing, error) {
	out := map[int64]*domain.Routing{}
	if len(linkIDs) == 0 || !rs.s.tuskReady(ctx) {
		return out, nil
	}
	s := rs.s
	for len(linkIDs) > 0 {
		n := min(len(linkIDs), 500)
		chunk := linkIDs[:n]
		linkIDs = linkIDs[n:]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, s.q("SELECT link_id, enabled, mode, rules FROM tusk_link_routing WHERE link_id IN ("+placeholders(n)+")"), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				id      int64
				enabled boolScan
				mode    string
				rules   []byte
			)
			if err := rows.Scan(&id, &enabled, &mode, &rules); err != nil {
				rows.Close()
				return nil, err
			}
			r, err := decodeRouting(bool(enabled), mode, rules)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = r
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (rs *routingStore) Put(ctx context.Context, linkID int64, r domain.Routing) error {
	if err := rs.ready(ctx); err != nil {
		return err
	}
	s := rs.s
	rules := r.Rules
	if rules == nil {
		rules = map[domain.Platform]domain.RouteRule{}
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	now := s.ts(time.Now())
	q := s.upsert("tusk_link_routing", []string{"link_id", "enabled", "mode", "rules", "created_at", "updated_at"},
		[]string{"link_id"}, []string{"enabled", "mode", "rules", "updated_at"}, nil)
	_, err = s.db.ExecContext(ctx, s.q(q), linkID, s.b(r.Enabled), string(r.Mode), string(b), now, now)
	if isFKViolation(err) {
		return domain.ErrNotFound
	}
	return err
}

func (rs *routingStore) Delete(ctx context.Context, linkID int64) (bool, error) {
	if err := rs.ready(ctx); err != nil {
		return false, err
	}
	res, err := rs.s.db.ExecContext(ctx, rs.s.q("DELETE FROM tusk_link_routing WHERE link_id = ?"), linkID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (rs *routingStore) DisableModes(ctx context.Context, userID int64, allowed []domain.RoutingMode) ([]store.LinkKey, error) {
	if !rs.s.tuskReady(ctx) {
		return nil, nil
	}
	s := rs.s
	q := "SELECT tusk_link_routing.link_id, links.address, links.domain_id FROM tusk_link_routing JOIN links ON links.id = tusk_link_routing.link_id WHERE links.user_id = ? AND tusk_link_routing.enabled = ?"
	args := []any{userID, s.b(true)}
	if len(allowed) > 0 {
		q += " AND tusk_link_routing.mode NOT IN (" + placeholders(len(allowed)) + ")"
		for _, m := range allowed {
			args = append(args, string(m))
		}
	}
	var keys []store.LinkKey
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, s.q(q), args...)
		if err != nil {
			return err
		}
		var ids []any
		for rows.Next() {
			var (
				id  int64
				k   store.LinkKey
				dom sql.NullInt64
			)
			if err := rows.Scan(&id, &k.Address, &dom); err != nil {
				rows.Close()
				return err
			}
			k.DomainID = niPtr(dom)
			ids = append(ids, id)
			keys = append(keys, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		for len(ids) > 0 {
			n := min(len(ids), 500)
			chunk := ids[:n]
			ids = ids[n:]
			a := append([]any{s.b(false), s.ts(time.Now())}, chunk...)
			if _, err := tx.ExecContext(ctx, s.q("UPDATE tusk_link_routing SET enabled = ?, updated_at = ? WHERE link_id IN ("+placeholders(n)+")"), a...); err != nil {
				return err
			}
		}
		return nil
	})
	return keys, err
}

// ---------- user controls ----------

func encodeModes(m []domain.RoutingMode) any {
	if m == nil {
		return nil
	}
	parts := make([]string, len(m))
	for i, x := range m {
		parts[i] = string(x)
	}
	return strings.Join(parts, ",")
}

func decodeModes(n sql.NullString) []domain.RoutingMode {
	if !n.Valid {
		return nil
	}
	out := []domain.RoutingMode{}
	for _, p := range strings.Split(n.String, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, domain.RoutingMode(p))
		}
	}
	return out
}

func (rs *routingStore) GetUser(ctx context.Context, userID int64) (*domain.UserRouting, error) {
	u := &domain.UserRouting{UserID: userID}
	if !rs.s.tuskReady(ctx) {
		return u, nil
	}
	s := rs.s
	var (
		modes   sql.NullString
		defs    sql.NullString
		auto    boolScan
		updated nullTime
	)
	err := s.db.QueryRowContext(ctx, s.q("SELECT allowed_modes, defaults, auto_apply, updated_at FROM tusk_user_routing WHERE user_id = ?"), userID).
		Scan(&modes, &defs, &auto, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	if err != nil {
		return nil, err
	}
	u.AllowedModes = decodeModes(modes)
	u.AutoApply = bool(auto)
	u.UpdatedAt = updated.T
	if defs.Valid && defs.String != "" {
		var r domain.Routing
		if err := json.Unmarshal([]byte(defs.String), &r); err != nil {
			return nil, fmt.Errorf("sqlstore: tusk_user_routing.defaults: %w", err)
		}
		u.Defaults = &r
	}
	return u, nil
}

func (rs *routingStore) PutUser(ctx context.Context, u domain.UserRouting) error {
	if err := rs.ready(ctx); err != nil {
		return err
	}
	s := rs.s
	var defs any
	if u.Defaults != nil {
		b, err := json.Marshal(u.Defaults)
		if err != nil {
			return err
		}
		defs = string(b)
	}
	q := s.upsert("tusk_user_routing", []string{"user_id", "allowed_modes", "defaults", "auto_apply", "updated_at"},
		[]string{"user_id"}, []string{"allowed_modes", "defaults", "auto_apply", "updated_at"}, nil)
	_, err := s.db.ExecContext(ctx, s.q(q), u.UserID, encodeModes(u.AllowedModes), defs, s.b(u.AutoApply), s.ts(time.Now()))
	if isFKViolation(err) {
		return domain.ErrNotFound
	}
	return err
}

// ---------- domain app association ----------

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (rs *routingStore) GetDomainApps(ctx context.Context, domainID int64) (*domain.DomainApps, error) {
	if !rs.s.tuskReady(ctx) {
		return nil, domain.ErrNotFound
	}
	s := rs.s
	var (
		ios, sha, paths string
		a               = &domain.DomainApps{DomainID: domainID}
		updated         nullTime
	)
	err := s.db.QueryRowContext(ctx, s.q("SELECT ios_app_ids, android_package, android_sha256, paths, updated_at FROM tusk_domain_apps WHERE domain_id = ?"), domainID).
		Scan(&ios, &a.AndroidPackage, &sha, &paths, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.IOSAppIDs, a.AndroidSHA256, a.Paths = splitList(ios), splitList(sha), splitList(paths)
	a.UpdatedAt = updated.T
	return a, nil
}

func (rs *routingStore) PutDomainApps(ctx context.Context, a domain.DomainApps) error {
	if err := rs.ready(ctx); err != nil {
		return err
	}
	s := rs.s
	q := s.upsert("tusk_domain_apps", []string{"domain_id", "ios_app_ids", "android_package", "android_sha256", "paths", "updated_at"},
		[]string{"domain_id"}, []string{"ios_app_ids", "android_package", "android_sha256", "paths", "updated_at"}, nil)
	_, err := s.db.ExecContext(ctx, s.q(q), a.DomainID, strings.Join(a.IOSAppIDs, "\n"), a.AndroidPackage,
		strings.Join(a.AndroidSHA256, "\n"), strings.Join(a.Paths, "\n"), s.ts(time.Now()))
	return err
}

func (rs *routingStore) DeleteDomainApps(ctx context.Context, domainID int64) (bool, error) {
	if err := rs.ready(ctx); err != nil {
		return false, err
	}
	res, err := rs.s.db.ExecContext(ctx, rs.s.q("DELETE FROM tusk_domain_apps WHERE domain_id = ?"), domainID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---------- stats ----------

func (rs *routingStore) ApplyRouteDeltas(ctx context.Context, deltas []store.RouteDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	if err := rs.ready(ctx); err != nil {
		return err
	}
	s := rs.s
	cols := append([]string{"link_id", "user_id", "hour", "total"}, routeCountSet...)
	cols = append(cols, "created_at", "updated_at")
	inc := append([]string{"total"}, routeCountSet...)
	q := s.q(s.upsert("tusk_route_visits", cols, []string{"link_id", "hour"}, []string{"updated_at"}, inc))
	var errs []error
	for i := range deltas {
		d := &deltas[i]
		if d.Total <= 0 {
			continue
		}
		now := s.ts(time.Now())
		args := []any{d.LinkID, i64Ptr(d.UserID), s.ts(d.Hour.UTC().Truncate(time.Hour)), d.Total}
		for _, k := range routeKeys {
			args = append(args, d.Routes[k])
		}
		for _, k := range routeOSKeys {
			args = append(args, d.OS[k])
		}
		for _, k := range routeDevKeys {
			args = append(args, d.Device[k])
		}
		args = append(args, now, now)
		var err error
		for attempt := 0; attempt < 8; attempt++ {
			_, err = s.db.ExecContext(ctx, q, args...)
			if !retryable(err) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 15 * time.Millisecond):
			}
		}
		if isFKViolation(err) {
			continue // link (or user) deleted since the click
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("route delta link=%d hour=%s: %w", d.LinkID, d.Hour.Format(time.RFC3339), err))
		}
	}
	return errors.Join(errs...)
}

type routeAcc struct {
	views  []int64
	total  int64
	routes [4]int64
	os     [8]int64
	dev    [4]int64
}

// RouteStats uses the same periods and view-index arithmetic as Kutt's stats
// (visit.queries.js find), keyed on the bucket hour.
func (rs *routingStore) RouteStats(ctx context.Context, linkID int64, now time.Time) (*store.RouteStats, error) {
	now = now.UTC()
	type period struct {
		from time.Time
		diff func(l, r time.Time) int
		acc  *routeAcc
	}
	periods := []period{
		{now.Add(-24 * time.Hour), differenceInHours, &routeAcc{views: make([]int64, 24)}},
		{jsDate(now.Year(), now.Month(), now.Day()-7, now.Hour(), now.Minute(), now.Second(), now.Nanosecond()), differenceInDays, &routeAcc{views: make([]int64, 7)}},
		{jsDate(now.Year(), now.Month(), now.Day()-30, now.Hour(), now.Minute(), now.Second(), now.Nanosecond()), differenceInDays, &routeAcc{views: make([]int64, 30)}},
		{subMonths(now, 12), differenceInMonths, &routeAcc{views: make([]int64, 12)}},
	}
	if rs.s.tuskReady(ctx) {
		s := rs.s
		// The bucket starting at hour H covers clicks up to H+59m, so it counts in a
		// period when H+1h is after the period start.
		cutoff := periods[3].from.Add(-time.Hour)
		rows, err := s.db.QueryContext(ctx, s.q("SELECT hour, total, "+strings.Join(routeCountSet, ", ")+
			" FROM tusk_route_visits WHERE link_id = ? AND hour > ? ORDER BY hour"), linkID, s.ts(cutoff))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				hour  nullTime
				total int64
				a     routeAcc
			)
			dest := []any{&hour, &total}
			for i := range a.routes {
				dest = append(dest, &a.routes[i])
			}
			for i := range a.os {
				dest = append(dest, &a.os[i])
			}
			for i := range a.dev {
				dest = append(dest, &a.dev[i])
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, err
			}
			if !hour.Valid {
				continue
			}
			end := hour.T.Add(time.Hour - time.Second)
			for _, p := range periods {
				if !end.After(p.from) {
					continue
				}
				acc := p.acc
				for i := range a.routes {
					acc.routes[i] += a.routes[i]
				}
				for i := range a.os {
					acc.os[i] += a.os[i]
				}
				for i := range a.dev {
					acc.dev[i] += a.dev[i]
				}
				at := hour.T
				if at.After(now) {
					at = now
				}
				if idx := len(acc.views) - p.diff(now, at) - 1; idx >= 0 && idx < len(acc.views) {
					acc.views[idx] += total
				}
				acc.total += total
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	conv := func(a *routeAcc) store.RoutePeriod {
		return store.RoutePeriod{
			Stats: store.RouteBuckets{
				Route:  toArray(routeKeys, a.routes[:]),
				OS:     toArray(routeOSKeys, a.os[:]),
				Device: toArray(routeDevKeys, a.dev[:]),
			},
			Views: a.views,
			Total: a.total,
		}
	}
	return &store.RouteStats{
		LastDay:   conv(periods[0].acc),
		LastWeek:  conv(periods[1].acc),
		LastMonth: conv(periods[2].acc),
		LastYear:  conv(periods[3].acc),
		UpdatedAt: now,
	}, nil
}

func (rs *routingStore) SweepOrphans(ctx context.Context) (int64, error) {
	if !rs.s.tuskReady(ctx) {
		return 0, nil
	}
	s := rs.s
	var total int64
	for _, q := range []string{
		"DELETE FROM tusk_link_routing WHERE NOT EXISTS (SELECT 1 FROM links WHERE links.id = tusk_link_routing.link_id)",
		"DELETE FROM tusk_route_visits WHERE NOT EXISTS (SELECT 1 FROM links WHERE links.id = tusk_route_visits.link_id)",
		"DELETE FROM tusk_user_routing WHERE NOT EXISTS (SELECT 1 FROM users WHERE users.id = tusk_user_routing.user_id)",
		"DELETE FROM tusk_domain_apps WHERE domain_id <> 0 AND NOT EXISTS (SELECT 1 FROM domains WHERE domains.id = tusk_domain_apps.domain_id)",
	} {
		res, err := s.db.ExecContext(ctx, q)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
