package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"
)

// Tusk-only schema (docs/UNIVERSAL-LINKS.md §3).
//
// Kutt's tables are never altered (C1). Tusk features live in additive tusk_*
// tables tracked by their own ledger, tusk_migrations, so Node Kutt can keep
// running on the same database and simply ignores them.

type tuskMigration struct {
	name string
	ddl  map[dialect][]string
}

// tuskMigrations are applied in order, each once. Never edit a shipped entry:
// append a new one.
var tuskMigrations = []tuskMigration{
	{name: "0001_platform_routing", ddl: map[dialect][]string{
		dSQLite: {
			"CREATE TABLE `tusk_link_routing` (`link_id` integer not null primary key, `enabled` boolean not null default '1', `mode` varchar(16) not null default 'redirect', `rules` text not null default '{}', `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP, constraint `tusk_link_routing_link_id_foreign` foreign key(`link_id`) references `links`(`id`) on delete CASCADE)",
			"CREATE TABLE `tusk_user_routing` (`user_id` integer not null primary key, `allowed_modes` varchar(64), `defaults` text, `auto_apply` boolean not null default '0', `updated_at` datetime not null default CURRENT_TIMESTAMP, constraint `tusk_user_routing_user_id_foreign` foreign key(`user_id`) references `users`(`id`) on delete CASCADE)",
			"CREATE TABLE `tusk_domain_apps` (`domain_id` integer not null primary key, `ios_app_ids` text not null default '', `android_package` varchar(255) not null default '', `android_sha256` text not null default '', `paths` text not null default '', `updated_at` datetime not null default CURRENT_TIMESTAMP)",
			"CREATE TABLE `tusk_route_visits` (`id` integer not null primary key autoincrement, `link_id` integer not null, `user_id` integer, `hour` datetime not null, `total` integer not null default '0', " + routeCountCols("integer not null default '0'") + ", `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP, constraint `tusk_route_visits_link_id_foreign` foreign key(`link_id`) references `links`(`id`) on delete CASCADE, constraint `tusk_route_visits_user_id_foreign` foreign key(`user_id`) references `users`(`id`) on delete CASCADE)",
			"CREATE UNIQUE INDEX `tusk_route_visits_link_hour_unique` on `tusk_route_visits` (`link_id`, `hour`)",
			"CREATE INDEX `tusk_route_visits_user_id_index` on `tusk_route_visits` (`user_id`)",
		},
		dPostgres: {
			`CREATE TABLE tusk_link_routing (
  link_id integer PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT true,
  mode varchar(16) NOT NULL DEFAULT 'redirect',
  rules text NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT tusk_link_routing_link_id_foreign FOREIGN KEY (link_id) REFERENCES links(id) ON DELETE CASCADE)`,
			`CREATE TABLE tusk_user_routing (
  user_id integer PRIMARY KEY,
  allowed_modes varchar(64),
  defaults text,
  auto_apply boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT tusk_user_routing_user_id_foreign FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE)`,
			`CREATE TABLE tusk_domain_apps (
  domain_id integer PRIMARY KEY,
  ios_app_ids text NOT NULL DEFAULT '',
  android_package varchar(255) NOT NULL DEFAULT '',
  android_sha256 text NOT NULL DEFAULT '',
  paths text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
			`CREATE TABLE tusk_route_visits (
  id serial PRIMARY KEY,
  link_id integer NOT NULL,
  user_id integer,
  hour timestamptz NOT NULL,
  total integer NOT NULL DEFAULT 0,
  ` + routeCountCols("integer NOT NULL DEFAULT 0") + `,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT tusk_route_visits_link_hour_unique UNIQUE (link_id, hour),
  CONSTRAINT tusk_route_visits_link_id_foreign FOREIGN KEY (link_id) REFERENCES links(id) ON DELETE CASCADE,
  CONSTRAINT tusk_route_visits_user_id_foreign FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE)`,
			`CREATE INDEX tusk_route_visits_user_id_index ON tusk_route_visits (user_id)`,
		},
		dMySQL: {
			"CREATE TABLE `tusk_link_routing` (`link_id` int unsigned NOT NULL PRIMARY KEY, `enabled` boolean NOT NULL DEFAULT 1, `mode` varchar(16) NOT NULL DEFAULT 'redirect', `rules` text NOT NULL, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, CONSTRAINT `tusk_link_routing_link_id_foreign` FOREIGN KEY (`link_id`) REFERENCES `links` (`id`) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
			"CREATE TABLE `tusk_user_routing` (`user_id` int unsigned NOT NULL PRIMARY KEY, `allowed_modes` varchar(64), `defaults` text, `auto_apply` boolean NOT NULL DEFAULT 0, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, CONSTRAINT `tusk_user_routing_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
			"CREATE TABLE `tusk_domain_apps` (`domain_id` int unsigned NOT NULL PRIMARY KEY, `ios_app_ids` text NOT NULL, `android_package` varchar(255) NOT NULL DEFAULT '', `android_sha256` text NOT NULL, `paths` text NOT NULL, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
			"CREATE TABLE `tusk_route_visits` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `link_id` int unsigned NOT NULL, `user_id` int unsigned, `hour` datetime NOT NULL, `total` int NOT NULL DEFAULT 0, " + routeCountCols("int NOT NULL DEFAULT 0") + ", `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY `tusk_route_visits_link_hour_unique` (`link_id`, `hour`), KEY `tusk_route_visits_user_id_index` (`user_id`), CONSTRAINT `tusk_route_visits_link_id_foreign` FOREIGN KEY (`link_id`) REFERENCES `links` (`id`) ON DELETE CASCADE, CONSTRAINT `tusk_route_visits_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
		},
	}},
}

