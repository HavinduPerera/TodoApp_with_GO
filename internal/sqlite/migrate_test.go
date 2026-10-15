// These tests live in package sqlite, not sqlite_test, because they drive the
// unexported migrateFS with an in-memory fs.FS to simulate a broken file.
package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// newTestDB opens a fresh database in a temp directory. The 5 s context turns
// a violation of the one-connection rule (a nested query while rows are open)
// into a fast, readable failure instead of a hung test.
func newTestDB(t *testing.T) (context.Context, *testDB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, &testDB{db: db, t: t}
}

func TestOpenAppliesPragmasOnEveryConnection(t *testing.T) {
	ctx, tdb := newTestDB(t)
	assertPragmas := func(when string) {
		t.Helper()
		for pragma, want := range map[string]string{
			"journal_mode": "wal",
			"foreign_keys": "1",
			"busy_timeout": "5000",
			"synchronous":  "1", // NORMAL
		} {
			if got := tdb.queryString(ctx, "PRAGMA "+pragma); got != want {
				t.Errorf("%s: %s = %q, want %q", when, pragma, got, want)
			}
		}
	}
	assertPragmas("first connection")

	// Drop the pool's idle connection so the next statement dials a new one. A
	// one-off db.Exec("PRAGMA ...") would not survive this; a DSN pragma does,
	// because it runs on every connection the pool opens.
	tdb.db.SetMaxIdleConns(0)
	tdb.db.SetMaxIdleConns(2)
	if closed := tdb.db.Stats().MaxIdleClosed; closed != 1 {
		t.Fatalf("MaxIdleClosed = %d, want 1: the pool did not recycle its connection, so this test proves nothing", closed)
	}
	assertPragmas("fresh connection")
}

func TestOpenEscapesURICharactersInThePath(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	// '#', '?' and '%' are legal in file names but special in a SQLite URI.
	path := filepath.Join(t.TempDir(), "hash#quest?pct%41", "todo.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	var opened string
	if err := db.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&opened); err != nil {
		t.Fatalf("pragma_database_list: %v", err)
	}
	want, _ := filepath.EvalSymlinks(path) // macOS: /var is a symlink to /private/var
	got, _ := filepath.EvalSymlinks(opened)
	if got != want {
		t.Errorf("SQLite opened %q, want %q", opened, path)
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal: a '?' in the path must not swallow the pragmas", mode, err)
	}
}

func TestOpenRejectsInMemory(t *testing.T) {
	if _, err := Open(t.Context(), ":memory:"); err == nil {
		t.Fatal("Open(:memory:) = nil error, want a refusal")
	}
}

func TestOpenCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "todo.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	db.Close()
}

func TestMigrateIsIdempotentAndCreatesEverything(t *testing.T) {
	ctx, tdb := newTestDB(t)
	logger := slog.New(slog.DiscardHandler)
	for i := range 2 { // a second run must apply nothing and fail nothing
		if err := Migrate(ctx, tdb.db, logger); err != nil {
			t.Fatalf("Migrate run %d: %v", i+1, err)
		}
	}
	if got := tdb.queryString(ctx, "SELECT count(*) FROM schema_migrations"); got != "1" {
		t.Errorf("schema_migrations rows = %s, want 1 after two runs", got)
	}
	if got := tdb.queryString(ctx, "SELECT name FROM schema_migrations WHERE version = 1"); got != "0001_create_todos.sql" {
		t.Errorf("version 1 name = %q, want 0001_create_todos.sql", got)
	}
	// Both objects from the one multi-statement file must exist: this is what
	// pins the driver's "one Exec runs the whole file" behaviour.
	for _, name := range []string{"todos", "todos_done_created_idx"} {
		if got := tdb.queryString(ctx, "SELECT count(*) FROM sqlite_master WHERE name = '"+name+"'"); got != "1" {
			t.Errorf("sqlite_master has %s rows named %s, want 1", got, name)
		}
	}
}

