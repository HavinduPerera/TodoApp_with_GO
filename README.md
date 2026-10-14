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
| 4 | Store interface, Service, create/update/delete endpoints, the full error envelope | next |

Milestones 4 to 15 are described in ARCHITECTURE.md, section 11.

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

```bash
curl -si localhost:8080/healthz
curl -s localhost:8080/api/todos
curl -si localhost:8080/api/todos/99
```

## Develop

| Command | What it does |
|---|---|
| `make build` | static binary at `bin/api` (`CGO_ENABLED=0`) |
| `make test` | `go test -race -shuffle=on -count=1 ./...` |
| `make vet` | `go vet ./...` |
| `make fmt` | fails if any file is not `gofmt`-formatted |

## Layout

```
cmd/api/            main.go (wiring and lifecycle), main_test.go
internal/todo/      the domain: Todo, ListFilter, Page, Stats, ErrNotFound; stdlib only
internal/memstore/  in-memory store: RWMutex + map, seeded with two todos
internal/web/       HTTP: routes, handlers, JSON encoding, the error envelope
ARCHITECTURE.md     design, decisions, milestones
Makefile
```
