# TodoApp_with_GO

A to-do app with a Go backend and, from milestone 10, a React frontend, built one
milestone at a time as a Go learning project. The design, the decisions behind it
and the full fifteen-milestone plan live in [ARCHITECTURE.md](ARCHITECTURE.md).

## Status

| Milestone | What it added | State |
|---|---|---|
| 1 | `main` → `run`, `log/slog`, server timeouts, `TODO_ADDR`, `GET /healthz`, Makefile, first `httptest` test | done |
| 2 | Graceful shutdown: signal context in `main`, synchronous bind, `serve()` with `Shutdown` | done |
| 3 | In-memory store, full CRUD, list envelope | next |

Milestones 3 to 15 are described in ARCHITECTURE.md, section 11.

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
| GET | `/api/todos` | the todo list (hardcoded until milestone 3) |
| GET | `/api/todos/count` | `{"count":2}` (becomes `/api/todos/stats` in milestone 3) |

```bash
curl -si localhost:8080/healthz
curl -s localhost:8080/api/todos
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
cmd/api/           main.go (wiring and lifecycle), main_test.go
ARCHITECTURE.md    design, decisions, milestones
Makefile
```
