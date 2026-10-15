// Package config turns environment variables into one typed, validated Config.
// It is read exactly once, at startup, and every problem is reported together
// so one failed start names every bad variable instead of the first one.
//
// The binary never reads a .env file: the Makefile loads one into the
// environment for local runs (make run), and containers pass -e or --env-file.
// That keeps configuration twelve-factor and leaves one source of truth.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"
)

// Config is everything the process needs from its environment.
type Config struct {
	Addr            string        // TODO_ADDR: host:port to listen on; loopback by default until authentication exists
	DBPath          string        // TODO_DB_PATH: the SQLite file; its directory is created if missing
	LogLevel        slog.Level    // TODO_LOG_LEVEL: debug, info, warn, error
	LogFormat       string        // TODO_LOG_FORMAT: text for terminals, json for containers
	ShutdownTimeout time.Duration // TODO_SHUTDOWN_TIMEOUT: how long in-flight requests get after a stop signal
}

// defaults are what Load returns when nothing is set: the values in
// ARCHITECTURE.md §8. Unexported on purpose: a package-level variable that any
// importer could assign to would silently change what every later Load returns.
var defaults = Config{
	Addr:            "127.0.0.1:8080",
	DBPath:          "data/todo.db",
	LogLevel:        slog.LevelInfo,
	LogFormat:       "text",
	ShutdownTimeout: 10 * time.Second,
}

// Load builds a Config from lookup, which has the shape of os.LookupEnv.
// Taking the function as a parameter instead of calling os.LookupEnv directly
// means a test can pass a closure over a map and never touch process state.
// A variable that is unset or set to the empty string keeps its default.
//
// Every invalid variable produces one error, prefixed with its name; the errors
// are combined with errors.Join, which formats them one per line and still
// lets errors.Is and errors.As see each of them.
func Load(lookup func(string) (string, bool)) (Config, error) {
	cfg := defaults // a struct copy: Load never writes to defaults itself
	var errs []error
	fail := func(key string, err error) { errs = append(errs, fmt.Errorf("config: %s: %w", key, err)) }
	get := func(key string) (string, bool) {
		v, ok := lookup(key)
		return v, ok && v != ""
	}

	if v, ok := get("TODO_ADDR"); ok {
		// Check the host:port shape and that the port is a number in 0..65535
		// now, so a typo fails as "config:" with the variable name rather than as
		// a listen error later. net.SplitHostPort is a tokenizer, not a
		// validator: it accepts "host:abc" and "host:99999", so the port is
		// checked separately. Whether the port is free is still net.Listen's to say.
		_, port, err := net.SplitHostPort(v)
		if err == nil {
			if _, perr := strconv.ParseUint(port, 10, 16); perr != nil { // rejects "", "abc", "99999" and "8080 " (a trailing space from a .env comment)
				err = fmt.Errorf("port %q must be a number between 0 and 65535", port)
			}
		}
		if err != nil {
			fail("TODO_ADDR", err)
		} else {
			cfg.Addr = v
		}
	}
	if v, ok := get("TODO_DB_PATH"); ok {
		cfg.DBPath = v // anything non-empty: whether it can be opened is sqlite.Open's to say
	}
	if v, ok := get("TODO_LOG_LEVEL"); ok {
		// slog.Level implements encoding.TextUnmarshaler, the standard library's
		// extension point for "parse yourself from text". It accepts the level
		// names in any case ("debug", "WARN") and offsets such as "info+2".
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			fail("TODO_LOG_LEVEL", err)
		}
	}
	if v, ok := get("TODO_LOG_FORMAT"); ok {
		switch v {
		case "text", "json":
			cfg.LogFormat = v
		default:
			fail("TODO_LOG_FORMAT", fmt.Errorf("%q is not one of text, json", v))
		}
	}
	if v, ok := get("TODO_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(v) // "10s", "1m30s", "500ms"
		switch {
		case err != nil:
			fail("TODO_SHUTDOWN_TIMEOUT", err)
		case d <= 0:
			fail("TODO_SHUTDOWN_TIMEOUT", fmt.Errorf("must be positive, got %s", d))
		default:
			cfg.ShutdownTimeout = d
		}
	}

	// errors.Join returns nil for an empty list, so the happy path needs no special case.
	return cfg, errors.Join(errs...)
}
