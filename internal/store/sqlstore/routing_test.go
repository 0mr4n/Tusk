package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/0mr4n/Tusk/internal/domain"
	"github.com/0mr4n/Tusk/internal/store"
)

func sampleRouting() domain.Routing {
	return domain.Routing{Enabled: true, Mode: domain.ModeRedirect, Rules: map[domain.Platform]domain.RouteRule{
		domain.PlatformIOS:     {Enabled: true, URL: "https://apps.apple.com/app/id1", AppURL: "myapp://x"},
		domain.PlatformAndroid: {Enabled: false, URL: "https://play.google.com/store/apps/details?id=x"},
	}}
}

func TestTuskMigrations(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		if err := db.Migrate(ctx); err != nil { // idempotent
			t.Fatal(err)
		}
		var n int
		if err := db.db.QueryRow("SELECT COUNT(*) FROM tusk_migrations").Scan(&n); err != nil || n != len(tuskMigrations) {
			t.Fatalf("tusk_migrations rows = %d (%v)", n, err)
		}
		// Upstream's ledger is untouched by Tusk migrations.
		db.db.QueryRow("SELECT COUNT(*) FROM knex_migrations").Scan(&n)
		if n != len(KnexMigrations) {
			t.Fatalf("knex_migrations rows = %d", n)
		}
		for _, tb := range []string{"tusk_link_routing", "tusk_user_routing", "tusk_domain_apps", "tusk_route_visits"} {
			if ok, err := db.hasTable(ctx, db.db, tb); err != nil || !ok {
				t.Fatalf("table %s missing (%v)", tb, err)
			}
		}
	})
}

