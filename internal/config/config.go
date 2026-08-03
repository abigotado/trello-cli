// Package config holds non-secret defaults.
//
// It deliberately never touches credentials: those live in internal/auth,
// behind the OS keychain or the environment. A config file that could hold a
// token would be a config file someone eventually commits.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/abigotado/trello-cli/internal/errx"
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
	if v, ok := lookup("TRELLO_CLI_READONLY"); ok {
		on, err := readOnlyFlag(v)
		if err != nil {
			return Config{}, err
		}
		cfg.ReadOnly = on
	}
	return cfg, nil
}

// readOnlyFlag interprets TRELLO_CLI_READONLY.
//
// The previous rule was "anything except 0, false or empty means on". That is
// safe in the sense that a typo locks rather than unlocks, but it made
// TRELLO_CLI_READONLY=off enable read-only mode — the opposite of what anyone
// typing it intends, and a value the tool would then report as "is set". A
// lock whose disengage spelling silently engages it is worse than one that
// refuses to start.
//
// So both directions are spelled out and anything else is an error. This is
// the same rule the resolver follows for names: when the input is ambiguous,
// say so rather than pick.
func readOnlyFlag(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "f", "false", "n", "no", "off":
		return false, nil
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	default:
		return false, errx.Usage(
			"TRELLO_CLI_READONLY=%q is not a yes or no value: use 1/true/yes/on, or 0/false/no/off", v)
	}
}
