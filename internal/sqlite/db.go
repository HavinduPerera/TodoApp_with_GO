// Package sqlite is the SQLite side of the app: opening the database with the
// right per-connection settings, running the embedded migrations, and (from
// milestone 8) the todo.Store implementation.
//
// The driver is modernc.org/sqlite, a cgo-free port of SQLite (the C source
// machine-translated to Go). It is the app's one direct dependency; it brings
// modernc.org/libc and a few helpers with it (the `// indirect` lines in
// go.mod), all Go, which is why CGO_ENABLED=0 builds work: no C toolchain
// anywhere, a static binary on Linux, and a distroless image later.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	// The blank import runs the driver's init(), which calls sql.Register with
	// the name "sqlite". That is the standard library's whole plugin mechanism:
	// database/sql knows no drivers of its own; a driver announces itself by
	// being imported.
	_ "modernc.org/sqlite"
)

// uriPath escapes the characters SQLite's URI parser would otherwise interpret:
// '?' starts the query string, '#' the fragment, and %HH is decoded. Without
// this, a path such as data/todo#1.db would silently open data/todo instead.
var uriPath = strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F")

// Open opens (creating if needed) the SQLite database at path, a plain
// filesystem path (never a URI), and verifies the connection works. The
// returned *sql.DB is a pool, not a connection: sql.Open itself touches
// nothing, and PingContext is what first connects.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return nil, errors.New("sqlite: in-memory databases are not supported: a plain :memory: gives each pooled connection its own empty database, and shared cache is not worth its locking rules; use a file in t.TempDir()")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("sqlite: create dir: %w", err)
	}

	// PRAGMAs go in the DSN, not in a db.Exec("PRAGMA ..."): busy_timeout and
	// foreign_keys are per connection, and *sql.DB hands out whichever pooled
	// connection is free, so an Exec would configure one connection and leave
	// the others at their defaults. DSN pragmas run on every new connection.
	dsn := "file:" + uriPath.Replace(path) +
		"?_pragma=journal_mode(WAL)" + // readers never block the single writer; persisted in the file itself
		"&_pragma=busy_timeout(5000)" + // wait up to 5 s on SQLITE_BUSY instead of failing instantly
		"&_pragma=foreign_keys(1)" + // OFF by default in SQLite; needed the day users arrive
		"&_pragma=synchronous(NORMAL)" + // with WAL: no corruption, durable across app crashes; the last commits may be lost on power loss
		"&_txlock=immediate" // BEGIN IMMEDIATE: a transaction takes the write lock up front, so it can never hit the "database is locked" upgrade failure that ignores busy_timeout
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	// One connection, on purpose. SQLite allows one writer at a time. The
	// _txlock=immediate above already spares explicit transactions the
	// read-to-write upgrade failure (SQLITE_BUSY returned at once, without
	// honouring busy_timeout); one connection goes further: this process's
	// statements are serialised, so SQLITE_BUSY cannot arise between them at
	// all, and the pool is simple to reason about. It costs nothing for a single
	// user. The price is a rule: never start a second statement while a
	// *sql.Rows is open, because with one connection that is a hang, not a
	// wait. Every query in this package reads its rows to the end (or closes
	// them) before running anything else.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	return db, nil
}
