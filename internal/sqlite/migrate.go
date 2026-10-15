package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Migrate brings db up to date with the embedded migrations. It is called
// explicitly from run() right after Open, so a future migrate-only command is
// one line away, and it delegates to migrateFS so tests can hand in any fs.FS.
func Migrate(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	return migrateFS(ctx, db, migrationsFS, logger)
}

// migration is one numbered SQL file: migrations/NNNN_name.sql.
type migration struct {
	version int
	name    string
	body    string
}

// timeLayout is the project's one timestamp format: fixed width for UTC (27
// characters), so text sorts by instant, with microsecond precision. The store
// in milestone 8 moves it to scan.go and uses it for every todo timestamp.
const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

// migrateFS applies, in version order and one transaction each, every file
// under migrations/ that schema_migrations does not already list. A failure
// rolls back that file and stops startup; the files before it stay applied.
// There are no down migrations: a file that failed never ran anywhere, so fix
// it in place; a file that applied but was wrong is corrected by a new,
// higher-numbered file, because some database already records it.
func migrateFS(ctx context.Context, db *sql.DB, fsys fs.FS, logger *slog.Logger) error {
	// The bookkeeping table is created by the runner itself, not by a numbered
	// file, so it exists before the first version is recorded.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at TEXT    NOT NULL CHECK (length(applied_at) = 27)
	) STRICT`); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, db) // reads AND closes its rows before anything else runs: the one-connection rule
	if err != nil {
		return err
	}
	pending, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	for _, m := range pending {
		if applied[m.version] {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return fmt.Errorf("sqlite: migration %s: %w", m.name, err)
		}
		logger.InfoContext(ctx, "applied migration", "version", m.version, "name", m.name)
	}
	return nil
}

// appliedVersions returns the set of versions already recorded. It is its own
// function so the deferred rows.Close runs before the caller's next statement.
func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("sqlite: scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	// rows.Err reports an error that ended the loop early; without this check a
	// failed read would look like a shorter result.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// loadMigrations reads every migrations/*.sql file and returns them sorted by
// version. A name that does not start with a number followed by an underscore
// is a startup error: the version is the contract, not the sort order of names.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read migrations dir: %w", err)
	}
	var ms []migration
	seen := make(map[int]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		version, convErr := strconv.Atoi(prefix)
		if !ok || convErr != nil || version <= 0 {
			return nil, fmt.Errorf("sqlite: migration %q: name must be NNNN_description.sql with a positive version", e.Name())
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("sqlite: migrations %q and %q share version %d", other, e.Name(), version)
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(fsys, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("sqlite: read migration %q: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: version, name: e.Name(), body: string(body)})
	}
	slices.SortFunc(ms, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	return ms, nil
}

// applyOne runs one migration file and records it, atomically.
func applyOne(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// The canonical transaction pattern: defer Rollback unconditionally. After a
	// successful Commit it is a no-op (it returns sql.ErrTxDone, which we
	// ignore); on any early return it undoes the partial work.
	defer tx.Rollback()

	// One Exec runs the whole file, several statements included: modernc
	// prepares each statement in turn from the tail SQLite hands back and steps
	// it. That is a driver choice, not a database/sql guarantee (other drivers
	// stop after the first statement), which is why migrate_test.go checks that
	// both objects in 0001 exist, so a driver change would fail loudly. A
	// migration file must not use ? placeholders: there are no arguments to
	// bind, and the driver fails the file with "missing argument".
	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(timeLayout),
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
