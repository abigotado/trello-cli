package config

import (
	"testing"
	"time"

	"github.com/abigotado-niko/trello-cli/internal/errx"
)

func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want %v", cfg.Timeout, DefaultTimeout)
	}
	if cfg.Concurrency != DefaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", cfg.Concurrency, DefaultConcurrency)
	}
	if cfg.ReadOnly {
		t.Error("ReadOnly should default to false")
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TRELLO_CLI_BASE_URL":    "http://localhost:9999/1",
		"TRELLO_CLI_TIMEOUT":     "5s",
		"TRELLO_CLI_CONCURRENCY": "9",
		"TRELLO_CLI_BOARD":       "Roadmap",
		"TRELLO_CLI_LIST":        "Doing",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseURL != "http://localhost:9999/1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
	if cfg.Concurrency != 9 {
		t.Errorf("Concurrency = %d, want 9", cfg.Concurrency)
	}
	if cfg.DefaultBoard != "Roadmap" || cfg.DefaultList != "Doing" {
		t.Errorf("defaults = %q/%q", cfg.DefaultBoard, cfg.DefaultList)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"unparseable timeout", map[string]string{"TRELLO_CLI_TIMEOUT": "soon"}},
		{"zero timeout", map[string]string{"TRELLO_CLI_TIMEOUT": "0s"}},
		{"negative timeout", map[string]string{"TRELLO_CLI_TIMEOUT": "-5s"}},
		{"unparseable concurrency", map[string]string{"TRELLO_CLI_CONCURRENCY": "many"}},
		{"zero concurrency", map[string]string{"TRELLO_CLI_CONCURRENCY": "0"}},
		{"negative concurrency", map[string]string{"TRELLO_CLI_CONCURRENCY": "-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if errx.ExitCode(err) != errx.CodeUsage {
				t.Errorf("exit code = %d, want %d (usage)", errx.ExitCode(err), errx.CodeUsage)
			}
		})
	}
}

// Someone setting this variable is trying to lock the tool down. An
// unrecognized value must not silently leave mutations enabled.
func TestReadOnlyParsing(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"anything", true},
		{"0", false},
		{"false", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			cfg, err := Load(env(map[string]string{"TRELLO_CLI_READONLY": tt.value}))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.ReadOnly != tt.want {
				t.Errorf("ReadOnly = %v, want %v", cfg.ReadOnly, tt.want)
			}
		})
	}
}

func TestLoadNilLookupDoesNotPanic(t *testing.T) {
	if _, err := Load(nil); err != nil {
		t.Fatalf("Load(nil) error = %v", err)
	}
}
