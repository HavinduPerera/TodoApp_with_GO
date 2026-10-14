BIN := bin/api
PKG := ./cmd/api

.PHONY: run build test vet fmt

run:   ; go run $(PKG)
build: ; CGO_ENABLED=0 go build -trimpath -o $(BIN) $(PKG)
test:  ; go test -race -shuffle=on -count=1 ./...
vet:   ; go vet ./...
# gofmt -l prints unformatted files on stdout but parse errors on stderr (exit 2); capture both so neither slips through.
fmt:   ; @out="$$(gofmt -l . 2>&1)"; test -z "$$out" || { printf '%s\n' "$$out"; echo "run gofmt -w ."; exit 1; }