func TestMigrateRollsBackABrokenFile(t *testing.T) {
	ctx, tdb := newTestDB(t)
	fsys := fstest.MapFS{
		"migrations/0001_ok.sql":  {Data: []byte(`CREATE TABLE ok (id INTEGER PRIMARY KEY) STRICT;`)},
		"migrations/0002_bad.sql": {Data: []byte(`CREATE TABLE bad (id INTEGER PRIMARY KEY) STRICT; INSERT INTO nope VALUES (1);`)},
	}
	err := migrateFS(ctx, tdb.db, fsys, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Fatalf("migrateFS error = %v, want one naming 0002_bad.sql", err)
	}
	if got := tdb.queryString(ctx, "SELECT count(*) FROM schema_migrations"); got != "1" {
		t.Errorf("schema_migrations rows = %s, want exactly 1 (0001 applied, 0002 rolled back)", got)
	}
	if got := tdb.queryString(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'ok'"); got != "1" {
		t.Errorf("table ok exists = %s, want 1", got)
	}
	// The CREATE TABLE bad ran inside the failed transaction, so it is gone too.
	if got := tdb.queryString(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'bad'"); got != "0" {
		t.Errorf("table bad exists = %s, want 0: the transaction must roll back the whole file", got)
	}
}

func TestMigrateRejectsBadFileNames(t *testing.T) {
	ctx, tdb := newTestDB(t)
	tests := map[string]fstest.MapFS{
		"no version":        {"migrations/create.sql": {Data: []byte(`SELECT 1;`)}},
		"zero version":      {"migrations/0000_x.sql": {Data: []byte(`SELECT 1;`)}},
		"duplicate version": {"migrations/0001_a.sql": {Data: []byte(`SELECT 1;`)}, "migrations/0001_b.sql": {Data: []byte(`SELECT 1;`)}},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			if err := migrateFS(ctx, tdb.db, fsys, slog.New(slog.DiscardHandler)); err == nil {
				t.Fatal("migrateFS = nil error, want a naming error")
			}
		})
	}
}

// TestSchemaRejectsBadRows proves the CHECK constraints and STRICT typing are
// real: Go validates first with friendly messages, but the database is the
// last line of defence even if a future code path forgets.
func TestSchemaRejectsBadRows(t *testing.T) {
	ctx, tdb := newTestDB(t)
	if err := Migrate(ctx, tdb.db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	const good = "2026-10-15T09:00:00.000000Z"
	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"title too long", `INSERT INTO todos (title, created_at, updated_at) VALUES (?, ?, ?)`, []any{strings.Repeat("a", 201), good, good}},
		{"empty title", `INSERT INTO todos (title, created_at, updated_at) VALUES (?, ?, ?)`, []any{"", good, good}},
		{"done not 0 or 1", `INSERT INTO todos (title, done, created_at, updated_at) VALUES (?, ?, ?, ?)`, []any{"x", 2, good, good}},
		{"done is text (STRICT)", `INSERT INTO todos (title, done, created_at, updated_at) VALUES (?, ?, ?, ?)`, []any{"x", "yes", good, good}},
		{"timestamp wrong width", `INSERT INTO todos (title, created_at, updated_at) VALUES (?, ?, ?)`, []any{"x", "2026-10-15T09:00:00Z", good}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tdb.db.ExecContext(ctx, tt.sql, tt.args...); err == nil {
				t.Fatal("insert succeeded, want a constraint error")
			}
		})
	}
	// And a well-formed row is accepted, so the constraints are not simply rejecting everything.
	if _, err := tdb.db.ExecContext(ctx, `INSERT INTO todos (title, created_at, updated_at) VALUES (?, ?, ?)`, "Buy milk", good, good); err != nil {
		t.Fatalf("valid insert failed: %v", err)
	}
}

// testDB is a tiny helper around *sql.DB for one-value queries in tests.
type testDB struct {
	db *sql.DB
	t  *testing.T
}

// queryString runs a query that returns one value and returns it as text.
func (d *testDB) queryString(ctx context.Context, query string) string {
	d.t.Helper()
	var v string
	if err := d.db.QueryRowContext(ctx, query).Scan(&v); err != nil {
		d.t.Fatalf("%s: %v", query, err)
	}
	return v
}
