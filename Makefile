# A .env file (copy .env.example) is loaded into every recipe's environment, so
# `make run` picks up TODO_* settings. The binary never reads .env itself.
# Keep it to bare KEY=value lines, no quotes and no trailing comments: make
# parses it, not a shell, so quotes stay in the value and so do spaces before
# a #. A value set in .env beats the same variable in your shell; to override
# it for one run, pass a make variable: make run TODO_ADDR=127.0.0.1:9090
-include .env
export

BIN     := bin/api
PKG     := ./cmd/api
# The version baked into the binary: git describe's nearest tag, or with no
# tags (as now) the short commit hash; "-dirty" when a tracked file has
# uncommitted changes (untracked files do not count, unlike the go command's
# own vcs.modified); "dev" outside a checkout. -X sets main.version at link time.
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: run build test vet fmt db-shell

run:   ; go run $(PKG)
build: ; CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) $(PKG)
test:  ; go test -race -shuffle=on -count=1 ./...
vet:   ; go vet ./...
# gofmt -l prints unformatted files on stdout but parse errors on stderr (exit 2); capture both so neither slips through.
fmt:   ; @out="$$(gofmt -l . 2>&1)"; test -z "$$out" || { printf '%s\n' "$$out"; echo "run gofmt -w ."; exit 1; }
# Open the development database in the sqlite3 shell: TODO_DB_PATH from .env, your shell or a make variable, else the default.
db-shell: ; sqlite3 $(or $(TODO_DB_PATH),data/todo.db)
