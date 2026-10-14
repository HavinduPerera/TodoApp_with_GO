# todoapp — Architecture

A single Go binary that owns its JSON API, its SQLite file, its migrations and a React + TypeScript UI embedded as the static files Vite builds, with the Go side written almost entirely from the standard library so every layer is readable by someone learning Go — and still shaped like a service a senior engineer would run (structured logs with request IDs, graceful shutdown, health/readiness, timeouts, one error envelope, tests at every layer, CI, a distroless image).

It grows out of the existing `cmd/api/main.go` in fifteen milestones, each of which leaves a running app. Every import arrow points inward to one small `internal/todo` package that imports only the standard library; HTTP and SQLite are replaceable edges.

Toolchain facts this document relies on: `go1.27.1 darwin/arm64`, module `todoapp`, no dependencies yet, `/usr/bin/sqlite3` 3.54, `modernc.org/sqlite` v1.60.1 (bundles SQLite 3.53.4), Node v24.14.0 + npm 11.9.0 (they build the UI; nothing of theirs is linked into the binary), Docker and make. Every Go 1.22–1.25 API named below was checked on this toolchain with `go doc` or a scratch program; every SQL shape was executed on sqlite3 3.54; `//go:embed all:dist` over a directory holding only `.gitkeep` was confirmed to compile (a plain `//go:embed dist` does not). On the frontend side, `create-vite` 9.2 scaffolds Vite 8.3 (Rolldown-based), `@vitejs/plugin-react` 6, React 19.3 and TypeScript 6.0, and Vitest 5.0 was added to it; the Vite config keys, the hashed `assets/` output, the absence of inline scripts, the 4 KiB `data:` inlining limit, the proxy's `changeOrigin` behaviour and `emptyOutDir` deleting dot-files were all checked on that build.

---

## 1. Goals & non-goals

**Goals**

- A real to-do app with Go as the backend: JSON API is the contract, a React UI is its first client, SQLite persistence survives restarts, and the deliverable is one static binary.
- A React + TypeScript UI built by Vite and embedded into that same binary (`//go:embed all:dist`), with a proxy-based dev loop: `make dev` runs Go on `:8080` and Vite on `:5173`, and the browser only ever talks to one origin.
- Fifteen incremental milestones starting from the existing `main.go`; each leaves a runnable app and teaches one legible Go idiom with a reason attached.
- Production shape from the start: `log/slog` with request IDs, graceful shutdown, `/healthz` + `/readyz`, server timeouts and body limits, one JSON error envelope, `go test -race` at every layer, CI, distroless Docker.
- Exactly one Go module linked into the binary (the pure-Go SQLite driver); every other module must earn its place with a concrete reason. npm packages build the UI and reach the binary only as static files.
- An obvious, non-rewrite path to multi-user + auth: ownership threads through as one parameter; the schema changes are two additive migrations.

**Non-goals**

- Horizontal scaling. SQLite plus startup migrations assume one process on one disk; crossing that line is the named trigger for PostgreSQL and a separate migrate step.
- Server-side rendering, Next.js or Remix. The UI is an API client; a static bundle needs no second server runtime.
- Deploying the frontend separately. The Go binary serves `dist/`, so there is one origin, one container and no CORS.
- A state-management library (Redux, Zustand) or a CSS framework (Tailwind). Hooks and ~100 lines of plain CSS cover one list; TanStack Query is the named upgrade when server-state caching matters.
- Tags, priorities, due dates, sharing. v1 is title + done; schema evolution is taught with one later migration (notes + completedAt), not up front.
- A web framework, ORM, DI container, mocking library, or down migrations (production rolls forward).

---

## 2. High-level overview

```
   browser: React SPA (frontend/ → internal/web/dist)    curl / tests (httptest)
     prod: served by Go at "/"                                      │
     dev:  served by Vite on :5173, which proxies /api → :8080      │
            │  fetch /api/...                                       │
            ▼                                                       ▼
 ┌────────────────────────────────────────────────────────────────────┐
 │ http.Server (Read/Write/Idle timeouts, ErrorLog → slog)            │
 │  internal/web                                                      │
 │   requestID → securityHeaders → logging → csrf → recover → ServeMux│
 │   /api/…  decode JSON / path / query  →  *todo.Service             │
 │           any error  →  writeError  →  one JSON envelope           │
 │   /       spa: embed.FS(dist) → hashed asset | file | index.html   │
 └────────────────────────────────┬───────────────────────────────────┘
                                  │ Go calls (ctx first)
                                  ▼
                   ┌──────────────────────────────┐
                   │ internal/todo  (stdlib only)  │ Todo, params, errors,
                   │   Service: validate, stamp UTC│ Store interface
                   └──────────────┬───────────────┘
                                  │ todo.Store (interface owned by the domain)
                 ┌────────────────┴─────────────────┐
                 ▼                                  ▼
   internal/memstore (M3–M8 only)        internal/sqlite (database/sql)
   RWMutex + map; the first Store,       WAL file at TODO_DB_PATH,
   deleted once sqlite passes the suite  embedded forward-only migrations
```

Dependency direction: `cmd/api` imports everything; `web` and `sqlite` both import `todo` and never each other; `todo` imports only the standard library. The domain is a leaf, so it is tested in microseconds and swapped under nothing. The React app under `frontend/` sits outside the Go module entirely (§3).

---

## 3. Repository layout

```
todoapp/
├── go.mod                        # module todoapp; go 1.27.1; require modernc.org/sqlite; tool honnef.co/go/tools/cmd/staticcheck; ignore ./frontend (M10)
│                                 # (the tool line adds indirect requires; only modernc.org/sqlite is linked into the binary)
├── go.sum
├── .gitignore                    # /api  /bin/  /data/  *.db  *.db-wal  *.db-shm  .env  .DS_Store  coverage.out  internal/web/dist/* (!.gitkeep)
├── .dockerignore                 # .git  bin/  data/  *.db*  .env  .vscode  .DS_Store  api  coverage.out  frontend/node_modules  internal/web/dist
├── .env.example                  # every TODO_* variable with its default; the Go binary never reads .env
├── Makefile                      # run dev build test cover vet lint fmt tidy check ui-install ui-dev ui-build ui-test ui-lint docker-build docker-run db-shell clean
├── Dockerfile                    # node:24-alpine ui (Vite build) → golang:1.27.1-alpine build → gcr.io/distroless/static-debian12:nonroot
├── README.md                     # run / configure / endpoint table / make targets / backup recipe / architecture summary
├── .github/workflows/ci.yml      # npm ci/lint/test/build, then gofmt check, vet, staticcheck, test -race -shuffle, govulncheck, docker build
├── .vscode/settings.json         # already present: gofmt + organizeImports on save (keep as is)
├── cmd/
│   └── api/
│       ├── main.go               # main() → run(ctx, lookupEnv, stderr) error → serve(ctx, ln, handler, …): wiring and lifecycle only
│       └── main_test.go          # M2: serve() answers /healthz on its listener and returns nil after ctx cancel
├── frontend/                     # React 19 + TypeScript: `npm create vite@latest frontend -- --template react-ts --eslint`. An npm project, not a Go package
│   ├── package.json              # scripts: dev, build (tsc -b && vite build), lint (eslint .), test (vitest, M11), preview; runtime deps: react, react-dom only (react-router arrives in M15)
│   ├── package-lock.json         # committed: `npm ci` in CI and Docker installs exactly these versions
│   ├── vite.config.ts            # plugins [react()]; build.outDir '../internal/web/dist' + emptyOutDir; server.proxy { '/api': { target: 'http://127.0.0.1:8080' } }; test { environment: 'jsdom', setupFiles } (M11)
│   ├── tsconfig*.json            # from the template (TS 6 is strict by default; erasableSyntaxOnly, verbatimModuleSyntax); eslint.config.js likewise (typescript-eslint, react-hooks, react-refresh)
│   ├── index.html                # Vite's entry: <div id="root"> + <script type="module" src="/src/main.tsx">; becomes dist/index.html with hashed asset URLs
│   ├── public/                   # copied into dist/ unhashed, AFTER Vite empties it: for files that must keep their name
│   │   ├── favicon.svg           # from the template; index.html already links /favicon.svg
│   │   └── .gitkeep              # empty; every build copies it to dist/.gitkeep, so emptyOutDir never leaves the committed placeholder deleted
│   └── src/
│       ├── main.tsx              # createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
│       ├── App.tsx               # useTodos() + AddTodoForm, FilterBar, TodoList, StatsFooter, ErrorBanner; no router until M15
│       ├── App.css               # plain CSS, system font stack, dark mode via prefers-color-scheme
│       ├── setupTests.ts         # jest-dom's Vitest matchers + afterEach(cleanup) (M11; §9 says why)
│       ├── api/
│       │   ├── client.ts         # request<T>(): JSON headers, error envelope → ApiError{status, code, message, fields?, requestId?}, 204 → undefined
│       │   ├── client.test.ts    # vi.stubGlobal('fetch', …): envelope → ApiError with fields; 204 → undefined
│       │   └── todos.ts          # Todo, Page<T>, Stats types mirroring §4; listTodos(filter), createTodo, updateTodo, deleteTodo, getStats
│       ├── hooks/
│       │   └── useTodos.ts       # items, total, loading, error, filter, offset/loadMore; optimistic toggle/delete with rollback on ApiError
│       └── components/
│           ├── AddTodoForm.tsx   # props { onAdd }: controlled input: trims, submits, shows the 422 fields.title message
│           ├── AddTodoForm.test.tsx
│           ├── TodoList.tsx
│           ├── TodoItem.tsx      # props { todo, onToggle, onRemove }: checkbox → onToggle; trash → onRemove; title rendered as a text child (React escapes it)
│           ├── TodoItem.test.tsx # checkbox calls onToggle; a title "<script>x</script>" renders as text
│           ├── FilterBar.tsx     # all / open / done → ?done= (M11)
│           ├── StatsFooter.tsx   # "showing N of total" + load more (M11)
│           └── ErrorBanner.tsx   # ApiError.message + requestId
└── internal/                     # enforced by the go command: no other module can import anything below
    ├── config/
    │   ├── config.go             # Config + Load(lookup func(string) (string, bool)) (Config, error)
    │   └── config_test.go        # map-backed lookup: defaults, overrides, joined errors
    ├── todo/                     # DOMAIN. Imports stdlib only (context, errors, fmt, strings, time, unicode, unicode/utf8)
    │   ├── todo.go               # Todo, CreateParams, UpdateParams, ListFilter, Page, Stats
    │   ├── errors.go             # ErrNotFound (sentinel), *ValidationError (typed)
    │   ├── validate.go           # title rules (rune count, control chars); ListFilter defaults
    │   ├── store.go              # type Store interface — declared here, by its consumer (M4, with the Service)
    │   ├── service.go            # Service: validation, UTC timestamps, Now field pinned by tests (M4)
    │   ├── service_test.go       # package todo_test (external): imports a real store without an import cycle
    │   └── todotest/
    │       └── contract.go       # RunStoreContract(t, newStore): one suite, incl. concurrent_writers, run against every Store
    ├── memstore/                 # M3–M8 ONLY. sync.RWMutex + map; the first Store; deleted in M8 (decision 6)
    │   ├── memstore.go
    │   └── memstore_test.go      # todotest.RunStoreContract
    ├── sqlite/
    │   ├── db.go                 # Open(ctx, path) (*sql.DB, error): MkdirAll, DSN pragmas, pool, Ping; rejects ":memory:"
    │   ├── migrate.go            # Migrate(ctx, db, logger) → migrateFS(ctx, db, fs.FS, logger): schema_migrations, one tx per file
    │   ├── migrations.go         # //go:embed migrations/*.sql
    │   ├── migrations/
    │   │   └── 0001_create_todos.sql
    │   ├── store.go              # Store{db}: Create/Get/List/Update/Delete/Stats; RETURNING; RowsAffected → ErrNotFound
    │   ├── scan.go               # scanTodo + formatTime/parseTime: the ONLY place time.Time ↔ TEXT happens
    │   ├── store_test.go         # todotest.RunStoreContract on a t.TempDir() file; PRAGMA assertions; CHECK fires
    │   ├── migrate_test.go       # idempotent; table AND index exist; fstest.MapFS with a bad file rolls back
    │   └── sqlitetest/
    │       └── db.go             # NewDB(t) *sql.DB: temp file + Open + Migrate + Cleanup — the test fixture for every layer above
    └── web/                      # TRANSPORT: JSON API + SPA file serving + middleware. Imports todo, log/slog, net/http, embed, io/fs. Never sqlite in non-test code.
        ├── server.go             # Deps{Service, Logger, Ready, Version, UI fs.FS (nil → embedded dist)}; NewServer(Deps) *Server; ServeHTTP; SetReady; builds the middleware chain
        ├── routes.go             # every mux.Handle call in one place, including the JSON 404/405 fallbacks and the "/" SPA registration
        ├── todos.go              # /api/todos handlers + request DTOs + query parsing
        ├── health.go             # GET /healthz, GET /readyz
        ├── respond.go            # handlerFunc, handle(), writeJSON, writeError, decodeJSON, apiError
        ├── middleware.go         # requestID, securityHeaders, logging (statusRecorder with Unwrap + Written), csrf, recoverPanics
        ├── spa.go                # //go:embed all:dist; newSPA(fsys fs.FS, version) reads index.html once; "/" handler: hashed asset | file | index.html fallback; 503 when unbuilt
        ├── dist/.gitkeep         # the ONLY committed file under dist/: keeps `all:dist` compiling on a fresh clone; Vite empties the directory on every build and frontend/public/.gitkeep puts this file back
        ├── todos_test.go         # package web_test: httptest.NewRecorder over NewServer + sqlitetest.NewDB
        ├── middleware_test.go
        ├── spa_test.go           # fstest.MapFS: index fallback (+ 304 on If-None-Match), immutable asset headers, missing asset and /assets/ → 404, /api/nope stays JSON, POST / → 405, no index.html → 503
        ├── decode_fuzz_test.go   # FuzzDecodeJSON, FuzzParseListFilter (M12)
        └── e2e_test.go           # httptest.NewServer + real sqlite file; closing and reopening the file proves persistence
```

**`cmd/api`** — Parses config, builds the logger, opens the DB, runs migrations, constructs `store → service → server`, listens, serves, shuts down gracefully. `main()` is six lines; `run()` returns `error`; `serve(ctx, ln, …)` is the testable lifecycle. Must not contain handlers, SQL or validation. ~110 lines at the end.

**`internal/config`** — `Config` plus `Load(lookup)`; defaults, parsing, validation with every error joined so the user sees all bad variables at once. Stdlib only; knows nothing about HTTP or the DB.

**`internal/todo`** — The entity, parameter types, domain errors, validation, the `Store` interface and the `Service` that applies business rules (trimming, timestamps, defaults). Must never import `net/http`, `database/sql` or `encoding/json`. Domain and service share one package on purpose: Go's unit of encapsulation is the package, not the file; a separate `domain` package would hold only structs and the service would be its only importer. When the app grows, add sibling packages per concept (`internal/user`, `internal/auth`), not horizontal layers (`services/`, `repositories/`).

**`internal/todo/todotest`** — The store conformance suite, exported so other packages' `_test.go` files can import it (the `net/http/httptest` pattern; a helper in a `_test.go` file cannot be imported across packages). Compiled into test binaries only.

**`internal/memstore`** — Thread-safe in-memory `todo.Store`. The real store for M3–M8: it teaches mutexes, copies-not-aliases and the race detector, and it is the first implementation the conformance suite is written against. Deleted in M8 once `sqlite.Store` passes the same suite, so every later feature is implemented once (decision 6).