// Route-stats counter columns. Fixed columns (not JSON) so the worker can
// upsert with plain atomic increments and no read-modify-write.
var (
	routeKeys     = []string{"ios", "android", "desktop", "fallback"}
	routeOSKeys   = []string{"ios", "ipados", "android", "windows", "macos", "linux", "chromeos", "other"}
	routeDevKeys  = []string{"mobile", "tablet", "desktop", "other"}
	routeCountSet = func() []string {
		var out []string
		for _, k := range routeKeys {
			out = append(out, "r_"+k)
		}
		for _, k := range routeOSKeys {
			out = append(out, "os_"+k)
		}
		for _, k := range routeDevKeys {
			out = append(out, "dev_"+k)
		}
		return out
	}()
)

func routeCountCols(typ string) string {
	out := ""
	for i, c := range routeCountSet {
		if i > 0 {
			out += ", "
		}
		out += c + " " + typ
	}
	return out
}

// Routing-table readiness. Only the API role migrates; a standalone redirector or
// worker may start before the tables exist (rolling upgrade). Until they do, the
// store uses the original Kutt-only queries and re-probes periodically.
const (
	tuskUnknown int32 = iota
	tuskReady
	tuskMissing
)

const tuskProbeEvery = 15 * time.Second

type tuskState struct {
	state     atomic.Int32
	nextProbe atomic.Int64 // unix nanos
}

// tuskReady reports whether the tusk_* tables exist (cached; cheap on the hot path).
func (s *DB) tuskReady(ctx context.Context) bool { return s.tuskReadyOn(ctx, s.db) }

// tuskReadyOn probes on x (pass the open transaction to avoid needing a second
// pooled connection while holding one).
func (s *DB) tuskReadyOn(ctx context.Context, x querier) bool {
	switch s.tusk.state.Load() {
	case tuskReady:
		return true
	case tuskMissing:
		if time.Now().UnixNano() < s.tusk.nextProbe.Load() {
			return false
		}
	}
	s.tusk.nextProbe.Store(time.Now().Add(tuskProbeEvery).UnixNano())
	ok, err := s.hasTable(ctx, x, "tusk_migrations")
	if err != nil || !ok {
		s.tusk.state.Store(tuskMissing)
		return false
	}
	var n int
	if err := x.QueryRowContext(ctx, s.q("SELECT COUNT(*) FROM tusk_migrations WHERE name = ?"), tuskMigrations[0].name).Scan(&n); err != nil || n == 0 {
		s.tusk.state.Store(tuskMissing)
		return false
	}
	s.tusk.state.Store(tuskReady)
	return true
}

// migrateTusk applies pending tusk_* migrations. Runs under the Migrate lock.
func (s *DB) migrateTusk(ctx context.Context, conn *sql.Conn) error {
	has, err := s.hasTable(ctx, conn, "tusk_migrations")
	if err != nil {
		return err
	}
	if !has {
		ddl := "CREATE TABLE tusk_migrations (name varchar(255) NOT NULL PRIMARY KEY, applied_at varchar(32) NOT NULL)"
		if _, err := conn.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("sqlstore: create tusk_migrations: %w", err)
		}
	}
	applied := map[string]bool{}
	rows, err := conn.QueryContext(ctx, "SELECT name FROM tusk_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		applied[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range tuskMigrations {
		if applied[m.name] {
			continue
		}
		// pg and sqlite: one transaction per migration. mysql commits DDL implicitly,
		// so a failed mysql migration may need its tables dropped by hand before retrying.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range m.ddl[s.d] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("sqlstore: tusk migration %s: %w\n%s", m.name, err, stmt)
			}
		}
		if _, err := tx.ExecContext(ctx, s.q("INSERT INTO tusk_migrations (name, applied_at) VALUES (?, ?)"), m.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	s.tusk.state.Store(tuskReady)
	return nil
}