// A redirector that starts before the api has migrated must keep redirecting.
func TestResolveBeforeTuskMigration(t *testing.T) {
	ctx := context.Background()
	db, err := OpenDSN(ctx, "sqlite", filepath.Join(t.TempDir(), "old.sqlite"), 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, _ := db.db.Conn(ctx)
	if err := db.createSchema(ctx, conn); err != nil { // Upstream schema only
		t.Fatal(err)
	}
	conn.Close()
	l, err := db.Links().Create(ctx, store.CreateLink{Address: "old", Target: "https://t"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := db.Links().Resolve(ctx, "old", nil)
	if err != nil || rt.Target != "https://t" || rt.Routing != nil {
		t.Fatalf("resolve before migration: %+v %v", rt, err)
	}
	if err := db.Routing().Put(ctx, l.ID, sampleRouting()); !errors.Is(err, errRoutingNotMigrated) {
		t.Fatalf("Put before migration: %v", err)
	}
	if _, err := db.Links().Remove(ctx, store.LinkFilter{ID: &l.ID}); err != nil {
		t.Fatal("Remove before migration:", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if !db.tuskReady(ctx) {
		t.Fatal("not ready after Migrate")
	}
}

func TestLinkRouting(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		u := mkUser(t, db, "r@x.com")
		l, err := db.Links().Create(ctx, store.CreateLink{Address: "rt", Target: "https://web", UserID: &u.ID})
		if err != nil {
			t.Fatal(err)
		}
		rs := db.Routing()
		if _, err := rs.Get(ctx, l.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Get none: %v", err)
		}
		rt, _ := db.Links().Resolve(ctx, "rt", nil)
		if rt.Routing != nil {
			t.Fatal("unrouted link resolved with routing")
		}
		if err := rs.Put(ctx, l.ID, sampleRouting()); err != nil {
			t.Fatal(err)
		}
		got, err := rs.Get(ctx, l.ID)
		if err != nil || got.Mode != domain.ModeRedirect || len(got.Rules) != 2 || got.Rules[domain.PlatformIOS].AppURL != "myapp://x" {
			t.Fatalf("Get: %+v %v", got, err)
		}
		rt, err = db.Links().Resolve(ctx, "rt", nil)
		if err != nil || rt.Routing == nil || rt.Routing.Rules[domain.PlatformIOS].URL != "https://apps.apple.com/app/id1" {
			t.Fatalf("Resolve routed: %+v %v", rt, err)
		}
		// Upsert replaces; disabled routing is not exposed to the redirector.
		r2 := sampleRouting()
		r2.Enabled, r2.Mode = false, domain.ModeSmart
		if err := rs.Put(ctx, l.ID, r2); err != nil {
			t.Fatal(err)
		}
		if rt, _ = db.Links().Resolve(ctx, "rt", nil); rt.Routing != nil {
			t.Fatal("disabled routing resolved")
		}
		if m, _ := rs.GetMany(ctx, []int64{l.ID, 999}); len(m) != 1 || m[l.ID].Mode != domain.ModeSmart || m[l.ID].Enabled {
			t.Fatalf("GetMany: %+v", m)
		}
		if err := rs.Put(ctx, 999999, sampleRouting()); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Put on missing link: %v", err)
		}
		// DisableModes: only links whose mode is not allowed.
		rs.Put(ctx, l.ID, sampleRouting())
		keys, err := rs.DisableModes(ctx, u.ID, []domain.RoutingMode{domain.ModeSmart})
		if err != nil || len(keys) != 1 || keys[0].Address != "rt" {
			t.Fatalf("DisableModes: %+v %v", keys, err)
		}
		if got, _ := rs.Get(ctx, l.ID); got.Enabled {
			t.Fatal("still enabled after DisableModes")
		}
		if keys, _ := rs.DisableModes(ctx, u.ID, nil); len(keys) != 0 {
			t.Fatalf("already disabled: %+v", keys)
		}
		// Removing the link removes its routing and route stats.
		rs.Put(ctx, l.ID, sampleRouting())
		rs.ApplyRouteDeltas(ctx, []store.RouteDelta{{LinkID: l.ID, Hour: time.Now(), Total: 1, Routes: map[string]int64{"ios": 1}}})
		if _, err := db.Links().Remove(ctx, store.LinkFilter{ID: &l.ID}); err != nil {
			t.Fatal(err)
		}
		var n int
		db.db.QueryRow(db.q("SELECT COUNT(*) FROM tusk_link_routing WHERE link_id = ?"), l.ID).Scan(&n)
		var v int
		db.db.QueryRow(db.q("SELECT COUNT(*) FROM tusk_route_visits WHERE link_id = ?"), l.ID).Scan(&v)
		if n != 0 || v != 0 {
			t.Fatalf("leftover routing=%d visits=%d", n, v)
		}
	})
}

func TestUserRoutingAndApps(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		u := mkUser(t, db, "ur@x.com")
		rs := db.Routing()
		got, err := rs.GetUser(ctx, u.ID)
		if err != nil || got.AllowedModes != nil || got.Defaults != nil || got.AutoApply {
			t.Fatalf("zero user: %+v %v", got, err)
		}
		def := sampleRouting()
		if err := rs.PutUser(ctx, domain.UserRouting{UserID: u.ID, AllowedModes: []domain.RoutingMode{}, Defaults: &def, AutoApply: true}); err != nil {
			t.Fatal(err)
		}
		got, _ = rs.GetUser(ctx, u.ID)
		if got.AllowedModes == nil || len(got.AllowedModes) != 0 || !got.AutoApply || got.Defaults == nil || got.Defaults.Mode != domain.ModeRedirect {
			t.Fatalf("user: %+v", got)
		}
		rs.PutUser(ctx, domain.UserRouting{UserID: u.ID, AllowedModes: []domain.RoutingMode{domain.ModeRedirect, domain.ModeSmart}})
		got, _ = rs.GetUser(ctx, u.ID)
		if len(got.AllowedModes) != 2 || got.AllowedModes[1] != domain.ModeSmart || got.Defaults != nil {
			t.Fatalf("user update: %+v", got)
		}

		if _, err := rs.GetDomainApps(ctx, 0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("apps none: %v", err)
		}
		a := domain.DomainApps{DomainID: 0, IOSAppIDs: []string{"ABCDE12345.com.x.app"}, AndroidPackage: "com.x.app",
			AndroidSHA256: []string{"AA:BB", "CC:DD"}, Paths: []string{"/app/*"}}
		if err := rs.PutDomainApps(ctx, a); err != nil {
			t.Fatal(err)
		}
		ga, err := rs.GetDomainApps(ctx, 0)
		if err != nil || ga.AndroidPackage != "com.x.app" || len(ga.AndroidSHA256) != 2 || ga.Paths[0] != "/app/*" || ga.IOSAppIDs[0] != "ABCDE12345.com.x.app" {
			t.Fatalf("apps: %+v %v", ga, err)
		}
		d, _ := db.Domains().Add(ctx, store.UpsertDomain{Address: "app.example", UserID: &u.ID})
		a.DomainID = d.ID
		rs.PutDomainApps(ctx, a)
		if err := db.Domains().Remove(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := rs.GetDomainApps(ctx, d.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("apps survived domain removal: %v", err)
		}
		if ok, _ := rs.DeleteDomainApps(ctx, 0); !ok {
			t.Fatal("DeleteDomainApps")
		}
	})
}

func TestRouteStats(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		u := mkUser(t, db, "rst@x.com")
		l, _ := db.Links().Create(ctx, store.CreateLink{Address: "rs", Target: "https://t", UserID: &u.ID})
		rs := db.Routing()
		now := time.Now().UTC()
		d := func(at time.Time, n int64, route, os, dev string) store.RouteDelta {
			return store.RouteDelta{LinkID: l.ID, UserID: &u.ID, Hour: at.Truncate(time.Hour), Total: n,
				Routes: map[string]int64{route: n}, OS: map[string]int64{os: n}, Device: map[string]int64{dev: n}}
		}
		// Concurrent upserts into the same bucket must not lose increments.
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := rs.ApplyRouteDeltas(ctx, []store.RouteDelta{d(now, 1, "ios", "ios", "mobile")}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if err := rs.ApplyRouteDeltas(ctx, []store.RouteDelta{
			d(now, 2, "android", "android", "tablet"),
			d(now.Add(-3*24*time.Hour), 5, "fallback", "other", "other"),
			{LinkID: 987654, Hour: now, Total: 1, Routes: map[string]int64{"ios": 1}}, // deleted link: dropped
		}); err != nil {
			t.Fatal(err)
		}
		var rows int
		db.db.QueryRow(db.q("SELECT COUNT(*) FROM tusk_route_visits WHERE link_id = ?"), l.ID).Scan(&rows)
		if rows != 2 {
			t.Fatalf("bucket rows = %d, want 2", rows)
		}
		st, err := rs.RouteStats(ctx, l.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if st.LastDay.Total != 10 || st.LastWeek.Total != 15 || st.LastYear.Total != 15 {
			t.Fatalf("totals day=%d week=%d year=%d", st.LastDay.Total, st.LastWeek.Total, st.LastYear.Total)
		}
		if st.LastDay.Views[23] != 10 {
			t.Fatalf("lastDay views %v", st.LastDay.Views)
		}
		if top := st.LastDay.Stats.Route[0]; top.Name != "ios" || top.Value != 8 {
			t.Fatalf("route %+v", st.LastDay.Stats.Route)
		}
		if top := st.LastWeek.Stats.Device[0]; top.Name != "mobile" || top.Value != 8 {
			t.Fatalf("device %+v", st.LastWeek.Stats.Device)
		}
		if len(st.LastDay.Stats.OS) != 8 || len(st.LastDay.Stats.Route) != 4 {
			t.Fatalf("bucket keys %+v", st.LastDay.Stats)
		}
	})
}

func TestSweepOrphans(t *testing.T) {
	forEach(t, func(t *testing.T, db *DB) {
		ctx := context.Background()
		l, _ := db.Links().Create(ctx, store.CreateLink{Address: "or", Target: "https://t"})
		rs := db.Routing()
		rs.Put(ctx, l.ID, sampleRouting())
		rs.PutDomainApps(ctx, domain.DomainApps{DomainID: 424242, AndroidPackage: "x"})
		rs.PutDomainApps(ctx, domain.DomainApps{DomainID: 0, AndroidPackage: "x"})
		// Simulate the Node.js original deleting the link without knowing the tusk_* tables
		// (FK enforcement off on that connection, as with a plain sqlite3 client).
		if db.d == dPostgres {
			t.Skip("postgres always enforces FKs; the orphan state cannot arise")
		}
		conn, err := db.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if db.d == dMySQL {
			conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0")
		} else {
			conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
		}
		if _, err := conn.ExecContext(ctx, db.q("DELETE FROM links WHERE id = ?"), l.ID); err != nil {
			t.Fatal(err)
		}
		if db.d == dMySQL {
			conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1")
		} else {
			conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
		}
		conn.Close()
		n, err := rs.SweepOrphans(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n < 2 {
			t.Fatalf("swept %d, want >= 2", n)
		}
		if _, err := rs.GetDomainApps(ctx, 0); err != nil {
			t.Fatal("default-domain apps must never be swept")
		}
	})
}