**`internal/sqlite`** — `Open` (DSN pragmas + pool), the migration runner, the `Store`, and `sqlitetest.NewDB(t)` (a migrated temp-file database in a few milliseconds — the fixture for handler, service and e2e tests from M8 on). Translates `sql.ErrNoRows` / zero rows affected into `todo.ErrNotFound` and `time.Time` into fixed-width UTC text. Never imports `net/http`; never leaks `*sql.Rows`.

**`internal/web`** — Routing, JSON encode/decode hardening, error-to-status mapping, middleware, the SPA handler that serves Vite's build output out of `embed.FS`, health endpoints. Receives `*todo.Service`, a `Ready func(context.Context) error` and optionally a `UI fs.FS` (tests inject a `fstest.MapFS`; nil means the embedded `dist`); never imports `sqlite` outside tests. One package for API + SPA + middleware because at this size three packages would force exported helpers; split only when a second transport appears. `dist/` is build output, not source: Go sees it only through `//go:embed all:dist`, and nothing in the package knows what React is.

**`frontend/`** — The React + TypeScript client: a separate npm project outside the Go module. It knows the backend only as `/api/...` URLs and the JSON shapes of §4, mirrored as TypeScript types in `src/api/todos.ts`, so handler tests, curl and the UI exercise the same code path. `npm run build` writes into `../internal/web/dist`; that directory is the only thing the two worlds share. Never imported by Go, never linked: the binary contains files, not Node.

Two rules a learner can check by reading import blocks: `todo` is a leaf; `web` and `sqlite` are siblings that meet only through `todo.Store` and `*todo.Service`. "Accept interfaces, return structs": `todo.NewService(store Store)` accepts the tiny interface it needs; `sqlite.NewStore(db)` and `web.NewServer(deps)` return concrete types. There is exactly one hand-written interface in the app and no `Service` interface: handler tests use the real `*todo.Service` over a real store.

---

## 4. Domain model

### Go structs (`internal/todo/todo.go`)

```go
package todo

// Todo is the domain entity. JSON tags live here because today the wire shape
// equals the domain shape. The day they diverge (e.g. a UserID that must not be
// exposed), add a response DTO in internal/web; do not bend the domain.
type Todo struct {
	ID        int64     `json:"id"`        // int64: SQLite rowid is 64-bit; no narrowing casts anywhere
	Title     string    `json:"title"`     // trimmed, 1..200 runes, no control characters
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"createdAt"` // UTC, truncated to microseconds; set by Service, not by SQL
	UpdatedAt time.Time `json:"updatedAt"` // UTC; bumped by Service on every mutation
}

type CreateParams struct{ Title string }

// UpdateParams is a partial update. A nil pointer means "not supplied", which is
// how Go distinguishes {"done":false} from {}. JSON null also decodes to nil, so
// null is treated as absent (documented in the PATCH row of §5).
type UpdateParams struct {
	Title *string
	Done  *bool
}

type ListFilter struct {
	Done   *bool // nil = both
	Limit  int   // transport validates 1..200 (400 otherwise); Service only fills the default: 0 → 50
	Offset int   // >= 0
}

type Page struct {
	Items  []Todo `json:"items"`  // never nil: encodes as [] rather than null
	Total  int    `json:"total"`  // rows matching the filter, ignoring Limit/Offset
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

type Stats struct {
	Total int `json:"total"`
	Open  int `json:"open"`
	Done  int `json:"done"`
}

const (
	MaxTitleRunes = 200
	DefaultLimit  = 50
	MaxLimit      = 200
)
```

### Errors (`internal/todo/errors.go`)

```go
// ErrNotFound is a sentinel: callers test it with errors.Is.
var ErrNotFound = errors.New("todo not found")

// ValidationError carries per-field messages: callers extract it with errors.As.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string // "validation failed: title: must be 1–200 characters"
```

A sentinel is right for a condition with no payload; a typed error is right when the caller needs structured data. Stores wrap driver errors with `%w`, so `errors.Is(err, todo.ErrNotFound)` works through any number of `fmt.Errorf` layers.

### Service (`internal/todo/service.go`, M4)

```go
type Service struct {
	store Store
	// Now is exported so tests (in package todo_test and web_test) can pin time.
	// NewService sets it to func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }.
	// A functional-options constructor is the upgrade when a second knob appears; one knob does not earn it.
	Now func() time.Time
}

func NewService(store Store) *Service
func (s *Service) Create(ctx context.Context, p CreateParams) (Todo, error)            // validate, stamp both times
func (s *Service) Get(ctx context.Context, id int64) (Todo, error)
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error)                 // Limit 0 → DefaultLimit, then store.List
func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Todo, error)   // validate, store.Update(id, p, s.Now())
func (s *Service) Delete(ctx context.Context, id int64) error
func (s *Service) Stats(ctx context.Context) (Stats, error)
```

The Service is the one home for rules (validation, timestamps, defaults, later `CompletedAt` and ownership). Stores stay dumb and never read the clock, so two implementations cannot drift on "what time is it".

### Request DTOs (`internal/web/todos.go`) — transport shapes only

```go
type createTodoRequest struct{ Title string `json:"title"` }

type updateTodoRequest struct {
	Title *string `json:"title"`
	Done  *bool   `json:"done"`
}

type errorEnvelope struct {
	Error struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Fields    map[string]string `json:"fields,omitempty"`
		RequestID string            `json:"requestId,omitempty"`
	} `json:"error"`
}
```

### SQL DDL (`internal/sqlite/migrations/0001_create_todos.sql`)

```sql
-- Forward-only. The runner executes this file inside one transaction (SQLite DDL is transactional).
CREATE TABLE todos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,                        -- 64-bit rowid alias; AUTOINCREMENT: an id is never reused after DELETE
    title      TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),  -- length() counts characters, matching Go's rune count
    done       INTEGER NOT NULL DEFAULT 0 CHECK (done IN (0, 1)),        -- SQLite has no BOOLEAN; STRICT only allows INT/INTEGER/REAL/TEXT/BLOB/ANY
    created_at TEXT    NOT NULL CHECK (length(created_at) = 27),         -- '2026-10-14T09:00:00.123456Z': fixed width, UTC (see below)
    updated_at TEXT    NOT NULL CHECK (length(updated_at) = 27)
) STRICT;

-- Serves WHERE done = ? ORDER BY created_at DESC, id DESC and the filtered COUNT (covering).
-- The UNFILTERED list is a table scan plus a temp B-tree sort (verified with EXPLAIN QUERY PLAN), which is
-- irrelevant below ~100k rows. When it matters: CREATE INDEX todos_created_idx ON todos (created_at DESC, id DESC).
CREATE INDEX todos_done_created_idx ON todos (done, created_at DESC, id DESC);
```

Bookkeeping table, created by the runner itself (not by a numbered file):

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    applied_at TEXT    NOT NULL
) STRICT;
```

**UTC timestamps, fixed width.** Timestamps are TEXT so they are readable in the `sqlite3` shell and sort correctly — but only if the format is fixed width. `time.RFC3339Nano` trims trailing zeros, so `…09:00:00Z` sorts *after* `…09:00:00.5Z` (`Z` > `.`), which would misorder `ORDER BY created_at DESC`. The layout is therefore the constant `"2006-01-02T15:04:05.000000Z07:00"` in `scan.go`: always 27 characters for UTC, lexical order equals chronological order, microsecond precision round-trips through SQLite and later PostgreSQL. The Service produces `time.Now().UTC().Truncate(time.Microsecond)`, which also strips the monotonic reading. Tests compare with `.Equal`, never `==`. The `length() = 27` CHECK refuses a stray non-conforming write (binding a `time.Time` directly would produce a 36-char string and fail it, which is the point: everything goes through `scan.go`).

**IDs.** `INTEGER PRIMARY KEY AUTOINCREMENT`, `int64` end to end, returned via `RETURNING id`. Sequential ids keep URLs readable; AUTOINCREMENT costs one `sqlite_sequence` row and guarantees a deleted id is never handed out again. UUIDs are rejected: no stdlib generator beyond `crypto/rand` glue, worse index locality, and the multi-tenant "guessable id" concern is solved by scoping every query to `user_id` later.

**Hard delete.** `DELETE` removes the row; a second `DELETE` is 404 so a buggy client notices. Soft delete would put `deleted_at IS NULL` into every query for an undo feature nobody asked for; if it arrives it is additive.

**CHECK constraints duplicate Go validation on purpose.** Go gives a friendly 422 with field messages; the database guarantees the invariant even if a future code path forgets. `STRICT` makes SQLite reject `'abc'` in an INTEGER column instead of storing it silently.

---

## 5. HTTP API

### Conventions

- All API routes live under `/api/`. No `/v1` prefix: YAGNI for a single-client app; a second prefix can be registered later without touching the first.
- Requests with a body must send `Content-Type: application/json` (checked with `mime.ParseMediaType`, so `; charset=utf-8` is fine) → otherwise **415**. Bodies are capped at 64 KiB with `http.MaxBytesReader` → **413**. Unknown JSON keys → **400** (`json.Decoder.DisallowUnknownFields`: a typo like `"tittle"` fails loudly instead of being ignored). Trailing data after the first JSON value → **400**.
- Every `/api/` response is `Content-Type: application/json; charset=utf-8` and carries `X-Request-Id`. Bodies are marshalled to `[]byte` before headers are written, so `writeJSON` can set `Content-Length`, issue one `Write`, and share one code path with `writeError`. (`json.Encoder.Encode` also marshals fully before its single `Write`, so the current `main.go` is not wrong — the helper just centralises headers and status.)
- `{id}` must parse with `strconv.ParseInt(s, 10, 64)` and be `> 0`, else **400** `bad_request` (a syntax problem), not 404 (a well-formed id that does not exist).
- `GET` patterns also serve `HEAD` (ServeMux does this). `GET /api/todos/stats` and `GET /api/todos/{id}` coexist: the literal segment is more specific and wins, no registration-order tricks.

### Endpoints

| Method | Path | Request | Success | Errors |
|---|---|---|---|---|
| GET | `/healthz` | — | 200 `{"status":"ok","version":"a1b2c3d"}` — liveness; touches nothing | — |
| GET | `/readyz` | — | 200 `{"status":"ok"}` when `db.PingContext` (1 s timeout) succeeds and shutdown has not begun | 503 `{"status":"unavailable","reason":"db: …"}` or `"shutting down"` |
| GET | `/api/todos` | query `done=true\|false`, `limit` 1..200 (default 50), `offset` ≥ 0 | 200 `{"items":[Todo…],"total":12,"limit":50,"offset":0}`; order `createdAt DESC, id DESC`; `items` is `[]` when empty | 400 `bad_request` with `fields` naming the bad parameter (`limit=0`, `limit=999`, `limit=abc`, `offset=-1`, `done=maybe`) |
| POST | `/api/todos` | `{"title":"Buy milk"}` | 201, `Location: /api/todos/{id}`, body Todo | 400 malformed JSON / unknown field / empty body / wrong type; 413; 415; 422 `validation_failed` with `fields.title` |
| GET | `/api/todos/stats` | — | 200 `{"total":5,"open":3,"done":2}` (replaces the old `/count`) | — |
| GET | `/api/todos/{id}` | — | 200 Todo | 400 non-integer or ≤ 0 id; 404 `not_found` |
| PATCH | `/api/todos/{id}` | any non-empty subset of `{"title":"…","done":true}`; `null` is treated as absent | 200 updated Todo (`updatedAt` bumped) | 400 / 413 / 415 as POST; 404; 422 (no fields supplied — including `{}` and `{"done":null}` — → `fields.body`; bad title) |
| DELETE | `/api/todos/{id}` | — | 204, empty body | 400 bad id; 404 (a second DELETE is not silently idempotent) |
| GET | `/assets/{path...}` | — | 200 embedded Vite asset (content-hashed filename; served by the `/` SPA handler) with `Cache-Control: public, max-age=31536000, immutable` | 404 unknown file or any path ending in `/` (no directory listings); 405 for any method but GET/HEAD |
| GET | `/` and every other non-`/api/` path | — | 200 `text/html; charset=utf-8` `index.html` — the SPA fallback, so client-side routes and deep links load the app — with `Cache-Control: no-cache` + `ETag: "<version>"` (304 on `If-None-Match`; `/index.html` itself gets this fallback, never a redirect); a non-hashed file that exists in `dist/` (`favicon.svg`) is served with the same headers | 503 `text/plain` `UI not built: run make ui-build (or make dev)` when `dist/index.html` is missing; 404 for a dot-file (`/.gitkeep`); 405 for any method but GET/HEAD |
| any | `/api/todos`, `/api/todos/{id}` with another method | — | — | 405 JSON `method_not_allowed` + `Allow` header |
| any | other `/api/…` | — | — | 404 JSON `not_found` |
| POST/PATCH/DELETE | any path, cross-origin browser request | — | — | 403 JSON `forbidden` (M12 CSRF guard) |

Todo JSON shape, identical in every 2xx body:

```json
{"id":1,"title":"Buy milk","done":false,
 "createdAt":"2026-10-14T09:00:00.123456Z","updatedAt":"2026-10-14T09:00:00.123456Z"}
```

### Routing fallbacks (verified on this toolchain)

ServeMux's own 404 and 405 bodies are `text/plain`. Under `/api/` everything must be the envelope, so `routes.go` registers three method-less patterns beside the method-specific ones:

```go
mux.Handle("/api/todos",      jsonMethodNotAllowed("GET, POST"))          // same path as the GET/POST patterns: method-specific wins, no conflict
mux.Handle("/api/todos/{id}", jsonMethodNotAllowed("GET, PATCH, DELETE"))
mux.Handle("/api/",           jsonNotFound)                                 // method-less: POST /api/nope is 404 JSON, not a text 405
```

A `PUT /api/todos/7` gets `405` + `Allow: GET, PATCH, DELETE` as JSON; `POST /api/nope` gets JSON 404. (A `POST /api/todos/stats` falls into the `{id}` fallback; register `/api/todos/stats` method-less with `Allow: GET` if that header matters to you.)

The SPA handler (§7) is registered on the bare pattern `/`, which matches every path and is therefore the least specific pattern ServeMux has: `/healthz`, `/readyz`, every `/api/...` pattern and the method-less `/api/` fallback above all win over it. That is what keeps `GET /api/nope` a JSON 404 instead of `index.html`, and `spa_test.go` pins it.

### Error envelope (every non-2xx under `/api/`)

```json
{"error":{"code":"validation_failed","message":"title: must be 1–200 characters",
          "fields":{"title":"must be 1–200 characters"},"requestId":"K7QFJ3X2M4N5P6Q7R8S9T0UVWX"}}
```

| code | status | when |
|---|---|---|
| `bad_request` | 400 | malformed JSON, unknown field, wrong JSON type, empty body, trailing data, bad `{id}`, bad query parameter |
| `forbidden` | 403 | cross-origin unsafe request rejected by the CSRF guard (M12) |
| `not_found` | 404 | unknown id, unknown `/api/` path |
| `method_not_allowed` | 405 | known path, wrong method |
| `payload_too_large` | 413 | body > 64 KiB |
| `unsupported_media_type` | 415 | missing/wrong `Content-Type` on POST/PATCH |
| `validation_failed` | 422 | domain rules; always carries `fields` |
| `internal` | 500 | anything unexpected; `message` is the constant `"internal error"`, the real error is logged with the request id |
| `unavailable` | 503 | `/readyz` only |

### Validation rules

