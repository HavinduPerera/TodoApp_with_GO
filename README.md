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
| 6 | Config package, version stamping, `.env` | next |

Milestones 6 to 15 are described in ARCHITECTURE.md, section 11.

## Requirements

- Go 1.27.1 (see `go.mod`). No other dependencies yet.
- `make` and `curl` for the commands below.

## Run

```bash
make run
```

The server listens on `127.0.0.1:8080` by default (loopback only, because there
is no authentication yet). Override the address with `TODO_ADDR`:

```bash
TODO_ADDR=127.0.0.1:9090 make run
```

Ctrl-C or SIGTERM stops it gracefully: in-flight requests get up to 10 seconds to
finish, the log shows `shutdown complete`, and the exit code is 0.

## Endpoints

| Method | Path | Response |
|---|---|---|
| GET | `/healthz` | `{"status":"ok"}`; liveness only, touches nothing else |
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
| `make build` | static binary at `bin/api` (`CGO_ENABLED=0`) |
| `make test` | `go test -race -shuffle=on -count=1 ./...`; try removing the lock in `memstore.Create` and watch `concurrent_writers` report a data race |
| `make vet` | `go vet ./...` |
| `make fmt` | fails if any file is not `gofmt`-formatted |

## Layout

```
cmd/api/            main.go (wiring and lifecycle), main_test.go
internal/todo/      the domain: Todo and params, validation, the Store interface, the Service; stdlib only
internal/todo/todotest/  the conformance suite every Store must pass (run by memstore now, SQLite later)
internal/memstore/  in-memory store: RWMutex + map; starts empty
internal/web/       HTTP: routes, handlers, strict JSON decoding, the error envelope
ARCHITECTURE.md     design, decisions, milestones
Makefile
```
