package errx

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Code
	}{
		{"nil is ok", nil, CodeOK},
		{"usage", Usage("bad flag"), CodeUsage},
		{"not found", NotFound("board", "x", nil), CodeNotFound},
		{"ambiguous", Ambiguous("board", "x", nil), CodeAmbiguous},
		{"auth", Auth("NOPE", "bad"), CodeAuth},
		{"retryable", Retryable("RATE", 0, "slow down"), CodeRetryable},
		{"confirm", ConfirmRequired("delete"), CodeConfirm},
		{"internal", Internal("boom"), CodeInternal},
		// An untranslated error reaching the top level is a defect in the
		// layer that let it through. Reporting it as anything but internal
		// would tell the caller to retry something that cannot succeed.
		{"untyped error is internal", errors.New("raw"), CodeInternal},
		{"wrapped typed error keeps its code", fmt.Errorf("ctx: %w", Auth("NOPE", "bad")), CodeAuth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// The exit code table is the public contract. These assertions exist so that
// renumbering a code fails a test rather than silently breaking every agent
// that already branches on it.
func TestExitCodeValuesAreFrozen(t *testing.T) {
	frozen := map[Code]string{
		0: "OK",
		1: "INTERNAL",
		2: "USAGE",
		3: "NOT_FOUND",
		4: "AMBIGUOUS",
		5: "AUTH",
		6: "RETRYABLE",
		7: "CONFIRMATION_REQUIRED",
	}
	got := Codes()
	if len(got) != len(frozen) {
		t.Fatalf("Codes() returned %d entries, want %d", len(got), len(frozen))
	}
	for _, info := range got {
		want, ok := frozen[info.Code]
		if !ok {
			t.Errorf("undocumented exit code %d", info.Code)
			continue
		}
		if info.Name != want {
			t.Errorf("code %d is named %q, want %q", info.Code, info.Name, want)
		}
		if info.NextMove == "" {
			t.Errorf("code %d has no next_move; every code must state a distinct recovery action", info.Code)
		}
	}
}

// CodeUsage must stay 2. A compiled Go binary exits 2 when the runtime kills
// it, so 2 must never carry a meaning that makes a crash look like a
// legitimate, retryable result.
func TestUsageIsTwoSoPanicsAreNotMisread(t *testing.T) {
	if CodeUsage != 2 {
		t.Fatalf("CodeUsage = %d, want 2", CodeUsage)
	}
	for _, c := range []Code{CodeNotFound, CodeAmbiguous, CodeAuth, CodeRetryable, CodeConfirm} {
		if c == 2 {
			t.Fatalf("a semantic code collides with the Go runtime's panic status 2")
		}
	}
}

func TestCodesIsCopied(t *testing.T) {
	first := Codes()
	first[0].Name = "MUTATED"
	if Codes()[0].Name == "MUTATED" {
		t.Fatal("Codes() exposed the package-level table for mutation")
	}
}

func TestErrorReasonsAreScreamingSnake(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{"not found uppercases the kind", NotFound("board", "q", nil), "NOT_FOUND_BOARD"},
		{"ambiguous uppercases the kind", Ambiguous("list", "q", nil), "AMBIGUOUS_LIST"},
		{"separators become underscores", NotFound("check-item", "q", nil), "NOT_FOUND_CHECK_ITEM"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Reason != tt.want {
				t.Errorf("Reason = %q, want %q", tt.err.Reason, tt.want)
			}
		})
	}
}

func TestNotFoundHintChangesWithSuggestions(t *testing.T) {
	without := NotFound("board", "q", nil)
	with := NotFound("board", "q", []Candidate{{ID: "1", Name: "Roadmap", Kind: "board"}})
	if without.Hint == with.Hint {
		t.Error("the hint should tell the caller to use did_you_mean when suggestions exist")
	}
	if len(with.DidYouMean) != 1 {
		t.Errorf("DidYouMean has %d entries, want 1", len(with.DidYouMean))
	}
}

func TestRetryableCarriesRetryAfter(t *testing.T) {
	err := Retryable("RATE_LIMITED", 3*time.Second, "slow down")
	if err.RetryAfter != 3*time.Second {
		t.Errorf("RetryAfter = %v, want 3s", err.RetryAfter)
	}
	if err.Hint == "back off and retry" {
		t.Error("a known retry delay should appear in the hint")
	}
}

func TestUnwrapExposesTheCause(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	err := Retryable("NETWORK", 0, "request failed").Wrap(cause)
	if !errors.Is(err, cause) {
		t.Error("errors.Is could not reach the wrapped cause")
	}
}