- Transport (`web`): JSON shape and size, `Content-Type`, `{id}` syntax, query params (`done` via `strconv.ParseBool`, `limit`/`offset` via `strconv.Atoi` with range checks → 400 with `fields.limit` etc.), PATCH must carry at least one non-null field.
- Domain (`todo`): `title` trimmed with `strings.TrimSpace`, 1..200 runes (`utf8.RuneCountInString`, never `len`), no control characters (`unicode.IsControl`); `Limit == 0` becomes 50. All failures accumulate into one `ValidationError` so the client sees every problem at once.
- Database: `CHECK` + `STRICT` as the last line of defence.

### Pagination, filtering, sorting

Offset pagination with `total` in the envelope: trivially understandable, maps to one SQL clause, and `total` is what the UI footer wants. Offset is O(offset) in SQLite, irrelevant below ~100k rows; the documented upgrade is keyset paging on `(created_at, id)` with an `after` cursor. Sorting is fixed (newest first, `id DESC` tiebreak); filtering is `done` only. Adding `q=` later touches `ListFilter`, one `WHERE` clause and the conformance suite.

### Health and the demo endpoint

`/healthz` answers "is the process up" and never touches the DB, so liveness probes do not flap on DB hiccups. `/readyz` pings the DB and consults an `atomic.Bool` that `serve()` flips to false before `Shutdown`. Both are outside `/api/` and logged at Debug so probe traffic does not drown the request log.

`GET /api/healthStatuses` and `HealthStatus` were a learning exercise: **dropped in M1**, replaced by `GET /healthz`. `GET /api/todos/count` is **renamed to `/api/todos/stats`** in M3 (decision 5).

---

## 6. Persistence

### Database: SQLite via `modernc.org/sqlite` (pure Go)

