// Package output renders every user-facing result.
//
// Two rules hold everywhere in this package:
//
//   - stdout carries only the response envelope. Logs, warnings, progress, and
//     prompts go to stderr. An agent parses stdout, so anything else written
//     there corrupts the parse.
//   - raw Trello REST JSON is never the default. Context is the scarce resource
//     for the callers this tool is built for, so the default field set is
//     minimal and everything else is opt-in.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/abigotado-niko/trello-cli/internal/errx"
)

// Format selects how results are rendered.
type Format string

const (
	// FormatText is the compact human rendering: one line per entity.
	FormatText Format = "text"
	// FormatJSON is the machine envelope.
	FormatJSON Format = "json"
	// FormatRaw is Trello's untouched response payload.
	FormatRaw Format = "raw"
)

// ParseFormat validates a --output value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatText, FormatJSON, FormatRaw:
		return Format(s), nil
	default:
		return "", errx.Usage("invalid --output %q: want text, json, or raw", s)
	}
}

// Field is one rendered name/value pair of an entity.
type Field struct {
	Name  string
	Value string
	// Raw is the value used when this field is projected into JSON, so
	// booleans and numbers do not become strings.
	Raw any
}

// Renderable is an entity that can describe itself as ordered fields.
//
// Implementing it is what makes a type work with both the compact text
// renderer and --fields projection.
type Renderable interface {
	Fields() []Field
}

// Envelope is the machine contract. Field order here is the order agents see.
type Envelope struct {
	OK    bool       `json:"ok"`
	V     int        `json:"v"`
	Data  any        `json:"data,omitempty"`
	Meta  *Meta      `json:"meta,omitempty"`
	Error *ErrorBody `json:"error,omitempty"`
	Hint  string     `json:"hint,omitempty"`
}

// Meta describes a result set.
type Meta struct {
	Count     int  `json:"count"`
	Truncated bool `json:"truncated"`
}

// ErrorBody is the error half of the envelope.
type ErrorBody struct {
	Code       string           `json:"code"`
	Message    string           `json:"message"`
	Candidates []errx.Candidate `json:"candidates,omitempty"`
	DidYouMean []errx.Candidate `json:"did_you_mean,omitempty"`
	RetryAfter string           `json:"retry_after,omitempty"`
}

// Writer renders envelopes to a pair of streams.
type Writer struct {
	Format Format
	// Fields is the projection allowlist. Empty means the type's default set.
	Fields []string
	Out    io.Writer
	Err    io.Writer
}

// New builds a Writer over stdout and stderr.
func New(format Format, fields []string) *Writer {
	return &Writer{Format: format, Fields: fields, Out: os.Stdout, Err: os.Stderr}
}

// DefaultFormat reports the format to use when --output was not given.
//
// It is FormatJSON whenever stdout is not a terminal. Requiring an agent to
// remember a flag in order to get parseable output is a mistake that costs a
// whole class of failed runs, and it costs a human nothing to have text when
// they are actually at a terminal.
func DefaultFormat(stdout *os.File) Format {
	info, err := stdout.Stat()
	if err != nil {
		return FormatJSON
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return FormatText
	}
	return FormatJSON
}

// Success renders a successful result. data may be a Renderable, a slice of
// Renderable, or any JSON-marshalable value.
func (w *Writer) Success(data any) error {
	switch w.Format {
	case FormatText:
		return w.renderText(data)
	case FormatRaw:
		return w.encode(data)
	default:
		payload, err := w.project(data)
		if err != nil {
			return err
		}
		env := Envelope{OK: true, V: errx.EnvelopeVersion, Data: payload}
		if rows, ok := asRows(data); ok {
			env.Meta = &Meta{Count: len(rows)}
		}
		return w.encode(env)
	}
}

