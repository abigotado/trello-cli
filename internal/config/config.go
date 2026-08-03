// Package config holds non-secret defaults.
//
// It deliberately never touches credentials: those live in internal/auth,
// behind the OS keychain or the environment. A config file that could hold a
// token would be a config file someone eventually commits.
package config

import (
	"os"
	"strconv"
	"time"

	"github.com/abigotado-niko/trello-cli/internal/errx"
)

// DefaultBaseURL is the Trello REST root.
const DefaultBaseURL = "https://api.trello.com/1"

// DefaultTimeout bounds a single command invocation.
const DefaultTimeout = 30 * time.Second

// DefaultConcurrency bounds in-flight requests for commands that fan out.
//
// Trello's limits are enforced server-side and reported per response, so this
// is a politeness bound rather than a rate limiter; pacing is handled in
// internal/trello from the response headers.
const DefaultConcurrency = 4

// Config is the resolved non-secret configuration for one invocation.
type Config struct {
	BaseURL      string
	Timeout      time.Duration
	Concurrency  int
	DefaultBoard string
	DefaultList  string
	ReadOnly     bool
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		BaseURL:     DefaultBaseURL,
		Timeout:     DefaultTimeout,
		Concurrency: DefaultConcurrency,
	}
}

// Load resolves configuration from the environment over the built-in defaults.
//
// lookup is normally os.LookupEnv; tests pass their own so they never depend on
// the developer's environment.
func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	cfg := Default()

	if v, ok := lookup("TRELLO_CLI_BASE_URL"); ok && v != "" {
		cfg.BaseURL = v
	}
	if v, ok := lookup("TRELLO_CLI_TIMEOUT"); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, errx.Usage("invalid TRELLO_CLI_TIMEOUT %q: %v", v, err)
		}
		if d <= 0 {
			return Config{}, errx.Usage("TRELLO_CLI_TIMEOUT must be positive, got %q", v)
		}
		cfg.Timeout = d
	}
	if v, ok := lookup("TRELLO_CLI_CONCURRENCY"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, errx.Usage("invalid TRELLO_CLI_CONCURRENCY %q: want a positive integer", v)
		}
		cfg.Concurrency = n
	}
	if v, ok := lookup("TRELLO_CLI_BOARD"); ok {
		cfg.DefaultBoard = v
	}
	if v, ok := lookup("TRELLO_CLI_LIST"); ok {
		cfg.DefaultList = v
	}
	// Any non-empty value enables read-only mode. A user reaching for this is
	// trying to lock the tool down, so an unrecognized value must not silently
	// leave mutations enabled.
	if v, ok := lookup("TRELLO_CLI_READONLY"); ok && v != "" && v != "0" && v != "false" {
		cfg.ReadOnly = true
	}
	return cfg, nil
}