SQLite is the right shape for a single-user app and for learning: one binary plus one file, no server to install, a fresh database per test in `t.TempDir()` in milliseconds, backups are `sqlite3 todo.db ".backup out.db"` or `VACUUM INTO`, and it is a serious engine (WAL, transactions, CHECK, STRICT, `RETURNING`). PostgreSQL earns its keep the day the app runs on more than one machine; today it would cost `docker compose` for local dev, a pool to reason about and another moving part in CI, all of which teach ops rather than Go. The `todo.Store` interface plus the conformance suite make a later `internal/postgres` an addition (~200 lines: `$1` placeholders, `TIMESTAMPTZ`, `pgx`'s stdlib adapter, one line changed in `run()`), not a rewrite.

Driver comparison (`database/sql` ships no drivers, so one is unavoidable):

| driver | cgo | consequence | verdict |
|---|---|---|---|
| **`modernc.org/sqlite`** (driver name `"sqlite"`) | no | `CGO_ENABLED=0` works: static binary, trivial cross-builds, `distroless/static` image, no C toolchain for gopls, Docker or CI, `-race` just works. Costs: a large transitive `modernc.org/*` tree in `go.sum`, ~15 MB bigger binary, slow first compile, ~1.5–2× slower than cgo on write benchmarks (invisible here). | **chosen** |
| `github.com/mattn/go-sqlite3` (`"sqlite3"`) | yes | fastest, most battle-tested; forces gcc into every build image, painful cross-builds, no static image. | rejected |
| `github.com/ncruces/go-sqlite3` | no (WASM via wazero) | also cgo-free, smaller tree, less documented. | fine alternative; modernc is the better-trodden path for a learner searching for help |

### Connection setup (`internal/sqlite/db.go`)

```go
import _ "modernc.org/sqlite" // registers driver "sqlite" in init(); the blank import is the stdlib's plugin mechanism

func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return nil, errors.New("sqlite: :memory: is not supported: each pooled connection would get its own empty database; use a file in t.TempDir()")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("sqlite: create dir: %w", err)
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +   // readers never block the single writer; persisted in the file
		"&_pragma=busy_timeout(5000)" +  // wait up to 5 s on SQLITE_BUSY instead of failing instantly
		"&_pragma=foreign_keys(1)" +     // OFF by default in SQLite; needed the day users arrive
		"&_pragma=synchronous(NORMAL)" + // with WAL: no corruption, durable across app crashes; the last commits may be lost on power loss
		"&_txlock=immediate"             // BEGIN IMMEDIATE: a tx takes the write lock up front, so a deferred tx can
	                                     // never hit the "database is locked" upgrade failure that ignores busy_timeout
	db, err := sql.Open("sqlite", dsn) // sql.Open does not connect; PingContext does
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(1) // see below
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	return db, nil
}
```

**Pragmas belong in the DSN, not in `db.Exec("PRAGMA …")`.** `*sql.DB` is a pool and `busy_timeout` / `foreign_keys` are per connection: an `Exec` configures whichever connection it borrowed and leaves the others off. DSN pragmas run on every new connection. modernc silently accepts a misspelled DSN parameter, so `store_test.go` asserts `PRAGMA foreign_keys` = 1 and `PRAGMA journal_mode` = `wal` on a fresh connection.

**`SetMaxOpenConns(1)` — and the one rule it imposes.** SQLite allows one writer at a time; with several pooled connections a deferred transaction upgrading from read to write can fail with `SQLITE_BUSY` without honouring `busy_timeout`. One connection removes that whole class of errors and costs nothing for a single user. The price is a rule, not a slowdown: **never start a second statement while a `*sql.Rows` is open — with one connection it is a hang (verified: blocks until the context expires), not a wait.** Drain the loop or call `rows.Close()` first. Both recipes below are safe by construction: `List` runs `COUNT` before the row query; `Migrate` reads `schema_migrations` into a map and closes the rows before `BeginTx`. `migrate_test.go` runs `Migrate` under a 5 s context so a future nested query fails fast instead of hanging CI. Raising the limit later is safe because `_txlock=immediate` is already in the DSN; the real upgrade is a 1-connection writer pool plus an N-connection reader pool behind one `Store`, internal to `internal/sqlite`.

**Operational envelope:** startup migrations and SQLite both assume one process on one disk. Two replicas against one file is the trigger for PostgreSQL plus a separate `migrate` step. Never `cp` a live database — the `-wal` file holds committed data; use `.backup` or `VACUUM INTO`.

### Migrations: handwritten, embedded, forward-only (`migrate.go`, ~70 lines)

1. `//go:embed migrations/*.sql` → `embed.FS` (`migrations.go`).
2. `Migrate(ctx, db, logger)` calls unexported `migrateFS(ctx, db, fsys fs.FS, logger)` with the embedded FS — the `fs.FS` parameter is what lets `migrate_test.go` inject a `fstest.MapFS` with a deliberately broken file.
3. `CREATE TABLE IF NOT EXISTS schema_migrations …`; `SELECT version` into a `map[int]bool`; `rows.Close()` and `rows.Err()` **before** anything else runs (one-connection rule).
4. `fs.ReadDir(fsys, "migrations")` returns entries sorted by name; parse the leading `NNNN_` as the version (a malformed name is a startup error).
5. For each unapplied file in order: `tx, err := db.BeginTx(ctx, nil)`; `defer tx.Rollback()` (a no-op after Commit — the canonical Go transaction pattern); `tx.ExecContext(ctx, string(body))`; `INSERT INTO schema_migrations`; `tx.Commit()`. A failure rolls back that file and aborts startup with `migration 0002_…: <cause>`.
6. `run()` calls `Migrate` explicitly after `Open` and before the listener opens, logging `applied migration version=1 name=0001_create_todos.sql`.

The runner relies on modernc executing a multi-statement string in one `Exec` when no args are passed; `migrate_test.go` asserts via `sqlite_master` that both the table and the index from 0001 exist, so a driver change fails loudly. No down migrations: a broken migration is fixed by a new file. A library (`goose`, `golang-migrate`) would earn its place with a CLI, several databases or Go-code migrations; none applies here.

### Store interface (`internal/todo/store.go`, declared in M4)

```go
// Store is what the Service needs from persistence. It is declared here, by its
// consumer, with exactly the methods Service calls; implementations satisfy it
// implicitly and prove it with
//     var _ todo.Store = (*Store)(nil)
type Store interface {
	Create(ctx context.Context, t Todo) (Todo, error)                           // stores every field except ID; returns the row with ID set
	Get(ctx context.Context, id int64) (Todo, error)                            // ErrNotFound
	List(ctx context.Context, f ListFilter) (items []Todo, total int, err error) // items never nil; total ignores Limit/Offset
	Update(ctx context.Context, id int64, p UpdateParams, updatedAt time.Time) (Todo, error) // atomic partial update; ErrNotFound
	Delete(ctx context.Context, id int64) error                                 // ErrNotFound when zero rows affected
	Stats(ctx context.Context) (Stats, error)
}
```

Why it exists in M4 and not later: `Service` lives in `todo` and needs a field type for its store; `todo` cannot import `memstore` (which already imports `todo` — an import cycle the compiler rejects), so the interface is the only legal choice, and it is written from exactly the methods `Service` calls. The second implementation (sqlite, M8) proves it was the right shape.

Values, not pointers, cross the boundary: `Todo` is small and returning copies means an in-memory store can never hand out aliased state (a classic mutex-defeating bug). Every method takes `ctx` first and the sqlite store passes it to `QueryRowContext`/`ExecContext`, so a client disconnect cancels the statement through `r.Context()`.

### Implementation notes (`internal/sqlite/store.go`)

- `const cols = "id, title, done, created_at, updated_at"` and one `scanTodo(scanner) (todo.Todo, error)` helper used by every query (`interface{ Scan(dest ...any) error }` is satisfied by both `*sql.Row` and `*sql.Rows`). Scanning a TEXT column straight into `time.Time` fails with modernc, so `scan.go` parses the fixed layout explicitly.
- **Create:** `INSERT INTO todos (title, done, created_at, updated_at) VALUES (?, ?, ?, ?) RETURNING ` + cols via `QueryRowContext(...).Scan`; `done` bound as an explicit `int64` 0/1. `RETURNING` (SQLite ≥ 3.35) is the same idiom PostgreSQL uses and avoids a second round trip.
- **Update is one statement, so there is no read-modify-write window between concurrent PATCHes:**
  ```sql
  UPDATE todos SET title = COALESCE(?, title), done = COALESCE(?, done), updated_at = ?
  WHERE id = ? RETURNING id, title, done, created_at, updated_at
  ```
  A nil `*string` binds as NULL so `COALESCE` keeps the old value; the `*bool` is converted explicitly to `any(int64(0|1))` or `nil`. `sql.ErrNoRows` → `todo.ErrNotFound`.
- **Delete:** `ExecContext` then `RowsAffected() == 0` → `todo.ErrNotFound`.
- **List:** `WHERE` built from a `[]string` of clauses and a parallel `[]any` of args (never `fmt.Sprintf` with user input). First `SELECT COUNT(*) … WHERE …` → `total`; then the row query with `ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`; `defer rows.Close()`; loop `rows.Scan`; **`rows.Err()` after the loop** (a mid-iteration error is otherwise a silently short list); slice made with `make([]todo.Todo, 0, f.Limit)`. The two statements are separate autocommit transactions, so a write that lands between them can make `total` off by one relative to `items` — acceptable for a to-do list (the UI re-fetches); a `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})` around both is the upgrade if it ever matters.
- **Stats:** `SELECT COUNT(*), COALESCE(SUM(done), 0) FROM todos` — `SUM` over zero rows is NULL and scanning NULL into `int` fails, so the `COALESCE` is not optional. `open = total - done`.
- Every other error is wrapped with context: `fmt.Errorf("sqlite: get todo %d: %w", id, err)`.

### Transactions

Every v1 mutation is a single statement, which SQLite wraps in an implicit transaction; the only explicit transaction is in the migration runner. When a multi-statement unit of work appears (the first is the bootstrap-owner claim in M15), write the store methods against a tiny `dbtx` interface (`ExecContext`, `QueryContext`, `QueryRowContext`) satisfied by both `*sql.DB` and `*sql.Tx`, and add `WithTx(ctx, func(*Store) error) error` — no change to `todo.Store` for callers that do not need it.

---

## 7. Frontend

**Chosen: a React + TypeScript single-page app, built by Vite, embedded into the Go binary and served from `/`; in development Vite serves it and proxies `/api` to Go. The JSON API stays the only contract.**

### The app (`frontend/`)

- Scaffold: `npm create vite@latest frontend -- --template react-ts --eslint` — React 19, TypeScript (strict by default in TS 6; the template adds `erasableSyntaxOnly` and `verbatimModuleSyntax`), ESLint flat config, all as the template ships them. `--eslint` is load-bearing: create-vite ≥ 9 defaults its React templates to Oxlint, and the flag restores the ESLint set (`eslint` 10, `typescript-eslint`, `react-hooks`, `react-refresh`) this document relies on. One `npm install` right after scaffolding creates `package-lock.json`, which is committed; from then on everyone, CI and the Docker build run `npm ci`, because it installs exactly the locked versions and refuses to "fix" the lockfile; Node 24 LTS. Runtime dependencies are `react` and `react-dom` and nothing else until M15 adds `react-router` for `/login` and `/register`.
- `src/api/client.ts` — one `request<T>(method, path, body?)` around `fetch`: `Accept: application/json` always, `Content-Type: application/json` only when there is a body (GET must not carry one, and the API answers 415 to anything else), `credentials: 'same-origin'`. A 204 resolves to `undefined`; any other non-2xx parses the envelope of §5 into `class ApiError extends Error { status: number; code: string; fields?: Record<string, string>; requestId?: string }`, so a component can write `err instanceof ApiError && err.fields?.title`. Its fields are declared and assigned in the constructor body (the template's `erasableSyntaxOnly` rejects TypeScript parameter properties) and type-only imports are written `import type` (`verbatimModuleSyntax`). Nothing else in the app calls `fetch`.
- `src/api/todos.ts` — the TypeScript mirror of the Go structs in §4: `Todo { id: number; title: string; done: boolean; createdAt: string; updatedAt: string }`, `Page<T> { items: T[]; total: number; limit: number; offset: number }`, `Stats { total: number; open: number; done: number }`, and five typed functions: `listTodos(filter)`, `createTodo(title)`, `updateTodo(id, patch)`, `deleteTodo(id)`, `getStats()`. Timestamps stay strings and become `Date` at render time. `int64` ids are exact as JS numbers below 2^53, a ceiling `AUTOINCREMENT` will not reach; the day ids become UUIDs they become `string` here and in the JSON together.
- `src/hooks/useTodos.ts` — the one piece of client state: `{ items, total, loading, error, filter, setFilter, offset, loadMore, add, toggle, remove }` from `useState` + `useEffect` + `useCallback`. `toggle` and `remove` are optimistic: update `items` first, call the API, and on an `ApiError` put the previous array back and surface the error; an `AbortController` cancels an in-flight list request when the filter changes, so a slow old response cannot overwrite a new one. This is the hand-rolled core of what TanStack Query adds (cache, dedupe, retries, background refetch); adopt it when a second resource appears, not for one list.
- Components: `AddTodoForm({ onAdd })` (controlled input; trims; disabled while submitting; shows `fields.title` under the input when `onAdd` rejects with a 422 `ApiError`), `TodoList` → `TodoItem({ todo, onToggle, onRemove })` (checkbox → `onToggle(todo.id, !todo.done)`, trash → `onRemove(todo.id)`; the title is rendered as a text child, so React escapes it), `FilterBar` (all / open / done, M11), `StatsFooter` (`showing N of total` + load more, M11), `ErrorBanner` (`message` + `requestId`, so a bug report can be matched to one log line). `App` passes the hook's `add`, `toggle` and `remove` down as those props: state flows down, events come back as callbacks, `useTodos` is the only caller of `src/api/todos.ts`; no context, no store.
- `src/App.css` — plain CSS: system font stack, dark mode via `prefers-color-scheme`, ~100 lines. CSS Modules or Tailwind would add a second mental model for one page.

### Build output and embedding

- `vite.config.ts` sets `build.outDir: '../internal/web/dist'` and `emptyOutDir: true` (Vite refuses to wipe a directory outside its project root unless told to; the flag is the explicit consent). A build emits `index.html`, `assets/index-<hash>.js`, `assets/index-<hash>.css` and whatever `public/` holds; images under 4 KiB (`assetsInlineLimit`) are inlined as `data:` URIs, which is why the CSP below allows `img-src data:`.
- `internal/web/spa.go` holds `//go:embed all:dist`. The `all:` prefix is load-bearing: it makes `embed` include dot-files, so the committed `internal/web/dist/.gitkeep` counts as an embeddable file and the directive compiles on a fresh clone with no Node installed — a plain `//go:embed dist` over the same directory fails with "cannot embed directory dist: contains no embeddable files" (verified). Consequence: `go build`, `go vet` and `go test ./...` never need the UI; only `make build` and the Docker image do.
- `.gitignore` says `internal/web/dist/*` and then `!internal/web/dist/.gitkeep`: build output is never committed, the placeholder always is. `emptyOutDir` deletes everything in `dist/` except `.git`, dot-files included, so on its own the placeholder would vanish on the first `npm run build`, `git status` would show `D internal/web/dist/.gitkeep`, and one `git add -A` later a Node-less clone would fail `go build` while CI, which builds the UI first, stayed green. Hence a second empty file, `frontend/public/.gitkeep`: Vite copies `public/` into `dist/` after emptying it, so every build re-creates the placeholder and `git status` stays clean (verified on Vite 8.3). `make ui-build` fills the directory; `make dev` never touches it — in dev you open `localhost:5173`, which is what the 503 message's `(or make dev)` points to.
- `newSPA(fsys fs.FS, version string) *spa` is called by `NewServer` with `fs.Sub(distFS, "dist")`, or with `Deps.UI` when a test injects a `fstest.MapFS` — the same seam the migration runner uses. It reads `index.html` once with `fs.ReadFile` and keeps the bytes plus a precomputed `ETag: "<version>"`. A missing `index.html` is not a startup error: the handler answers **503** `text/plain` `UI not built: run make ui-build (or make dev)` until a build exists, so the API keeps working in a checkout that has never run npm, and a test binary never fails to start.

### Serving in production (Go)

The handler is registered on the method-less pattern `/`, the least specific pattern ServeMux has, so `/healthz`, `/readyz`, every `/api/...` route and the method-less `/api/` JSON-404 fallback all beat it (§5). Its rules, in order:

1. Method other than GET or HEAD → 405 with `Allow: GET, HEAD` (plain text: outside `/api/` the envelope rule does not apply). The SPA is read-only; every write goes to `/api/`.
2. Path ending in `/` other than `/` itself, or whose last segment starts with `.` → 404. No directory listings (`FileServerFS` would render one) and no serving of `/.gitkeep`.
3. Path under `/assets/` → `http.ServeFileFS` with `Cache-Control: public, max-age=31536000, immutable` when the file exists, otherwise 404. Vite puts a content hash in every filename there, so a URL changes whenever its bytes do and a year-long cache can never go stale; and a hashed name is never a client route, so a tab holding a stale `index.html` after a redeploy must get a loud 404 rather than HTML served as JavaScript, which `nosniff` would turn into a silent blank page.
4. Any other existing file except `index.html` (`favicon.svg`, anything from `public/`) → `ServeFileFS` with `Cache-Control: no-cache` + `ETag: "<version>"`. `embed.FS` files carry no modtime, so without a pre-set `ETag` a conditional request has nothing to compare; `ServeFileFS` checks `If-None-Match` against a pre-set `ETag` header (verified in `net/http/fs.go`), so repeat loads are cheap 304s and a stamped new build invalidates them (under `go run` the version is `dev`, but in dev the page comes from Vite anyway). `ServeFileFS` also rejects `..` segments for free — but it answers any path ending in `/index.html` with a 301 to `./` (`go doc ServeFileFS`), which is why `index.html` is excluded here.
5. Anything else, `/index.html` included → the cached `index.html` through `http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(s.index))` after setting `Content-Type: text/html; charset=utf-8`, `Cache-Control: no-cache` and the same `ETag`: `ServeContent` compares `If-None-Match` and answers 304, handles HEAD and `Content-Length`, and never redirects (verified). This is the SPA fallback: a deep link or a refresh on `/login` (M15) must load the app, and the app's router decides what to render — including its own "not found" view, which is why an unknown non-API path is a 200 here and never a 404.

### Development loop

- `make dev` is `$(MAKE) -j2 run ui-dev`: Go on `127.0.0.1:8080` with debug logs and Vite on `http://localhost:5173` with hot module replacement, in one terminal; one Ctrl-C stops both.
- `vite.config.ts` has `server.proxy: { '/api': { target: 'http://127.0.0.1:8080' } }`. The browser loads the page from `localhost:5173` and calls `/api/...` on `localhost:5173`; Vite forwards those to Go with the request headers unchanged. One origin in dev, one origin in prod, so there is **no CORS middleware anywhere** and nothing to keep in sync between environments. The object form is deliberate: Vite expands the string shorthand `'/api': 'http://127.0.0.1:8080'` to `{ target, changeOrigin: true }` (verified in Vite 8.3's source), which would rewrite `Host` on the way through; in the object form `changeOrigin` defaults to false.
- Edit a `.tsx` file and the component swaps in place; edit Go and it is Ctrl-C, `make dev` again. Go always serves whatever was embedded at build time — there is no flag that makes it read the UI from disk, because Vite is the UI's dev server.
- The CSRF guard (M12) passes through the proxy on both of its decision paths: `Sec-Fetch-Site: same-origin`, which `CrossOriginProtection` checks first, is forwarded intact; and were it ever missing, `Origin` (`http://localhost:5173`) still matches the preserved `Host`. With the string shorthand the second path fails (`Host` is `127.0.0.1:8080`), so the guard would hold only as long as `Sec-Fetch-Site` survives, and every write would become a 403 `forbidden` behind any proxy that dropped it — verified live with both configurations. That 403 is the symptom to recognise; the fix is `changeOrigin: false`, not `AddTrustedOrigin`.

### Security

- XSS: React renders `{todo.title}` as a text node and escapes it; `dangerouslySetInnerHTML` appears nowhere (grep for it in review — ESLint does not forbid it by default). `TodoItem.test.tsx` asserts that a title `<script>x</script>` is visible as text and that `container.querySelector('script')` is null, so the guarantee is pinned by a test rather than assumed.
- `securityHeaders` sets `Content-Security-Policy: default-src 'self'; img-src 'self' data:`. A Vite production build emits no inline scripts (the entry is `<script type="module" src="/assets/index-<hash>.js">`) and no inline styles (CSS is a linked file), so `'self'` holds; `img-src data:` admits the inlined images. `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin` as before. The header is set by Go, so check it on `make build && bin/api`, not on the Vite port.
- Same origin means no CORS; the JSON `Content-Type` requirement already blocks classic HTML-form CSRF; `http.NewCrossOriginProtection()` (Go 1.25) is installed in M12 so cookie auth later is safe by default.

**Why this and not the others**

| option | what it costs | when it is right |
|---|---|---|
| **React + TypeScript via Vite, embedded in the binary, Vite proxy in dev (chosen)** | Node and npm in the toolchain, a second lockfile, a three-stage Docker build, one more test runner (Vitest) | the UI is a real client of the API with typed wire shapes, components and tests of its own; the learner sees the Go side from the consumer's seat; deployment is still one binary and one origin |
| Vite's plain JavaScript template (no framework, same embedding) | still needs Node and the bundler, but list rendering and state become hand-rolled DOM code with no types on the wire and no component tests; outgrown by the first feature beyond one list | a single static page with one form |
| separate static hosting for the SPA + a CORS middleware + `cop.AddTrustedOrigin(spaOrigin)` | a second deployable with its own URL and TLS, a preflight on every write, two origins to keep aligned across dev, CI and prod | when the frontend ships on its own cadence or must sit on a CDN edge; nothing here blocks it — the CSRF guard rejects cross-origin writes before CORS is consulted, so both pieces are needed together |
| TanStack Query over the same `client.ts` | one more dependency and its cache vocabulary | the named upgrade when a second resource, background refetching or request dedupe arrives |
| Tailwind | a PostCSS pipeline and a utility vocabulary to learn | a design system shared across many pages |

---

## 8. Cross-cutting

### Configuration (`internal/config`) — environment only, typed, validated at startup

| variable | default | notes |
|---|---|---|
| `TODO_ADDR` | `127.0.0.1:8080` | loopback until auth exists; the Dockerfile sets `:8080` |
| `TODO_DB_PATH` | `./data/todo.db` | directory auto-created (M7) |
| `TODO_LOG_LEVEL` | `info` | parsed with `slog.Level.UnmarshalText` (`debug`, `warn`, …) |
| `TODO_LOG_FORMAT` | `text` | `text` for terminals, `json` for containers |
| `TODO_SHUTDOWN_TIMEOUT` | `10s` | `time.ParseDuration` |

```go
func Load(lookup func(string) (string, bool)) (Config, error)
```

`main` passes `os.LookupEnv`; tests pass a map. Errors are collected with `errors.Join`, each prefixed `config: TODO_X: …`, so one run reports every bad variable. The binary never reads `.env`: the Makefile does `-include .env` + `export`, Docker uses `-e`/`--env-file`. No flags (containers set env; `make` loads `.env`); decision 4 if you prefer `flag`.

### Version

`var version string` in `main.go`, set by `-ldflags "-X main.version=$(VERSION)"` from the Makefile and Dockerfile (`git describe --always --dirty`, or `dev`). `buildVersion()` returns it when set, else falls back to `vcs.revision` (+ `-dirty` when `vcs.modified`) from `runtime/debug.ReadBuildInfo()` for a bare `go build` inside the checkout, else `"dev"`. The fallback is needed because `go run` does not stamp VCS info and the Docker build context has no `.git` (excluded by `.dockerignore`), so without `-X` the container would report nothing.

### Logging — `log/slog`, injected, not global

- `run()` builds one `*slog.Logger` (`NewTextHandler` or `NewJSONHandler` on `stderr`, level from config), calls `slog.SetDefault(logger)` once so any stray `log.Printf` is structured too, and passes it explicitly into `web.Deps` and `sqlite.Migrate`. Packages never log through `slog.Default()`; the only reference is `web.NewServer`'s nil-Logger fallback, resolved once at construction.
- Startup line: `server listening addr=127.0.0.1:8080 db=data/todo.db version=a1b2c3d`.
- `http.Server.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)` so the server's own errors are structured too.
- One line per request from `logging`: `request method=PATCH path=/api/todos/3 pattern="PATCH /api/todos/{id}" status=200 bytes=187 duration_ms=1.4 request_id=K7QF…`. `r.Pattern` (Go 1.23) gives the route template for aggregation; the mux sets it on the request pointer `logging` holds, so it is read after `next.ServeHTTP` returns. 5xx at Error with the wrapped error; 4xx at Info; `/healthz` and `/readyz` at Debug.

### Request IDs

`requestID` honours an inbound `X-Request-Id` when it is 8..128 characters of `[A-Za-z0-9_-]` (a reverse proxy will set one), otherwise generates `crypto/rand.Text()` (Go 1.24; base32, documented as "at least 128 bits" and may grow — today 26 chars; tests assert `len >= 26` and `^[A-Z2-7]+$`, never `== 26`). It is stored in the context under an unexported `type ctxKey struct{}`, exposed by `requestIDFrom(ctx) string`, echoed in the `X-Request-Id` response header, and included in every error envelope and every 500 log line.

### Middleware chain (`internal/web/server.go`) — plain `func(http.Handler) http.Handler`, applied explicitly

```go
var h http.Handler = mux
h = recoverPanics(logger, h) // innermost: a panic becomes a 500 envelope BEFORE logging reads the status
h = s.csrf.Handler(h)        // M12: http.NewCrossOriginProtection; inside logging so a 403 is logged and carries the id
h = logging(logger, h)       // one line per request, written after the handler returns
h = securityHeaders(h)       // CSP, nosniff, Referrer-Policy
h = requestID(h)             // outermost: every line below, including the recover log, carries the id
```

- `logging` wraps `w` in `statusRecorder{http.ResponseWriter; status, bytes int; wroteHeader bool}` (status defaults to 200 when only `Write` is called), implements `Unwrap() http.ResponseWriter` so `http.ResponseController` (Flush, SetWriteDeadline) still reaches the real writer, and `Written() bool`. Because `logging` passes the recorder down the chain, `recoverPanics` receives it as its `w` and checks `w.(interface{ Written() bool })` before writing the 500 envelope — a panic after a partial write is logged but not double-written (no `superfluous WriteHeader`).
- `recoverPanics` re-panics `http.ErrAbortHandler` (the server's own "abort silently" sentinel); otherwise logs `debug.Stack()` at Error and writes the 500 envelope if nothing was written yet.
- `securityHeaders`: `Content-Security-Policy: default-src 'self'; img-src 'self' data:` (§7 explains both halves), `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, on every response — API and SPA alike.
- `csrf` (M12): `cop := http.NewCrossOriginProtection()` rejects cross-origin unsafe requests using the browser's `Sec-Fetch-Site`/`Origin` headers with no tokens and no library. Its default deny is a `text/plain` 403, so `cop.SetDenyHandler(s.handle(func(w, r) error { return &apiError{Status: 403, Code: "forbidden", Message: "cross-origin request rejected"} }))` keeps the envelope rule. curl and tests send neither header and pass; modern browsers always send `Sec-Fetch-Site`, so opening `127.0.0.1:8080` is same-origin as expected. Through the Vite proxy the browser is still same-origin (`Sec-Fetch-Site: same-origin`, and `Host` is preserved by the object-form proxy), so the guard passes without `AddTrustedOrigin` (§7).
- No CORS middleware: production and development are both single-origin (§7). No auth middleware yet; `requireUser` slots into the same chain in M15.

### Error handling convention, store → service → handler → JSON

1. **Domain** returns `todo.ErrNotFound` or `*todo.ValidationError`. No HTTP codes in `todo`.
2. **Store** maps `sql.ErrNoRows` / `RowsAffected()==0` → `todo.ErrNotFound`; wraps everything else with `fmt.Errorf("sqlite: <op> %d: %w", id, err)`. It never logs.
3. **Service** validates (returns `*ValidationError`), wraps store errors with the operation name, never logs.
4. **Handlers** have the signature `func(w http.ResponseWriter, r *http.Request) error` and are registered through `s.handle(fn) http.Handler`. Failing means `return err`, which removes the "forgot `return` after `http.Error`" bug class and puts the status policy in exactly one function. Transport-level failures are returned as `&apiError{Status, Code, Message, Fields}`. `http.Error` appears nowhere under `/api/`.
5. **`decodeJSON`** does the 415 check with `mime.ParseMediaType`, wraps the body in `http.MaxBytesReader(w, r.Body, 1<<16)`, uses `json.NewDecoder` + `DisallowUnknownFields`, and requires a second `Decode(&struct{}{})` to return `io.EOF`. It also owns the one string match in the app: `encoding/json` v1 reports an unknown field as a plain `errors.New("json: unknown field \"tittle\"")` with no exported type (verified; `encoding/json/v2` has a typed error, v1 does not), so

   ```go
   if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
   	return &apiError{Status: 400, Code: "bad_request", Message: "unknown field",
   		Fields: map[string]string{strings.Trim(name, `"`): "unknown field"}}
   }
   ```

   Without this the error would fall through to the 500 default. `todos_test.go` and `FuzzDecodeJSON` pin it.
6. **`writeError`** is the single translation point:

```go
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var ve *todo.ValidationError
	var mbe *http.MaxBytesError
	var se *json.SyntaxError
	var ute *json.UnmarshalTypeError
	switch {
	case errors.As(err, &ae):
		s.writeEnvelope(w, r, ae.Status, ae.Code, ae.Message, ae.Fields)
	case errors.As(err, &ve):
		s.writeEnvelope(w, r, 422, "validation_failed", ve.Error(), ve.Fields)
	case errors.Is(err, todo.ErrNotFound):
		s.writeEnvelope(w, r, 404, "not_found", "todo not found", nil)
	case errors.As(err, &mbe):
		s.writeEnvelope(w, r, 413, "payload_too_large", "body must be at most 64 KiB", nil)
	case errors.As(err, &se), errors.Is(err, io.ErrUnexpectedEOF):
		s.writeEnvelope(w, r, 400, "bad_request", "malformed JSON", nil) // never echo decoder text
	case errors.As(err, &ute):
		s.writeEnvelope(w, r, 400, "bad_request", "wrong type for field", map[string]string{ute.Field: "wrong type"})
	case errors.Is(err, io.EOF):
		s.writeEnvelope(w, r, 400, "bad_request", "request body is empty", nil)
	case (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && r.Context().Err() != nil:
		s.logger.DebugContext(r.Context(), "client went away", "request_id", requestIDFrom(r.Context()))
		return // nobody is listening; write nothing
	default:
		s.logger.ErrorContext(r.Context(), "unhandled error", "err", err, "request_id", requestIDFrom(r.Context()))
		s.writeEnvelope(w, r, 500, "internal", "internal error", nil) // the Go error text never reaches the client
	}
}
```

7. **`writeJSON`** marshals to `[]byte`, sets headers, `WriteHeader(status)`, one `Write`.
8. **`run()`** returns errors; `main` prints and `os.Exit(1)`. `log.Fatal` appears nowhere else (it skips deferred `db.Close()`).

### Server lifecycle, timeouts, graceful shutdown (`cmd/api/main.go`)

```go
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, lookupEnv func(string) (string, bool), stderr io.Writer) error {
	cfg, err := config.Load(lookupEnv)
	if err != nil { return err }
	logger := newLogger(stderr, cfg)
	slog.SetDefault(logger)

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil { return err }
	defer db.Close()
	if err := sqlite.Migrate(ctx, db, logger); err != nil { return err }

	svc := todo.NewService(sqlite.NewStore(db))
	srv := web.NewServer(web.Deps{Service: svc, Logger: logger, Ready: db.PingContext, Version: buildVersion()}) // UI nil → the embedded dist

	ln, err := net.Listen("tcp", cfg.Addr) // bind synchronously: a port clash is an immediate error, and tests can pass ":0"
	if err != nil { return fmt.Errorf("listen: %w", err) }
	logger.Info("server listening", "addr", ln.Addr().String(), "db", cfg.DBPath, "version", buildVersion())
	return serve(ctx, ln, srv, logger, cfg.ShutdownTimeout)
}

// serve runs the http.Server on ln until ctx is cancelled, then drains it. It is the unit main_test.go exercises.
func serve(ctx context.Context, ln net.Listener, srv *web.Server, logger *slog.Logger, shutdownTimeout time.Duration) error {
	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,   // slowloris
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,  // clock starts once headers are read: a slow handler is not stopped, its response just fails to be delivered (see below)
		IdleTimeout:       120 * time.Second, // keep-alive connections
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		// BaseContext is deliberately NOT set to ctx: that would cancel every in-flight
		// request the instant SIGTERM arrives, which is the opposite of graceful.
	}

	errCh := make(chan error, 1) // buffered: the goroutine can exit even if nobody reads
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	srv.SetReady(false) // /readyz → 503 so a load balancer stops routing before we close
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout) // fresh ctx: ctx is already cancelled
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err) // context.DeadlineExceeded if handlers overran
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("shutdown complete")
	return nil // run()'s deferred db.Close() runs after in-flight requests have finished
}
```

`Shutdown` stops accepting, waits for in-flight handlers, and returns `context.DeadlineExceeded` if they overrun `TODO_SHUTDOWN_TIMEOUT`. `WriteTimeout` bounds how long the response may take to be written (`go doc` says it "does not let Handlers make decisions on a per-request basis"); handler work is bounded by `r.Context()` (client disconnect) plus, where needed, an explicit `context.WithTimeout` in the handler. No `http.TimeoutHandler` (its buffered writer hides errors and breaks streaming).

---

## 9. Testing strategy

Principles: stdlib `testing` only; table-driven with `t.Run`; no assertion or mocking library (a six-line `wantStatus(t, rec, 201)` helper is enough; `google/go-cmp` is the first dependency worth adding if struct diffs get hard to read). Fake as little as possible: from M8 the fixture everywhere is `sqlitetest.NewDB(t)` — a real, migrated SQLite file in a temp dir, milliseconds per test — so handler, service and e2e tests all run on the production store; `memstore` is the fixture only in M3–M8. Real everywhere else: `ServeMux`, `encoding/json`, `*todo.Service`; the SPA handler gets a `fstest.MapFS` instead of the embedded `dist`, so its tests need no Node. `go test -race -shuffle=on -count=1 ./...` is the only Go test command (`-race` catches mutex mistakes, `-shuffle` order-dependent tests); `npm test --prefix frontend -- --run` is the only frontend one, and `make check` runs both. Use `t.Context()` (Go 1.24), `t.TempDir()`, `t.Cleanup`, `t.Helper()`, `.Equal` for times.

| layer | package / file | how | proves |
|---|---|---|---|
| Config | `config/config_test.go` | `Load(mapLookup)` table: defaults, each override, bad duration/level/format → joined error naming the variable | precedence and validation without touching the process env |
| Domain / service | `todo/service_test.go` (**`package todo_test`**, so importing a store is not an import cycle) | `svc := NewService(store); svc.Now = fixed` table: trims title; blank title; 201-rune title (200 × "é" passes); control chars; empty PATCH; `{"done":null}` counts as empty; `UpdatedAt == fixed` after update, `CreatedAt` unchanged; unknown id passes `ErrNotFound` through; `Limit 0 → 50` | business rules with deterministic time |
| Store conformance | `todo/todotest/contract.go`, called from `memstore_test.go` (until M8) and `sqlite/store_test.go` | `RunStoreContract(t, newStore func(*testing.T) todo.Store)` ~14 subtests: create returns id > 0; `create_preserves_done`; `list_empty_is_not_nil`; newest first with equal timestamps broken by id; two rows in the same second keep order (pins the fixed-width layout); Get unknown → `errors.Is(ErrNotFound)`; Update title only keeps done; Update with nil fields changes nothing but `updatedAt`; Delete twice → `ErrNotFound`; filter/limit/offset/total; stats on empty and non-empty; a non-UTC input round-trips `.Equal`; **`concurrent_writers`**: 20 goroutines × 10 `Create` plus interleaved `List`/`Stats` under a `sync.WaitGroup`, then `total == 200` and all ids unique — the subtest that gives `-race` something to race on | every implementation is interchangeable; SQL, scanning, time round-trip and thread-safety are right |
| SQLite specifics | `sqlite/store_test.go`, `sqlite/migrate_test.go` | `sqlitetest.NewDB(t)`; `PRAGMA foreign_keys` = 1 and `journal_mode` = `wal` on a fresh connection; a 201-char title or a 24-char timestamp via raw SQL fails the CHECK; `Migrate` twice → exactly N rows in `schema_migrations`; `sqlite_master` has `todos` **and** `todos_done_created_idx`; `migrateFS` over a `fstest.MapFS{0001_ok.sql, 0002_bad.sql}` returns an error naming `0002_bad.sql` and leaves exactly one row; all under a 5 s context so a nested-query hang fails instead of stalling CI | pragma correctness, idempotency, transactional failure, the one-connection rule |
| Handlers | `web/todos_test.go` (**`package web_test`**, proving the exported surface is sufficient) | `NewServer(Deps{Service: todo.NewService(sqlite.NewStore(sqlitetest.NewDB(t))), Logger: slog.New(slog.DiscardHandler), Ready: ok})`; `httptest.NewRequestWithContext(t.Context(), …)` + `httptest.NewRecorder()`; decode bodies into structs, never string-compare JSON; one case per row of the API table: 201 + `Location`; 400 malformed / **unknown field `tittle` with `fields.tittle`** / empty body / wrong type / bad id / each bad query param; 404; 405 JSON + `Allow`; 413 (65 KiB body); 415; 422 with exact `fields` incl. `{}` and `{"done":null}`; 204 then 404; stats; `items` is `[]` when empty; `/api/todos/stats` not captured by `{id}` | routing, status codes, envelope shape, decode hardening |
| Middleware | `web/middleware_test.go` | panicking handler → 500 envelope and the next request succeeds; panic after a partial write is logged, not double-written; `X-Request-Id` present, `len >= 26`, base32 shape, a valid inbound one echoed; one JSON log record (handler = `slog.NewJSONHandler(&buf, nil)`) with `status`, `pattern`, `request_id`; `statusRecorder` defaults to 200; `http.NewResponseController(w).Flush()` works through the wrapper; (M12) `Sec-Fetch-Site: cross-site` POST → 403 JSON `forbidden`; no `Sec-Fetch-Site` but an `Origin` whose host equals `Host` → 200, one that differs → 403 (the two paths the Vite proxy exercises, §7). `testing/slogtest` is not used: it validates custom `slog.Handler` implementations, and this app writes none | decorators do not break the writer, swallow panics or bypass the envelope |
| SPA handler | `web/spa_test.go` (`package web_test`) | `NewServer(Deps{…, UI: fstest.MapFS{"index.html": …, "assets/app-abc123.js": …, "favicon.svg": …, ".gitkeep": …}})`: `GET /` → 200 `text/html; charset=utf-8` with the index bytes, and with `If-None-Match: "<version>"` → 304; `GET /index.html` → 200 index, not a 301; `GET /assets/app-abc123.js` → 200, a JavaScript content type, `Cache-Control: public, max-age=31536000, immutable`; `GET /assets/missing.js` → 404; `GET /some/client/route` → 200 index.html (the fallback); `GET /favicon.svg` → `no-cache` + `ETag`, and `If-None-Match` with that ETag → 304; `GET /assets/` and `GET /.gitkeep` → 404; `GET /api/nope` → JSON 404, not index.html; `POST /` → 405; a MapFS without `index.html` → 503 `text/plain` | fallback routing and ServeMux precedence, embed wiring through the `fs.FS` seam, cache headers — none of it needs Node or a built UI |
| Frontend (Vitest) | `frontend/src/**/*.test.ts(x)`, run by `npm test -- --run` (`make ui-test`) | `jsdom` + Testing Library; `setupTests.ts` registers jest-dom's matchers and `afterEach(cleanup)` (RTL auto-cleans only when `afterEach` is a global, which Vitest's is not; without it the second `render` fails with "Found multiple elements" — verified). `api/client.test.ts` — `vi.stubGlobal('fetch', …)` replaces the network, so no server runs; an envelope body becomes an `ApiError` carrying `status`, `code`, `message`, `fields`, `requestId`; a 204 resolves to `undefined`; `components/AddTodoForm.test.tsx` — `onAdd` is a `vi.fn()`: it is called once with the trimmed title, and when it rejects with an `ApiError` whose `fields.title` is set, that message is rendered; `components/TodoItem.test.tsx` — clicking the checkbox calls `onToggle(todo.id, !todo.done)`; a title `<script>x</script>` is visible as text and `container.querySelector('script')` is null. `tsc -b` compiles the test files too (`tsconfig.app.json` includes all of `src/`), so a test that does not type-check fails `make build` and the Docker build — intended | the client parses the contract correctly, the components behave as a user sees them, and React's escaping is pinned by a test rather than assumed |
| Fuzz (M12) | `web/decode_fuzz_test.go` | `FuzzDecodeJSON` and `FuzzParseListFilter` with `testing.F`: no panics, every error maps to a 4xx (unknown field included) | hardening with stdlib tooling |
| End-to-end (M11) | `web/e2e_test.go` | one test: `httptest.NewServer(NewServer(…sqlite.NewStore(db)…))`; a real `http.Client` walks POST → 201 + `Location` → GET Location → PATCH done → list `?done=true` has 1 → stats → DELETE → 404; then `db.Close()`, `Open` + `Migrate` on the **same path** (a second `t.TempDir()` test could not: the dir is removed at cleanup), and the surviving rows are listed | wiring, migrations on a fresh DB, data survives a "restart" |
| Process (M2) | `cmd/api/main_test.go` | `ln := net.Listen("tcp", "127.0.0.1:0")`; `go serve(ctx, ln, web.NewServer(…Ready ok…), logger, 2*time.Second)`; GET `http://`+`ln.Addr()`+`/healthz` → 200 (proves it is actually serving, so the cancel test cannot pass vacuously); cancel; `serve` returns nil within the timeout. Any test that calls `run()` must set `TODO_DB_PATH` to a temp file or it creates `cmd/api/data/todo.db` in the source tree | graceful shutdown actually terminates |