// Failure renders err and reports the exit code the process should use.
//
// It never returns an error of its own: a failure to report a failure would
// leave the caller with no output at all, so a write problem is surfaced on
// stderr and the original exit code is preserved.
func (w *Writer) Failure(err error) errx.Code {
	code := errx.ExitCode(err)
	body := &ErrorBody{Code: "INTERNAL", Message: err.Error()}
	hint := "this is a bug in trello-cli; do not retry"

	var typed *errx.Error
	if errors.As(err, &typed) {
		body.Code = typed.Reason
		body.Message = typed.Message
		body.Candidates = typed.Candidates
		body.DidYouMean = typed.DidYouMean
		hint = typed.Hint
		if typed.RetryAfter > 0 {
			body.RetryAfter = typed.RetryAfter.String()
		}
	}

	if w.Format == FormatText {
		fmt.Fprintf(w.Err, "error: %s\n", body.Message)
		if hint != "" {
			fmt.Fprintf(w.Err, "hint: %s\n", hint)
		}
		for _, c := range append(body.Candidates, body.DidYouMean...) {
			fmt.Fprintf(w.Err, "  - %s (%s)\n", c.Name, c.ID)
		}
		return code
	}

	// The error envelope goes to stdout like any other envelope: it is the
	// command's result, and an agent that parsed only stdout would otherwise
	// see nothing at all on failure.
	if encErr := w.encode(Envelope{OK: false, V: errx.EnvelopeVersion, Error: body, Hint: hint}); encErr != nil {
		fmt.Fprintf(w.Err, "error: %s\n", body.Message)
		fmt.Fprintf(w.Err, "error: could not encode the error envelope: %v\n", encErr)
	}
	return code
}

func (w *Writer) encode(v any) error {
	enc := json.NewEncoder(w.Out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return errx.Internal("encode output: %v", err)
	}
	return nil
}

// project applies the --fields allowlist. Without it, values are marshaled as
// their own types so the default field set is whatever the type declares.
func (w *Writer) project(data any) (any, error) {
	if len(w.Fields) == 0 {
		return data, nil
	}
	rows, ok := asRows(data)
	if !ok {
		return nil, errx.Usage("--fields is not supported for this command")
	}
	projected := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		available := row.Fields()
		out := make(map[string]any, len(w.Fields))
		for _, want := range w.Fields {
			found := false
			for _, f := range available {
				if f.Name == want {
					out[f.Name] = f.Raw
					found = true
					break
				}
			}
			if !found {
				return nil, errx.Usage("unknown field %q: available are %s", want, fieldNames(available))
			}
		}
		projected = append(projected, out)
	}
	if _, isSlice := asSlice(data); !isSlice && len(projected) == 1 {
		return projected[0], nil
	}
	return projected, nil
}

func (w *Writer) renderText(data any) error {
	rows, ok := asRows(data)
	if !ok {
		return w.encode(data)
	}
	for _, row := range rows {
		fields := row.Fields()
		if len(w.Fields) > 0 {
			selected := make([]Field, 0, len(w.Fields))
			for _, want := range w.Fields {
				for _, f := range fields {
					if f.Name == want {
						selected = append(selected, f)
						break
					}
				}
			}
			fields = selected
		}
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			// Empty values are dropped rather than padded. Trello leaves most
			// optional fields blank, so keeping them would end every line in a
			// run of separators and bury the values that are actually set.
			if f.Value == "" {
				continue
			}
			parts = append(parts, f.Value)
		}
		if _, err := fmt.Fprintln(w.Out, strings.Join(parts, "  ")); err != nil {
			return errx.Internal("write output: %v", err)
		}
	}
	return nil
}

// asRows normalizes a single Renderable or a slice of them into a slice.
func asRows(data any) ([]Renderable, bool) {
	if rows, ok := asSlice(data); ok {
		return rows, true
	}
	if row, ok := data.(Renderable); ok {
		return []Renderable{row}, true
	}
	return nil, false
}

func asSlice(data any) ([]Renderable, bool) {
	rows, ok := data.([]Renderable)
	return rows, ok
}

func fieldNames(fields []Field) string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}
