package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"todoapp/internal/config"
)

// want spells out the documented defaults (ARCHITECTURE.md §8) as a literal,
// so this file fails if a default ever drifts; comparing against a value
// exported by the package under test would prove nothing.
var want = config.Config{
	Addr:            "127.0.0.1:8080",
	LogLevel:        slog.LevelInfo,
	LogFormat:       "text",
	ShutdownTimeout: 10 * time.Second,
}

// lookup turns a map into the os.LookupEnv shape, so each test case is a
// literal and no test ever touches the process environment: os.Setenv would
// leak into every other test and race with parallel ones, and t.Setenv, which
// does restore the value, refuses to run in a test that has called t.Parallel.
func lookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

// with returns the defaults with one change applied, so every override case
// asserts the whole struct: the field it set, and the three it must not touch.
func with(change func(c *config.Config)) config.Config {
	c := want
	change(&c)
	return c
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(lookup(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != want {
		t.Errorf("Load(nothing) = %+v, want %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want config.Config
	}{
		{"addr", map[string]string{"TODO_ADDR": ":9090"}, with(func(c *config.Config) { c.Addr = ":9090" })},
		{"addr with port 0 is allowed", map[string]string{"TODO_ADDR": "127.0.0.1:0"}, with(func(c *config.Config) { c.Addr = "127.0.0.1:0" })},
		{"ipv6 addr", map[string]string{"TODO_ADDR": "[::1]:8080"}, with(func(c *config.Config) { c.Addr = "[::1]:8080" })},
		{"level, any case", map[string]string{"TODO_LOG_LEVEL": "DEBUG"}, with(func(c *config.Config) { c.LogLevel = slog.LevelDebug })},
		{"level with offset", map[string]string{"TODO_LOG_LEVEL": "warn+2"}, with(func(c *config.Config) { c.LogLevel = slog.LevelWarn + 2 })},
		{"json format", map[string]string{"TODO_LOG_FORMAT": "json"}, with(func(c *config.Config) { c.LogFormat = "json" })},
		{"timeout", map[string]string{"TODO_SHUTDOWN_TIMEOUT": "1m30s"}, with(func(c *config.Config) { c.ShutdownTimeout = 90 * time.Second })},
		{"set but empty keeps the default", map[string]string{"TODO_ADDR": "", "TODO_LOG_FORMAT": ""}, want},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Load(lookup(tt.env))
			if err != nil {
				t.Fatalf("Load(%v): %v", tt.env, err)
			}
			if cfg != tt.want {
				t.Errorf("Load(%v) = %+v, want %+v", tt.env, cfg, tt.want)
			}
		})
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		env      map[string]string
		wantText string // must appear in the error, so the message names the variable and the cause
	}{
		{"addr without a port", map[string]string{"TODO_ADDR": "9090"}, "config: TODO_ADDR: "},
		{"addr with a non-numeric port", map[string]string{"TODO_ADDR": "localhost:abc"}, `config: TODO_ADDR: port "abc" must be a number`},
		{"addr with a port out of range", map[string]string{"TODO_ADDR": "127.0.0.1:99999"}, `config: TODO_ADDR: port "99999" must be a number`},
		{"addr with a trailing space", map[string]string{"TODO_ADDR": "127.0.0.1:8080 "}, `config: TODO_ADDR: port "8080 " must be a number`},
		{"unknown level", map[string]string{"TODO_LOG_LEVEL": "loud"}, "config: TODO_LOG_LEVEL: "},
		{"unknown format", map[string]string{"TODO_LOG_FORMAT": "xml"}, `config: TODO_LOG_FORMAT: "xml" is not one of text, json`},
		{"unparsable timeout", map[string]string{"TODO_SHUTDOWN_TIMEOUT": "abc"}, `config: TODO_SHUTDOWN_TIMEOUT: time: invalid duration "abc"`},
		{"zero timeout", map[string]string{"TODO_SHUTDOWN_TIMEOUT": "0s"}, "config: TODO_SHUTDOWN_TIMEOUT: must be positive"},
		{"negative timeout", map[string]string{"TODO_SHUTDOWN_TIMEOUT": "-5s"}, "config: TODO_SHUTDOWN_TIMEOUT: must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(lookup(tt.env))
			if err == nil {
				t.Fatalf("Load(%v) = nil error, want one containing %q", tt.env, tt.wantText)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantText)
			}
		})
	}
}

// TestLoadReportsEveryProblem is the point of errors.Join: one failed start
// names every bad variable, not just the first.
func TestLoadReportsEveryProblem(t *testing.T) {
	t.Parallel()
	_, err := config.Load(lookup(map[string]string{
		"TODO_LOG_LEVEL":        "loud",
		"TODO_SHUTDOWN_TIMEOUT": "abc",
	}))
	if err == nil {
		t.Fatal("Load = nil error, want two")
	}
	for _, want := range []string{"TODO_LOG_LEVEL", "TODO_SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %s", err, want)
		}
	}
	if got := strings.Count(err.Error(), "config: "); got != 2 {
		t.Errorf("error lists %d problems, want 2: %q", got, err)
	}
}