Not tested on purpose: SQLite itself, `encoding/json`, ServeMux semantics beyond the precedence cases relied upon, React itself, and a real browser: a Playwright run against `make dev` is the optional browser-level upgrade when the UI grows past one page; today the Go e2e test covers the API end to end and Vitest covers the components. Coverage via `make cover` is a map, not a gate.

---

## 10. Tooling & delivery

### Makefile (tabs; `-include .env` + `export` pushes `KEY=value` lines into every recipe)

```make
-include .env
export

BIN     := bin/api
PKG     := ./cmd/api
UI      := frontend
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: run dev build test cover vet lint fmt tidy check ui-install ui-dev ui-build ui-test ui-lint docker-build docker-run db-shell clean

run:          ; go run $(PKG)
dev:          ; $(MAKE) -j2 TODO_LOG_LEVEL=debug run ui-dev
build: ui-build ; CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) $(PKG)
test:         ; go test -race -shuffle=on -count=1 ./...
cover:        ; go test -race -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
vet:          ; go vet ./...
lint:         ; go tool staticcheck ./...
fmt:          ; @out="$$(gofmt -l cmd internal 2>&1)"; test -z "$$out" || { printf '%s\n' "$$out"; echo "run gofmt -w cmd internal"; exit 1; } # stderr too: a parse error must fail
tidy:         ; go mod tidy && go mod verify
ui-install:   ; npm ci --prefix $(UI)
ui-dev:       ; npm run dev --prefix $(UI)
ui-build:     ; npm run build --prefix $(UI)
ui-test:      ; npm test --prefix $(UI) -- --run
ui-lint:      ; npm run lint --prefix $(UI)
check: fmt vet lint test ui-lint ui-test
docker-build: ; docker build --build-arg VERSION=$(VERSION) -t todoapp:local .
docker-run:   ; docker run --rm -p 8080:8080 -v todo-data:/data todoapp:local
db-shell:     ; sqlite3 $(or $(TODO_DB_PATH),data/todo.db)
clean:        ; rm -rf bin coverage.out
```

One-time: `go get -tool honnef.co/go/tools/cmd/staticcheck@latest` adds a `tool` line to `go.mod` (Go 1.24+) plus its `// indirect` requires, so everyone runs the same version; none of it is linked into the binary. Also one-time, and again whenever `package-lock.json` changes: `make ui-install` (`npm ci` installs exactly the locked versions and refuses to "fix" the lockfile; `npm install`, the only command that may rewrite it, runs once in M10 to create it and then only to add a dependency in M11 and M15). `npm --prefix frontend` runs the script with `frontend/` as the working directory, so there is no `cd` and no second Makefile. `build` depends on `ui-build`, so `bin/api` always carries a fresh UI; `dev` runs two long-lived recipes under `-j2`, which is why their output interleaves and one Ctrl-C reaches both. The go tool skips only `.`-prefixed, `_`-prefixed and `testdata` directories when it expands `./...`, so from M10 on it would descend into `frontend/node_modules`, where an npm package that happened to ship a `.go` file would break `go vet`, `staticcheck` and `go test` (verified: a stray `node_modules/pkg/p.go` fails `go list ./...`). `go.mod` therefore carries `ignore ./frontend` (a Go 1.25 directive; `go mod edit -ignore=./frontend`), which keeps every `./...` in this document honest; `gofmt` does not read `go.mod`, so `fmt` names `cmd internal` explicitly.

