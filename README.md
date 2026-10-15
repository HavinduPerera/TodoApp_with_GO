# TodoApp_with_GO

A to-do app with a Go backend and, from milestone 10, a React frontend, built one
milestone at a time as a Go learning project. The design, the decisions behind it
and the full fifteen-milestone plan live in [ARCHITECTURE.md](ARCHITECTURE.md).

## Status

| Milestone | What it added | State |
|---|---|---|
| 1 | `main` → `run`, `log/slog`, server timeouts, `TODO_ADDR`, `GET /healthz`, Makefile, first `httptest` test | done |
| 2 | Graceful shutdown: signal context in `main`, synchronous bind, `serve()` with `Shutdown` | done |
| 3 | Domain package (`internal/todo`), in-memory store (`internal/memstore`), HTTP package (`internal/web`), list envelope, `GET /api/todos/{id}`, `/stats` replaces `/count` | done |
| 4 | `todo.Store` interface, `todo.Service` (validation, timestamps, defaults), `POST`/`PATCH`/`DELETE`, strict JSON decoding, the full error envelope | done |
| 5 | Store conformance suite in `internal/todo/todotest`, compile-time `Store` assertion, parallel handler tests, the race-detector exercise; example todos now seeded from `cmd/api` | done |
| 6 | `internal/config` with `Load(lookup)`, log level and format, shutdown timeout, version stamping via `-ldflags -X` with a build-info fallback, `.env.example` and `.env` loading in the Makefile | done |
| 7 | SQLite plumbing: the pure-Go driver, DSN pragmas (WAL, busy_timeout, foreign_keys), embedded forward-only migrations, `TODO_DB_PATH`, `GET /readyz`; todos still served from memory | done |
| 8 | SQLite store replaces the in-memory store | next |

Milestones 8 to 15 are described in ARCHITECTURE.md, section 11.

## Requirements

- Go 1.27.1 (see `go.mod`). One dependency: `modernc.org/sqlite`, a pure-Go SQLite
  driver, so no C toolchain is needed and `CGO_ENABLED=0` builds a static binary.
- `make`, `curl` and the `sqlite3` shell for the commands below.

## Run

```bash
make run
```

Ctrl-C or SIGTERM stops it gracefully: in-flight requests get up to the shutdown
timeout to finish, the log shows `shutdown complete`, and the exit code is 0.

## Configuration

Everything comes from environment variables, read once at startup. An unset or
empty variable keeps its default; an invalid one stops the process with an error
that names it, and every invalid variable is reported together.

| Variable | Default | Meaning |
|---|---|---|
| `TODO_ADDR` | `127.0.0.1:8080` | address to listen on; loopback only until authentication exists |
| `TODO_DB_PATH` | `data/todo.db` | the SQLite file; its directory is created on first start |
| `TODO_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (any case; offsets like `warn+2` work) |
| `TODO_LOG_FORMAT` | `text` | `text` for a terminal, `json` for anything that collects logs |
| `TODO_SHUTDOWN_TIMEOUT` | `10s` | how long in-flight requests get after a stop signal |

```bash
TODO_ADDR=127.0.0.1:9090 TODO_LOG_FORMAT=json make run
```

For local work, copy `.env.example` to `.env`, then uncomment and edit the lines
you want: the Makefile loads it into the environment for `make run` and the other
targets. The binary never reads `.env` itself; in a container, pass `-e` or
`--env-file`. A value set in `.env` beats the same variable in your shell for
make targets, so to override it for one run pass a make variable instead:

```bash
make run TODO_ADDR=127.0.0.1:9090
```

## Database

On first start the server creates the SQLite file at `TODO_DB_PATH` (with the
`-wal` and `-shm` files SQLite's write-ahead log needs) and applies the
migrations embedded in the binary, one transaction each, recording them in
`schema_migrations`. Restarts apply only what is new. Never copy a live database
file: the `-wal` file holds committed data, so use `sqlite3 data/todo.db
".backup out.db"` instead.

`make build` stamps the git revision into the binary, and the startup log and
`/healthz` report it. A plain `go build` reports the revision the go command
records itself; `go run` reports `dev`.

## Endpoints

| Method | Path | Response |
|---|---|---|
| GET | `/healthz` | `{"status":"ok","version":"<git revision>"}`; liveness only, touches nothing else |
| GET | `/readyz` | `200 {"status":"ok"}` when the database answers a ping within a second; `503 {"status":"unavailable","reason":…}` otherwise, and from the moment shutdown begins |
| GET | `/api/todos` | `{"items":[…],"total":2,"limit":50,"offset":0}`, newest first (in-memory until milestone 8) |
| GET | `/api/todos/{id}` | one todo; `404` `not_found` if it does not exist, `400` `bad_request` if the id is not a positive integer |
| GET | `/api/todos/stats` | `{"total":2,"open":1,"done":1}` |
| POST | `/api/todos` | body `{"title":"Buy milk"}`; `201` + `Location: /api/todos/{id}` with the new todo |
| PATCH | `/api/todos/{id}` | body with any of `title`, `done` (at least one); `200` with the updated todo |
| DELETE | `/api/todos/{id}` | `204`, no body; a second delete is `404` |

Requests with a body must send `Content-Type: application/json`. Decoding is
strict: unknown fields, a second JSON value, or a body over 64 KiB are rejected.

Every error under `/api/` has the same shape:

```json
{"error":{"code":"validation_failed","message":"validation failed: title: must not be blank","fields":{"title":"must not be blank"}}}
```

| code | status | when |
|---|---|---|
| `bad_request` | 400 | malformed JSON, unknown field, wrong JSON type, empty body, bad `{id}` |
| `not_found` | 404 | unknown id, unknown `/api/` path |
| `method_not_allowed` | 405 | known path, wrong method (`Allow` header lists the valid ones) |
| `payload_too_large` | 413 | body over 64 KiB |
| `unsupported_media_type` | 415 | missing or non-JSON `Content-Type` on POST/PATCH |
| `validation_failed` | 422 | domain rules; always carries `fields` |
| `internal` | 500 | anything unexpected; details go to the log, not the client |

```bash
curl -si localhost:8080/healthz
curl -s localhost:8080/api/todos
curl -si -X POST -H 'Content-Type: application/json' -d '{"title":"Buy milk"}' localhost:8080/api/todos
curl -si -X PATCH -H 'Content-Type: application/json' -d '{"done":true}' localhost:8080/api/todos/3
curl -si -X DELETE localhost:8080/api/todos/3
```

## Develop

| Command | What it does |
|---|---|
| `make build` | static binary at `bin/api` (`CGO_ENABLED=0`), version stamped from `git describe` |
| `make test` | `go test -race -shuffle=on -count=1 ./...`; try removing the lock in `memstore.Create` and watch `concurrent_writers` report a data race |
| `make vet` | `go vet ./...` |
| `make fmt` | fails if any file is not `gofmt`-formatted |
| `make db-shell` | opens the development database in the `sqlite3` shell |

## Layout

```
cmd/api/            main.go (wiring and lifecycle), main_test.go
internal/config/    environment variables → one validated Config
internal/todo/      the domain: Todo and params, validation, the Store interface, the Service; stdlib only
internal/todo/todotest/  the conformance suite every Store must pass (run by memstore now, SQLite later)
internal/memstore/  in-memory store: RWMutex + map; starts empty (retires in milestone 8)
internal/sqlite/    Open (DSN pragmas, one connection), embedded migrations and the runner
internal/web/       HTTP: routes, handlers, strict JSON decoding, the error envelope
ARCHITECTURE.md     design, decisions, milestones
Makefile
```
