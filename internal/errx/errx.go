// Package errx defines the typed errors that carry trello-cli's exit-code
// contract.
//
// It imports nothing else from this module, deliberately. Every other package
// depends on it, so a single convenience import here would turn the bottom of
// the dependency stack into a cycle. That is why [Candidate] is defined here
// rather than reusing a Trello model type.
package errx

import (
	"errors"
	"fmt"
	"time"
)

// Candidate identifies one possible match for an ambiguous or misspelled name.
//
// It is deliberately a plain value type rather than a Trello model: errx sits
// below internal/trello and must not import it.
type Candidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Error is a trello-cli error carrying everything the envelope and the exit
// status are built from.
type Error struct {
	// Code determines the process exit status.
	Code Code
	// Reason is the stable SCREAMING_SNAKE_CASE value of error.code. New
	// granularity belongs here, not in a new exit code.
	Reason string
	// Message is the human-readable explanation.
	Message string
	// Hint states the caller's next action ("pass --board-id"), written for a
	// machine reader rather than as an apology.
	Hint string
	// Candidates lists the possible matches behind an ambiguous lookup.
	Candidates []Candidate
	// DidYouMean lists near matches behind a failed lookup.
	DidYouMean []Candidate
	// RetryAfter is the server-advertised backoff for a retryable failure.
	RetryAfter time.Duration

	wrapped error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Reason
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.wrapped }

// Wrap attaches an underlying cause without changing the contract fields.
func (e *Error) Wrap(err error) *Error {
	e.wrapped = err
	return e
}

// WithHint replaces the remediation hint.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// ExitCode reports the process exit status for err.
//
// A nil error is [CodeOK]. An error that is not an [*Error] is [CodeInternal]:
// an untranslated error escaping to the top level is a bug in the layer that
// let it through, and reporting it as a semantic result would tell the caller
// to retry something that will never succeed.
func ExitCode(err error) Code {
	if err == nil {
		return CodeOK
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}

// Usage reports invalid flags or arguments.
func Usage(format string, args ...any) *Error {
	return &Error{
		Code:    CodeUsage,
		Reason:  "USAGE",
		Message: fmt.Sprintf(format, args...),
		Hint:    "check the flags against --help",
	}
}

// Internal reports a defect in trello-cli itself.
func Internal(format string, args ...any) *Error {
	return &Error{
		Code:    CodeInternal,
		Reason:  "INTERNAL",
		Message: fmt.Sprintf(format, args...),
		Hint:    "this is a bug in trello-cli; do not retry",
	}
}

// NotFound reports that no object of kind matched query. Attach near matches
// with DidYouMean so the caller can recover in one more call.
func NotFound(kind, query string, didYouMean []Candidate) *Error {
	hint := fmt.Sprintf("run 'trello-cli %ss list' to see available names", kind)
	if len(didYouMean) > 0 {
		hint = "re-run with one of the names in did_you_mean"
	}
	return &Error{
		Code:       CodeNotFound,
		Reason:     "NOT_FOUND_" + upper(kind),
		Message:    fmt.Sprintf("no %s matches %q", kind, query),
		Hint:       hint,
		DidYouMean: didYouMean,
	}
}

// Ambiguous reports that query matched more than one object of kind.
//
// Resolution never guesses between candidates: picking one would silently act
// on the wrong board.
func Ambiguous(kind, query string, candidates []Candidate) *Error {
	return &Error{
		Code:       CodeAmbiguous,
		Reason:     "AMBIGUOUS_" + upper(kind),
		Message:    fmt.Sprintf("%q matches %d %ss", query, len(candidates), kind),
		Hint:       fmt.Sprintf("re-run with --%s-id using an id from candidates", kind),
		Candidates: candidates,
	}
}

// Auth reports missing, rejected, or expired credentials.
func Auth(reason, format string, args ...any) *Error {
	return &Error{
		Code:    CodeAuth,
		Reason:  reason,
		Message: fmt.Sprintf(format, args...),
		Hint:    "run 'trello-cli auth login', or set TRELLO_API_KEY and TRELLO_TOKEN",
	}
}

// Retryable reports a rate limit or transport failure that may succeed later.
func Retryable(reason string, retryAfter time.Duration, format string, args ...any) *Error {
	hint := "back off and retry"
	if retryAfter > 0 {
		hint = fmt.Sprintf("retry after %s", retryAfter)
	}
	return &Error{
		Code:       CodeRetryable,
		Reason:     reason,
		Message:    fmt.Sprintf(format, args...),
		Hint:       hint,
		RetryAfter: retryAfter,
	}
}

// ConfirmRequired reports a destructive operation that was not confirmed.
func ConfirmRequired(action string) *Error {
	return &Error{
		Code:    CodeConfirm,
		Reason:  "CONFIRMATION_REQUIRED",
		Message: fmt.Sprintf("%s is destructive and was not confirmed", action),
		Hint:    "re-run with --yes, or with --dry-run to preview",
	}
}

// upper uppercases ASCII letters and turns separators into underscores, so a
// kind like "check-item" becomes the CHECK_ITEM half of an error code.
func upper(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z':
			out = append(out, c-('a'-'A'))
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