### Dockerfile (three stages: Node builds the UI, Go embeds it, distroless runs it; the Go builder is pinned to go.mod's exact version so `GOTOOLCHAIN` does not download another toolchain mid-build)

```dockerfile
FROM node:24-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci                               # cached layer: re-runs only when the lockfile changes
COPY frontend/ ./
RUN npm run build                        # outDir is ../internal/web/dist, so this writes /src/internal/web/dist

FROM golang:1.27.1-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download                      # cached layer: re-runs only when go.mod/go.sum change
COPY . .
COPY --from=ui /src/internal/web/dist ./internal/web/dist   # dist is in .dockerignore, so this is the only copy; placed after COPY . . so nothing overwrites it
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api \
 && mkdir -p /out/data                   # created here so it can be chown'd for the nonroot user below

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
COPY --from=build --chown=nonroot:nonroot /out/data /data   # a fresh named volume mounted here inherits this ownership;
VOLUME /data                                                 # without it, /data is root-owned and nonroot cannot create todo.db
ENV TODO_ADDR=:8080 TODO_DB_PATH=/data/todo.db TODO_LOG_FORMAT=json
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/api"]
```

`distroless/static` works only because the SQLite driver is pure Go. Neither Node nor the Go toolchain reaches the final image: the `ui` stage's only output is a directory, the `build` stage copies it in before `go build` so `//go:embed all:dist` picks up the real files, and the runtime image holds one binary with the UI bytes inside it. Image ≈ 15–20 MB (the UI adds well under 1 MB). Distroless has no shell, so there is no `HEALTHCHECK CMD curl`; point the orchestrator's HTTP probe at `/readyz`. Bind mounts need `chown 65532` on the host directory; named volumes do not. SIGTERM from `docker stop` reaches `run()` as PID 1 and triggers the graceful path.

### .gitignore

```
# A # starts a comment only at the beginning of a line; a trailing comment becomes part of the pattern.
# The stray 9 MB binary that lived in the repo root — deleted in M1, never committed:
/api
/bin/
/data/
*.db
*.db-wal
*.db-shm
.env
coverage.out
.DS_Store
# Vite's build output is never committed, except the placeholder that keeps
# //go:embed all:dist compiling without Node (frontend/public/.gitkeep re-creates it on every build):
internal/web/dist/*
!internal/web/dist/.gitkeep
```

`frontend/.gitignore`, as scaffolded by the template, ignores `node_modules`. `.dockerignore`: `.git`, `bin/`, `data/`, `*.db*`, `.env`, `.vscode`, `.DS_Store`, `api`, `coverage.out`, `frontend/node_modules`, `internal/web/dist` — the last two because the image builds both itself, and a stale local build must never leak into it.

### Static analysis

- `gofmt`: VS Code already formats on save with organizeImports (blank driver imports survive it); CI enforces with `gofmt -l`.
- `go vet ./...`: `copylocks` catches a `memstore.Store` copied by value, `lostcancel` a missing `defer cancel()`, `httpresponse` using `resp` before checking `err`, `slog` mismatched key/value pairs.
- `go tool staticcheck ./...` pinned via the `tool` directive.
- `govulncheck` in CI only (needs network): `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` — reports only vulnerabilities reachable from this binary, which matters precisely because the driver pulls a large tree.
- golangci-lint is deliberately not adopted: for one module its config becomes its own project. If it ever is, enable `errorlint, bodyclose, sqlclosecheck, rowserrcheck`.
- Frontend: `npm run lint` (ESLint flat config exactly as `--eslint` scaffolds it: `typescript-eslint`, `eslint-plugin-react-hooks`, `eslint-plugin-react-refresh`) and `tsc -b`, which `npm run build` runs first and which type-checks the test files as well — together they are the frontend's `vet`. No Prettier or Biome: the template does not ship one; add one the day two people edit `frontend/`.

### CI (`.github/workflows/ci.yml`)

```yaml
on: [push, pull_request]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7          # v7 is the current major of all three actions (GitHub releases API, 2026-10-14); they run on the node24 runtime
      - uses: actions/setup-node@v7
        with: { node-version: 24, cache: npm, cache-dependency-path: frontend/package-lock.json }
      - run: npm ci
        working-directory: frontend
      - run: npm run lint
        working-directory: frontend
      - run: npm test -- --run
        working-directory: frontend
      - run: npm run build               # fills internal/web/dist, so the Go steps below embed the real UI
        working-directory: frontend
      - uses: actions/setup-go@v7
        with: { go-version-file: go.mod, cache: true }
      - run: test -z "$(gofmt -l cmd internal)"
      - run: go vet ./...
      - run: go tool staticcheck ./...
      - run: go test -race -shuffle=on -count=1 -coverprofile=coverage.out ./...
      - run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...
  docker:
    needs: check
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - run: docker build --build-arg VERSION=${GITHUB_SHA::7} -t todoapp:ci .
```

`make check` runs the same lint and test steps as CI, Go and npm alike; CI's `npm run build` mirrors what `make build` does locally, and the Docker job builds the UI again inside its own stage, so a lockfile drift is caught in both places.

---

## 11. Milestones

Each milestone starts from a running app and ends with a check you run yourself. M1 starts from the existing `cmd/api/main.go`. The first two are deliberately small: your curriculum so far has no goroutines or channels, so graceful shutdown gets its own sitting.

| # | title | scope (files) | concept taught | done when |
|---|---|---|---|---|
| 1 | Housekeeping, `slog`, `run()`, `/healthz` | `rm ./api`; `.gitignore`; `git init`; `Makefile` (run/build/test/vet/fmt). `main.go`: `main()` → `run(lookupEnv, stderr) error` (still `ListenAndServe` inline, blocking); `newHandler() http.Handler` returns the mux; `slog` TextHandler replaces `log`; `ReadTimeout`/`WriteTimeout`/`IdleTimeout` join `ReadHeaderTimeout`; `TODO_ADDR` via `lookupEnv` (default `127.0.0.1:8080`); drop `/api/healthStatuses` + `HealthStatus`; add `GET /healthz`; keep the hardcoded slice, `GET /api/todos` and `/api/todos/count` untouched. `main_test.go`: `httptest.NewRecorder` against `newHandler()` for `/healthz`. | why `main` is tiny and `run` returns an error (defers run, no `log.Fatal`); what each `http.Server` timeout defends against; `slog` basics; a handler is testable without a socket. | `go vet ./... && make build` clean. `curl -si localhost:8080/healthz` → 200 `{"status":"ok"}`; `/api/healthStatuses` → 404; `/api/todos` still lists two todos. `make test` passes. `git status` does not list `api`. |
| 2 | Graceful shutdown | `main.go`: `run(ctx, …)`; `signal.NotifyContext` in `main`; `net.Listen` then `serve(ctx, ln, handler, logger, 10*time.Second)`: `Serve(ln)` in a goroutine with buffered `errCh`, `select` on `errCh`/`ctx.Done()`, `Shutdown` with a fresh timeout ctx, `http.ErrServerClosed` is success. `main_test.go`: the `serve` test from §9. | your first goroutine, channel and `select`, with a reason; context from OS signals; why `Shutdown` needs a fresh context; buffered channels prevent goroutine leaks; binding synchronously so `:0` is testable. | Ctrl-C logs `shutdown complete` and `echo $?` prints 0. `go test -race ./cmd/api` shows `TestServeShutsDown` GET `/healthz` on a random port, cancel, return nil. |
| 3 | Domain package + memstore + `web` package; behaviour identical | `internal/todo/{todo.go,errors.go}` (Todo with `int64` ID, `CreatedAt`, `UpdatedAt`; `ErrNotFound`); `internal/memstore/memstore.go` (`RWMutex` + map, `New() *Store`, seeded with the two existing titles, Get/List/Stats); `internal/web/{server.go,routes.go,todos.go,health.go,respond.go}` with `NewServer(Deps{Store *memstore.Store, Logger})` (concrete for now), `writeJSON` and a minimal `writeError` (`ErrNotFound` → 404, default 500); routes `GET /api/todos` (envelope), `GET /api/todos/{id}`, `GET /api/todos/stats` (replaces `/count`), `GET /healthz`; `main.go` becomes wiring; the `/healthz` test moves to `web`. | `internal/` visibility; package naming (short nouns, never `utils`/`models`); pointer receivers and never copying a struct that holds a mutex; `RWMutex` + `defer Unlock`; returning copies, not aliases; `r.PathValue` + `strconv.ParseInt`; literal-beats-wildcard precedence; `make([]T, 0)` so empty lists encode as `[]`. | `curl -s localhost:8080/api/todos` → `{"items":[…2…],"total":2,"limit":50,"offset":0}`; `/api/todos/2` → 200; `/api/todos/99` → 404 JSON; `/api/todos/abc` → 400; `/api/todos/stats` → `{"total":2,"open":1,"done":1}`; `/api/todos/count` → 400 `bad_request` (the `{id}` wildcard now captures `count`, and §5 says a non-integer id is a malformed request, not a missing todo). Temporarily add `p := memstore.New(); s := *p` in `main`, watch `go vet ./...` report `assignment copies lock value`, remove it (note: vet deliberately ignores `*memstore.New()` written directly — "a call may return a pointer to a zero value"). |
| 4 | Store interface, Service, write endpoints, the error envelope | `todo/store.go` (the interface — required now, see §6; written from exactly what Service calls), `todo/service.go` (`NewService`, `Now`, Create/Get/List/Update/Delete/Stats), `todo/validate.go`, `UpdateParams`, `ValidationError`; memstore `Create`/`Update`/`Delete`; `web/respond.go` complete (`handlerFunc`, `handle`, `decodeJSON` incl. the unknown-field match, `apiError`, the full `writeError` switch); `todos.go` POST (201 + `Location`), PATCH, DELETE (204); `routes.go` method-less fallbacks; `web.Deps` now takes `*todo.Service`; `todo/service_test.go` in `package todo_test` over memstore. | consumer-declared interfaces and why the import cycle forces one here; the handler-returns-error idiom; `json.Decoder` hardening (size, unknown fields, content type, single value) and the one stdlib wart that needs a string match; `errors.Is` for sentinels vs `errors.As` for typed errors and `%w`; pointer fields express absence in PATCH; `utf8.RuneCountInString` vs `len`; an injectable clock as a plain field; external test packages avoid import cycles. | `curl -si -X POST -H 'Content-Type: application/json' -d '{"title":"Buy milk"}' localhost:8080/api/todos` → 201 + `Location: /api/todos/3`; `-d '{"title":"  "}'` → 422 with `fields.title`; `-d '{"tittle":"x"}'` → **400** with `fields.tittle`; `-H 'Content-Type: text/plain'` → 415; `-d 'nope'` → 400; `-d '{"title":5}'` → 400. `curl -si -X PATCH … -d '{"done":true}' localhost:8080/api/todos/3` → 200 with `"done":true` and a later `updatedAt`; `-d '{}'` and `-d '{"done":null}'` → 422. `DELETE /api/todos/3` → 204, again → 404. `PUT /api/todos/1` → 405 JSON with `Allow`. `go test -race ./internal/todo/` green. |
| 5 | Conformance suite, handler tests, the race detector | memstore gains `var _ todo.Store = (*Store)(nil)`; `todo/todotest/contract.go` (incl. `concurrent_writers`, `create_preserves_done`); `memstore_test.go`; `web/todos_test.go` in `package web_test` (one case per API-table row); `Makefile test` becomes `-race -shuffle=on -count=1`. | interfaces are satisfied implicitly; compile-time assertions; table-driven tests with `t.Run`/`t.Helper`/`t.Parallel`; `httptest` without a socket; `t.Context`; `time.Equal`; `sync.WaitGroup`; the race detector only sees races that execute. | `go test -race -v -run TestStore ./internal/memstore` lists subtests such as `list_empty_is_not_nil` and `concurrent_writers`; `go test -race ./...` is `ok` for `todo`, `memstore`, `web`. Remove **both** the `Lock()` and its `defer Unlock()` from memstore `Create`, run `go test -race ./internal/memstore`: `concurrent_writers` reports `DATA RACE` on the map; restore them. |
| 6 | Config package, version, `.env` | `internal/config/{config.go,config_test.go}` (`Load(lookup)`, defaults, `slog.Level` and `time.Duration` parsing, `errors.Join`); `run()` uses config for addr, log level/format, shutdown timeout; `buildVersion()` with `-X` + `ReadBuildInfo` fallback; Makefile `VERSION`/`LDFLAGS`; version in the startup line and `/healthz`; `.env.example`; Makefile `-include .env`. | injecting `lookupEnv` instead of calling `os.Getenv` so tests do not mutate process state; `encoding.TextUnmarshaler` as the stdlib's extension point (`slog.Level` parses itself); `errors.Join`; `-ldflags -X` and why `go run` shows `dev`; `os.Exit` skips defers so it lives only in `main`. | `TODO_ADDR=:9090 make run` logs `addr=[::]:9090`; `TODO_LOG_FORMAT=json make run` emits JSON lines; `TODO_SHUTDOWN_TIMEOUT=abc go run ./cmd/api` exits 1 printing `config: TODO_SHUTDOWN_TIMEOUT: time: invalid duration "abc"`; `make build && bin/api` logs a git revision and `curl -s localhost:8080/healthz` shows it (`make run` shows `dev`); `go test ./internal/config` passes. |
| 7 | SQLite plumbing: driver, pragmas, embedded migrations, `/readyz` | `go get modernc.org/sqlite`; `internal/sqlite/{db.go,migrate.go,migrations.go,migrations/0001_create_todos.sql}`; `sqlite/migrate_test.go` (idempotent, table + index, `fstest.MapFS` failure, 5 s ctx); `run()` opens the DB from `TODO_DB_PATH` and calls `Migrate` (todos are **still served from memstore**); `web` gains `Ready func(ctx) error` and `ready atomic.Bool`; `GET /readyz`; `SetReady(false)` before `Shutdown`; Makefile `db-shell`. | `database/sql` is a pool (`sql.Open` is lazy, `Ping` connects); per-connection PRAGMAs via the DSN; `SetMaxOpenConns(1)` and the open-`Rows` rule; `embed` for non-Go files; `fs.FS` as the seam that makes the runner testable; transactional, idempotent migrations with `defer tx.Rollback()`; the blank import that registers a driver; STRICT and CHECK; what `CGO_ENABLED=0` buys. | `make run` creates `data/todo.db` (+ `-wal`, `-shm`); `sqlite3 data/todo.db 'select version,name from schema_migrations'` → `1\|0001_create_todos.sql`; `'pragma journal_mode'` → `wal`; `curl -si localhost:8080/readyz` → 200; `go test -race ./internal/sqlite` green; `CGO_ENABLED=0 go build ./cmd/api` succeeds. |
| 8 | SQLite store; memstore retires | `internal/sqlite/{store.go,scan.go}`; `sqlite/store_test.go` runs the conformance suite plus pragma/CHECK assertions; `internal/sqlite/sqlitetest/db.go`; `run()` wires `sqlite.NewStore(db)`; `todo/service_test.go` and `web/todos_test.go` switch to `sqlitetest.NewDB`; `internal/memstore` deleted (decision 6). Optional: seed the two original titles when the table is empty behind `TODO_SEED=true`. | `QueryRowContext` + `RETURNING`; `defer rows.Close()` and `rows.Err()`; `COUNT` before rows (one-connection rule); mapping driver errors to domain errors at the boundary; fixed-width UTC timestamps so TEXT sorts correctly; a conformance suite is what makes two implementations interchangeable — and lets you retire one. | POST a todo, Ctrl-C, `make run` again, `curl localhost:8080/api/todos` still lists it. `go test -race ./internal/sqlite` passes the same conformance subtests memstore did, plus `foreign_keys = 1` and the CHECK-violation cases; `concurrent_writers` passes on sqlite (the single connection serialises writers). `go test -race ./...` green with memstore gone. |
| 9 | Middleware: request ID, structured request log, recover, security headers | `web/middleware.go` (`requestID` with inbound validation, `securityHeaders`, `logging` with `statusRecorder` + `Unwrap` + `Written` + `r.Pattern`, probes at Debug, `recoverPanics` re-panicking `http.ErrAbortHandler`); chain in `server.go`; `writeError` puts `requestId` in envelopes and 500 logs; `ErrorLog: slog.NewLogLogger(...)`; `web/middleware_test.go`. | `func(http.Handler) http.Handler` and closures; why order matters (recover innermost, request ID outermost); wrapping `ResponseWriter` without losing `Flusher`/`ResponseController`; `context.WithValue` done right (unexported key type, typed accessor); `defer`/`recover` semantics; `crypto/rand.Text`. | every response carries `X-Request-Id`; `curl -H 'X-Request-Id: abc12345' -si localhost:8080/healthz` echoes it; each request logs one line with `status`, `pattern`, `request_id`; `go test -race -run TestRecover ./internal/web` shows a panicking handler yields a 500 envelope and the next request succeeds; `curl -sI localhost:8080/api/todos \| grep -i x-content-type-options` → `nosniff`. |
| 10 | React UI: Vite scaffold, typed API client, list/add/toggle/delete, Vite proxy dev loop, Go SPA handler embedding `dist`, Makefile `ui-*` targets | `npm create vite@latest frontend -- --template react-ts --eslint`, then `cd frontend && npm install` once to create `package-lock.json` (committed); delete the template's `src/index.css` (and its import in `main.tsx`), `src/assets/` (and the imports in `App.tsx`) and `public/icons.svg`; keep `public/favicon.svg`, which `index.html` already links; add the empty `public/.gitkeep`; `vite.config.ts` (`build.outDir '../internal/web/dist'`, `emptyOutDir: true`, `server.proxy { '/api': { target: 'http://127.0.0.1:8080' } }`); `src/api/client.ts` (`request<T>`, `ApiError`), `src/api/todos.ts` (types + `listTodos`/`createTodo`/`updateTodo`/`deleteTodo`/`getStats`), `src/hooks/useTodos.ts` (list, add, optimistic toggle/remove with rollback), `App.tsx`, `AddTodoForm`, `TodoList`, `TodoItem`, `ErrorBanner`, `App.css`; `internal/web/spa.go` (`//go:embed all:dist`, `newSPA(fs.FS, version)`, the five rules of §7, 503 when unbuilt), `internal/web/dist/.gitkeep`, `.gitignore` `internal/web/dist/*` + `!…/.gitkeep`, `go mod edit -ignore=./frontend`, `Deps.UI fs.FS`, `routes.go` registers `/`, CSP gains `img-src 'self' data:`; `web/spa_test.go` over `fstest.MapFS`; Makefile `UI := frontend`, `ui-install ui-dev ui-build ui-lint`, `build: ui-build`, `dev: $(MAKE) -j2 TODO_LOG_LEVEL=debug run ui-dev`, `fmt` over `cmd internal`. | a SPA as an API client: the JSON contract mirrored as TypeScript types, one `fetch` wrapper that turns the envelope into a typed error; `embed` + `fs.FS` for build output (`all:` and the two `.gitkeep`s, so Go tooling never needs Node; the same `fs.FS` seam as the migration runner); SPA fallback routing and why `/` is the least specific ServeMux pattern; HTTP caching: `immutable` for content-hashed assets, `no-cache` + `ETag`/304 for the rest; why the Vite proxy removes CORS (one origin in dev and prod, §7); optimistic updates with rollback; React escapes text, so the XSS guarantee lives in the UI and its test. | `make ui-install && make dev`; at `http://localhost:5173/` add, toggle and delete work without a reload and show up in `curl -s localhost:8080/api/todos`; a todo titled `<b>bold</b>` displays literally; an empty title shows the 422 message under the input; the Network tab shows every call going to `localhost:5173/api/...`; edit `TodoItem.tsx` and the change appears without a reload. `make build && cd /tmp && TODO_DB_PATH=/tmp/x.db /Users/havinduperera/Personal/todoapp/bin/api` serves the same UI at `http://127.0.0.1:8080/` from another cwd, and `git status` is clean after that build (`dist/.gitkeep` came back); `curl -si localhost:8080/assets/` → 404; `curl -si localhost:8080/some/route` → 200 `text/html`; `curl -si localhost:8080/index.html` → 200, not 301; `curl -si localhost:8080/api/nope` → JSON 404; `curl -sI "localhost:8080/assets/$(ls internal/web/dist/assets \| grep '\.js$' \| head -1)"` shows `immutable`; `curl -si localhost:8080/assets/nope.js` → 404; `curl -si -X POST localhost:8080/` → 405. `git clean -xfd internal/web/dist && go test -race ./...` is still green with only `.gitkeep` left in `dist/`, and `go run ./cmd/api` then answers `curl -si localhost:8080/` with 503 `UI not built…` while `/api/todos` keeps working; `make ui-build` restores the page. `npm run lint --prefix frontend` clean; `go test -race ./...` green. |
| 11 | Filtering & pagination, API and React UI; Vitest suite; end-to-end test | query parsing in `todos.go` (`done`, `limit`, `offset` → `ListFilter` with per-field 400s); Service default `Limit 0 → 50`; sqlite `List` with clauses+args `WHERE` and `COUNT` first; conformance cases for filter/limit/offset/total; frontend: `listTodos({ done, limit, offset })` builds the query with `URLSearchParams`, `FilterBar` (all / open / done → `?done=`), `StatsFooter` (`showing N of total` + "load more" that advances `offset` and appends), `useTodos` resets `offset` on a filter change and aborts the stale request; `npm i -D vitest @testing-library/react @testing-library/jest-dom jsdom`, `test: { environment: 'jsdom', setupFiles: ['src/setupTests.ts'] }` in `vite.config.ts` (`defineConfig` from `vitest/config`), the `"test": "vitest"` script and the Makefile `ui-test` target, `src/setupTests.ts` (`import '@testing-library/jest-dom/vitest'`, then `afterEach(cleanup)` with `afterEach` from `vitest` and `cleanup` from `@testing-library/react`); `src/api/client.test.ts`, `src/components/AddTodoForm.test.tsx`, `src/components/TodoItem.test.tsx`; `web/e2e_test.go` with the close-and-reopen step. | safe dynamic SQL (clauses + args, never `Sprintf`); `url.Values` parsing with explicit validation; validate at the edge, default in the service; offset paging and its limits (keyset documented as the upgrade); `httptest.NewServer` and a real `http.Client`; `EXPLAIN QUERY PLAN`; on the client: `AbortController` against stale responses, Testing Library's "test what the user sees" (query by role and label, never by class), `vi.stubGlobal('fetch', …)` for the client and a `vi.fn()` prop for the components as the whole fake — no server, no mocking library. | `curl -s 'localhost:8080/api/todos?done=false&limit=2&offset=2'` returns the right page and `total`; `?limit=abc`, `?limit=0`, `?limit=999` → 400 with `fields.limit`. Seed 60 todos (`for i in $(seq 60); do curl -s -X POST -H 'Content-Type: application/json' -d "{\"title\":\"t$i\"}" localhost:8080/api/todos >/dev/null; done`): in the UI the filter switches the list, "load more" appends the next page and the footer reads `showing 60 of 60`. `npm test --prefix frontend -- --run` lists `client.test.ts`, `AddTodoForm.test.tsx` and `TodoItem.test.tsx` green, including the `<script>x</script>`-renders-as-text case. `go test -race -run TestEndToEnd ./internal/web -v` shows the steps and the reopened file still has the rows. `sqlite3 data/todo.db 'explain query plan select * from todos where done=0 order by created_at desc, id desc limit 50'` mentions `todos_done_created_idx`; the same without `where` shows `SCAN` + `TEMP B-TREE` — now you know what the index does and does not do. |
| 12 | Hardening: fuzzing, CSRF guard, README | `decode_fuzz_test.go`; `http.NewCrossOriginProtection()` inside the chain with `SetDenyHandler` writing the `forbidden` envelope; middleware tests for it (cross-site → 403; no `Sec-Fetch-Site` with `Origin` matching `Host` → 200, differing → 403); `README.md` with the curl cookbook, the `make dev` loop and the backup recipe. | `testing.F` fuzzing; `Sec-Fetch-Site` CSRF protection in the stdlib, its `Origin`-vs-`Host` fallback, and why it sits inside the logging/request-id layers; why the proxied dev UI needs no `AddTrustedOrigin` (§7). | `go test -fuzz=FuzzDecodeJSON -fuzztime=10s ./internal/web` finds no crashers. `curl -si -X POST -H 'Sec-Fetch-Site: cross-site' -H 'Content-Type: application/json' -d '{"title":"x"}' localhost:8080/api/todos` → 403 JSON `{"error":{"code":"forbidden",…}}` with an `X-Request-Id`; the same without the header → 201; `-H 'Origin: http://evil.example'` and no `Sec-Fetch-Site` → 403, `-H 'Origin: http://localhost:8080'` → 201; the React UI through the Vite proxy still works (`make dev`, add a todo at `http://localhost:5173/` → 201), and so does the built UI at `http://127.0.0.1:8080/`. |
| 13 | Docker (three stages), CI, staticcheck, govulncheck | `Dockerfile` (`node:24-alpine AS ui` → `golang:1.27.1-alpine AS build` with `COPY --from=ui /src/internal/web/dist ./internal/web/dist` → distroless; `ARG VERSION`), `.dockerignore` (+ `frontend/node_modules`, `internal/web/dist`), `go get -tool honnef.co/go/tools/cmd/staticcheck@latest`, Makefile `lint`/`check` (`fmt vet lint test ui-lint ui-test`)/`cover`/`docker-*`, `.github/workflows/ci.yml` (`setup-node` + `npm ci`/`lint`/`test`/`build` in `frontend/`, then the Go steps); fix anything staticcheck reports; README deploy section. | static Go binaries and why pure-Go SQLite made distroless possible; a multi-stage build as a pipeline: the Node stage's only output is a directory the Go stage copies in before `go build`, and neither toolchain reaches the final image; layer caching of `npm ci` and `go mod download` alike; running as non-root and the volume-ownership gotcha; configuring via env in containers; pinning the builder to go.mod's version; Go 1.24 `tool` directives; what staticcheck catches that vet does not. | `make docker-build` succeeds and `docker images todoapp:local` is well under 50 MB; `make docker-run`, open `http://localhost:8080/` and the React UI loads from the container (`curl -s localhost:8080/ \| head -c 200` shows `<div id="root">`), create a todo, `docker stop`, run again → the todo persists; logs are JSON lines; `curl -s localhost:8080/healthz` shows the git revision, not `dev`; `make check` passes locally (Go and npm); the first push shows a green Actions run with the npm steps before the Go ones. |
| 14 | (optional) Schema evolution: `completedAt` + `notes` | `migrations/0002_add_completed_at_notes.sql`: `ALTER TABLE todos ADD COLUMN notes TEXT NOT NULL DEFAULT ''; ALTER TABLE todos ADD COLUMN completed_at TEXT; UPDATE todos SET completed_at = updated_at WHERE done = 1;` (a cross-column CHECK cannot be added by `ALTER` once rows exist; the invariant lives in the Service and the store's `CASE`). `Todo.Notes`, `Todo.CompletedAt *time.Time`; sqlite `Update` binds with `sql.Named`: `completed_at = CASE WHEN :done IS NULL THEN completed_at WHEN :done = 1 AND done = 0 THEN :now WHEN :done = 0 THEN NULL ELSE completed_at END` (SQLite named parameters are `:name`/`@name`/`$name`; `?N` must be numeric — `?done` is a syntax error); conformance cases for the flip; `notes` ≤ 2000 runes. | evolving a schema through the migration runner; nullable columns via `sql.Null[string]`; named parameters bind one value in several places; what `ALTER TABLE` can and cannot do in SQLite. | restart twice: `select count(*) from schema_migrations where version=2` → 1; PATCH `{"done":true}` sets `completedAt`, `{"done":false}` clears it, `{"title":"x"}` leaves it alone; existing done todos got `completedAt` backfilled; `go test -race ./...` green. |
| 15 | (stretch, after confirmation) Users, sessions, per-user todos | see §12: migrations 0003/0004; `internal/user`, `internal/auth`; `todo.Store` and `Service` gain `ownerID`; `requireUser` middleware on `/api/todos…` and `/api/me`; `POST /api/auth/register` (claims the bootstrap owner on first use), `/login`, `/logout`, `GET /api/me`; frontend: `npm i react-router` (the first runtime dependency beyond React), `<BrowserRouter>` with `/`, `/login`, `/register`, a `useAuth` hook over `GET /api/me`, `LoginPage`/`RegisterPage`, the header shows the signed-in email and a logout button; `client.ts` turns any 401 into a redirect to `/login` through an `onUnauthorized` callback that `App` installs; the Go SPA fallback already serves `index.html` for `/login`, so `spa.go` does not change; e2e test with two users. | server-side sessions vs JWT — `HttpOnly` cookie sessions are the right choice for a same-origin SPA: the browser attaches the cookie and JavaScript can never read it, whereas a JWT kept in `localStorage` is readable by any injected script, the XSS anti-pattern; cookie attributes; password hashing choices; authorization by filtering in SQL (IDOR → 404, never 403); additive migrations with backfills; the first `WithTx`; client-side routes on top of the SPA fallback; using compiler errors as a refactoring checklist. | the todos you created before M15 are visible after registering at `http://127.0.0.1:8080/register` and logging in as the first user; a refresh on `/login` still loads the app (SPA fallback); user B lists → empty, GET/PATCH/DELETE A's id → 404; `curl -si localhost:8080/api/todos` without a cookie → 401 envelope, and the UI redirects to `/login` on the same 401; `select count(*) from todos where user_id is null` → 0; `npm test --prefix frontend -- --run` and `go test -race ./...` green. |

---

## 12. Path to multi-user + auth

Nothing is rewritten. Four things are added and one parameter threads through; the compiler lists every call site.

### 1. Two additive migrations (both executed on sqlite3 3.54 with `foreign_keys=ON`; they ship together — 0004 assumes `users` is empty when it runs)

```sql
-- 0003_create_users_and_sessions.sql
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,                 -- argon2id/bcrypt encoded string; never the password
    created_at    TEXT NOT NULL CHECK (length(created_at) = 27)
) STRICT;

CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,              -- sha256(token) hex; the raw token lives only in the cookie, so a DB read yields no live sessions
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT    NOT NULL,
    expires_at TEXT    NOT NULL
) STRICT;
CREATE INDEX sessions_user_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- 0004_add_user_id_to_todos.sql
-- The column MUST be nullable: with foreign_keys=ON, SQLite refuses
-- "ADD COLUMN … NOT NULL DEFAULT 1 REFERENCES users(id)" ("Cannot add a REFERENCES column with non-NULL default value").
ALTER TABLE todos ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE CASCADE;

-- Bootstrap owner for existing rows. '!' can never match a hash; the first POST /api/auth/register claims this row (see below).
-- strftime('%f') yields 3 fractional digits; '|| ''000Z''' pads to the app's 27-char layout so scan.go can parse it.
INSERT INTO users (id, email, password_hash, created_at)
  SELECT 1, 'owner@localhost', '!', strftime('%Y-%m-%dT%H:%M:%f', 'now') || '000Z'
  WHERE NOT EXISTS (SELECT 1 FROM users);

UPDATE todos SET user_id = 1 WHERE user_id IS NULL;
CREATE INDEX todos_user_done_created_idx ON todos (user_id, done, created_at DESC, id DESC);
```

`foreign_keys=1` has been in the DSN since M7, so the `REFERENCES` clauses are enforced from the first run. `user_id` stays nullable at the SQL level; the Service never writes NULL. If you want the strict schema, a later migration rebuilds `todos` with `user_id INTEGER NOT NULL` (create `todos_new`, `INSERT … SELECT`, `DROP`, `RENAME`, recreate indexes) — and must copy the `sqlite_sequence` row across, because a rebuild otherwise resets the AUTOINCREMENT high-water mark.

**Claiming the bootstrap owner.** `user.Service.Register` runs inside one transaction (the first `WithTx` use case, §6): if `users` holds the unclaimed bootstrap row (`id = 1 AND password_hash = '!'`), it `UPDATE`s that row's email and hash instead of inserting, so the first person to register owns every pre-auth todo. Every later registration inserts normally and `ErrEmailTaken` applies. Without this rule the learner's own todos would become unreachable the moment `requireUser` is enabled.

### 2. Ownership threads through the domain as an explicit parameter

- `todo.Todo` gains `OwnerID int64 \`json:"-"\`` (or a response DTO appears in `web` — the trigger from §4).
- `todo.Store`: `Create(ctx, t)` reads `t.OwnerID`; `Get(ctx, ownerID, id)`, `List(ctx, ownerID, f)`, `Update(ctx, ownerID, id, p, now)`, `Delete(ctx, ownerID, id)`, `Stats(ctx, ownerID)`. Every SQL statement gains `AND user_id = ?`. There is no fetch-then-compare-owner path, so another user's row is indistinguishable from a missing one: 404, never 403, which is the correct IDOR answer.
- `todo.Service` methods gain `ownerID int64` as the second argument. Handlers get it from `auth.UserFromContext(r.Context())`. Nothing builds until every call site is updated — the refactor Go makes safe.
- The conformance suite gains an isolation subtest (A cannot see, update or delete B's todo).

### 3. New packages, same shapes as the existing ones

- `internal/user`: `User{ID, Email, CreatedAt}`, its own consumer-defined `Store` (`Create`, `ClaimBootstrap`, `ByEmail`, `ByID`), `ErrEmailTaken`, `ErrInvalidCredentials`, `Service.Register/Authenticate`, a `PasswordHasher` interface with an argon2id (or bcrypt cost 12) implementation from `golang.org/x/crypto` — the one place a second dependency is justified.
- `internal/auth`: `Sessions` (issue: `crypto/rand.Text()` token, store `sha256` hex, 30-day sliding expiry; verify: hash the cookie value, look up, check expiry; revoke), `RequireUser(next http.Handler) http.Handler` (reads the cookie, loads the user, puts the ID in ctx; 401 envelope — it only ever guards `/api/` routes, because the SPA shell is public and redirects itself), `UserFromContext`. Cookie: name `session`, `HttpOnly; SameSite=Lax; Path=/`, `Secure` from `TODO_COOKIE_SECURE` (true in prod, false on plain-http localhost). Use the `__Host-` prefix only when Secure is on: browsers reject a `__Host-` cookie that is not Secure, so dev login would silently fail.
- `internal/sqlite/user_store.go`, `session_store.go`, `WithTx`; a `DeleteExpired` run from a `time.Ticker` goroutine in `run()`, cancelled by the signal context.

### 4. Transport changes are local

- `routes.go`: `POST /api/auth/register`, `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/me`; todo routes and `/api/me` registered through `requireUser(...)`; `/healthz`, `/readyz`, `/api/auth/*` and the SPA at `/` stay open (static files hold no data; the app calls `GET /api/me` and sends itself to `/login` on 401). This is the only Go wiring file that changes.
- CSRF is already covered by the M12 guard, in production and through the Vite proxy alike (§7); `client.ts` already sends `credentials: 'same-origin'`.
- Login rate limiting: a mutex-guarded `map[string]*tokenBucket` keyed by client IP (30 lines), or `golang.org/x/time/rate` if a third dependency is acceptable.
- UI: `npm i react-router` — the first runtime dependency beyond `react`/`react-dom`: `<BrowserRouter>` with `/`, `/login` and `/register`, a `useAuth` hook over `GET /api/me`, the header shows the signed-in email and a logout button; `client.ts` turns any 401 into a redirect to `/login`. The Go SPA fallback already serves `index.html` for `/login` and `/register`, so a refresh there works and `spa.go` does not change.
- Config: `TODO_COOKIE_SECURE`, `TODO_SESSION_TTL=720h`; `TODO_ADDR` can finally default to `:8080`.

### Why sessions, not JWT

Server-side sessions are revocable (logout and password change actually work), need no signing-key rotation story, and are implementable with `crypto/rand` + `crypto/sha256` from the stdlib. JWT buys statelessness across many services, which a single binary does not have, and either pulls a library or hand-rolls HMAC verification that is easy to get subtly wrong. For a same-origin SPA the cookie is also the safer client story: an `HttpOnly` cookie is attached by the browser and unreadable from JavaScript, whereas a JWT kept in `localStorage` is readable by any injected script — the XSS anti-pattern. If an external SPA or mobile app later needs bearer tokens, `RequireUser` is the single place to accept `Authorization: Bearer` as a second credential source against the same `sessions` table.

### What does not change

`config` (two new keys), `respond.go`, `writeError`, the envelope, `middleware.go` (one more decorator), the migration runner, `scan.go`, `spa.go` (the fallback already covers the new routes), the Dockerfile, CI, and the test strategy (same fixture, same patterns).

---

## 13. Decisions the user should confirm

These eight change what gets built in the next milestones; say "yes" or name the alternative.

| # | decision | recommendation (✔) | alternative | because |
|---|---|---|---|---|
| 1 | Service layer | ✔ thin `*todo.Service` from M4 (validation, timestamps, defaults; `Now` field pinned by tests) | none: handlers call `Validate()` then the store, stores stamp times | one home for rules; it is exactly where `ownerID` and `CompletedAt` land later. Costs one small type. |
| 2 | List response shape | ✔ `{"items":[…],"total":N,"limit":L,"offset":O}` from M3 | bare JSON array (what `main.go` returns today) | adding `total` or a cursor to a bare array later is a breaking change; the UI footer wants `total` anyway |
| 3 | React delivery | ✔ Vite + TypeScript; built output embedded in the Go binary; dev via Vite proxy; plain CSS; hooks-only state | plain JavaScript template; separate static hosting + CORS middleware + `AddTrustedOrigin`; TanStack Query; Tailwind | one origin in dev and prod (no CORS), one deployable, typed wire shapes, Go tooling stays Node-free via `all:dist` + `.gitkeep`; each alternative has its trigger in §7 |
| 4 | Config style | ✔ environment only, `Load(lookup)`, `.env` via the Makefile | `flag` > env > defaults with `flag.TextVar` | less code, 12-factor, Docker-friendly; flags would add a second precedence rule to explain |
| 5 | Old endpoints | ✔ `/api/healthStatuses` dropped in M1 → `/healthz`; `/api/todos/count` renamed to `/api/todos/stats` `{total,open,done}` in M3 | keep `/count` → `{"count":N}` | one endpoint answers the UI footer; renaming is cheapest before anything depends on it |
| 6 | Fate of `memstore` | ✔ real store M3–M8, deleted in M8 once `sqlite.Store` passes the same conformance suite; `sqlitetest.NewDB(t)` (a migrated temp file, milliseconds) is the fixture for every test after that | keep memstore live forever as the handler-test fake | every later store feature (filters M11, `completedAt` M14, ownership M15) is written once instead of twice-plus-a-contract-case; tests run against the production store; memstore has already taught mutexes, copies and the race detector by M8. Keeping it buys I/O-free handler tests, which SQLite in a temp file makes unnecessary. |
| 7 | Default bind address | ✔ `127.0.0.1:8080` (Dockerfile sets `:8080`) | `:8080` everywhere (current behaviour) | there is no auth until M15; loopback means a laptop on café Wi-Fi is not serving an editable to-do list |
| 8 | v1 fields | ✔ `title`, `done`, `createdAt`, `updatedAt`; `notes` + `completedAt` in optional M14 | notes/dueAt/completedAt from the start | every milestone fits a sitting; M14 still teaches schema evolution |

**Defaults applied unless you object** (each has its reasoning in the section named): offset pagination with `total`, keyset as the documented upgrade (§5); JSON tags on `todo.Todo` with a response DTO only when shapes diverge (§4); strict decoding — unknown fields 400, no JSON content type 415 (§5); `run()` calls `sqlite.Migrate` explicitly rather than `Open` migrating internally (§6); fixed-width UTC TEXT timestamps rather than integer microseconds (§4); no index for the unfiltered list until it measurably matters (§4); version via `-ldflags -X` with a `ReadBuildInfo` fallback (§8); nullable `user_id` + bootstrap user claimed on first registration rather than a table rebuild (§12); `golang.org/x/crypto` argon2id/bcrypt as the second dependency in M15 rather than stdlib `crypto/pbkdf2`, which is not memory-hard (§14); SPA fallback to `index.html` for unknown GET paths outside `/api/` (§7); immutable caching for content-hashed assets (§7); npm, not pnpm or yarn, with a committed lockfile (§10); Node 24 LTS — maintenance from 2026-10-20, end of life 2028-04-30, so `node:24-alpine` and `node-version: 24` outlive this plan; Node 26 becomes Active LTS on 2026-10-28 and moving to it is the Dockerfile tag plus `node-version`, nothing else (§10); ESLint as `--eslint` scaffolds it rather than create-vite's default Oxlint (§10); no Playwright — the Go e2e test plus Vitest cover it until the UI grows (§9).

---

## 14. Dependencies

| module | why | stdlib alternative rejected because |
|---|---|---|
| `modernc.org/sqlite` (runtime; the only module linked into the binary) | Pure-Go SQLite driver registered as `"sqlite"` for `database/sql`. Lets the app build with `CGO_ENABLED=0`: static binary, trivial cross-compilation, gopls/Docker/CI without a C toolchain, `distroless/static` image, `-race` just works. Supports WAL, STRICT, `RETURNING`, `_pragma` and `_txlock` DSN options. Accepts a large `go.sum` and ~15 MB of binary in exchange. | `database/sql` is only the interface; the standard library ships no driver and no embedded database. Persisting to a JSON/gob file would not be real persistence (no concurrent-safe writes, queries, constraints or migrations). The cgo driver `mattn/go-sqlite3` forces a C toolchain into every build and image. |
| `honnef.co/go/tools/cmd/staticcheck` (dev only; `tool` directive via `go get -tool`, Go 1.24+; adds `// indirect` requires to go.mod but is never linked into the binary) | Catches what `go vet` does not: unused code, deprecated APIs, mis-wrapped errors, simplifications. Pinned so `go tool staticcheck ./...` gives identical results locally and in CI. | `go vet` is kept and runs first, but its analyzers are deliberately narrow. golangci-lint rejected as heavier than one module needs. |
| `golang.org/x/vuln/cmd/govulncheck` (CI only; `go run …@latest`; not in go.mod) | Reports known vulnerabilities actually reachable from this binary's call graph — relevant precisely because the SQLite driver pulls in a large transitive tree. | No stdlib equivalent; it is the Go team's own tool and leaves no trace in go.mod. |
| `golang.org/x/crypto` (argon2 or bcrypt) — **deferred to M15**, not added now | Password hashing needs a memory-hard or cost-tunable algorithm; `x/crypto` is maintained by the Go team and is the de-facto stdlib annex. | `crypto/pbkdf2` exists in the stdlib since Go 1.24 and would be acceptable with a high iteration count, but it is not memory-hard; for a login system the stronger primitive is the honest choice. |

**Frontend** (npm; never linked into the Go binary — only the built files are embedded)

| package | why | alternative rejected because |
|---|---|---|
| `react`, `react-dom` (the only runtime dependencies until M15) | components and hooks for one list's state; text children are escaped by default, so the XSS guarantee is the framework's, pinned by a Vitest case | hand-rolled DOM code stops scaling at the first interactive feature and has no component tests |
| `vite`, `@vitejs/plugin-react` (dev) | the dev server with HMR and the `/api` proxy, and the production bundler that content-hashes assets and writes `internal/web/dist` | webpack needs far more configuration; Create React App is unmaintained |
| `typescript` (+ the template's `@types/react`, `@types/react-dom`, `@types/node`) (dev) | `Todo`, `Page<T>`, `Stats` mirror the Go structs, so a wire-shape change breaks `tsc -b` in `npm run build` | plain JavaScript keeps the contract in the developer's head |
| `eslint` + what `--eslint` scaffolds (`@eslint/js`, `globals`, `typescript-eslint`, `eslint-plugin-react-hooks`, `eslint-plugin-react-refresh`) (dev) | the rules of hooks and TS lint as scaffolded; `make ui-lint` | Oxlint, create-vite 9's default, is the faster alternative and a one-flag swap later; ESLint is chosen because its flat config and the `react-hooks`/`react-refresh` plugins are the better-trodden path for a learner. Biome/Prettier are not in the template; add one when two people edit `frontend/` |
| `vitest`, `@testing-library/react` (+ its peer `@testing-library/dom`), `@testing-library/jest-dom`, `jsdom` (dev, added in M11) | Vitest reuses `vite.config.ts` and its transforms; Testing Library queries by role and label, like a user; jest-dom adds `toBeInTheDocument`/`toBeChecked`; jsdom gives Node a `document` | Jest would be a second transform pipeline beside Vite; Playwright component tests need a browser in CI for three tests |
| `react-router` — **deferred to M15**, not added now | `/login` and `/register` as client routes on top of the Go SPA fallback, with `navigate('/login')` on 401 | a hand-rolled `location.pathname` switch works for two routes, but the first redirect-after-login wants real navigation and history |

| explicitly not used | replaced by | when it would earn its place |
|---|---|---|
| chi/gin/echo, sqlx/GORM/ent/sqlc, zap/zerolog, viper/godotenv, testify/gomock, golang-migrate/goose, google/uuid, CORS libraries | a stdlib feature the learner should master: Go 1.22 ServeMux patterns, hand-written `database/sql`, `log/slog`, `os.LookupEnv` + Makefile, `testing` + a real store in a temp file, a 70-line embedded migration runner, int64 ids, a same-origin SPA served by the binary | sqlc past ~10 queries or a second engine; chi past ~40 routes; CORS middleware + `AddTrustedOrigin` when the SPA is hosted on another origin; goose when a CLI or several databases are needed |
| Next.js/Remix, Redux/Zustand, TanStack Query, Tailwind, axios, Playwright | an API client needs no SSR; hooks hold one list's state; `fetch` behind a 40-line `client.ts` is the HTTP client; plain CSS covers one page; the Go e2e test plus Vitest cover the flows | SSR when SEO or first paint of data matters; a store when state is shared across many unrelated components; TanStack Query (the named upgrade) when a second resource or background refetching appears; Tailwind with a design system across many pages; Playwright when the UI grows past one page |
