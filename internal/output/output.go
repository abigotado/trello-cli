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
	"reflect"
	"strings"

	"github.com/abigotado/trello-cli/internal/errx"
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

// Meta describes a result set. It is present only for collections.
type Meta struct {
	Count int `json:"count"`
	// Truncated is deliberately declared before anything sets it. Pagination
	// arrives with the list commands; reserving the name now means adding the
	// behavior later is additive, whereas introducing the field later risks a
	// caller having already assumed its absence means "complete".
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
		// Projection is meaningless here: raw output is the payload as
		// received. Accepting --fields and quietly discarding it would give
		// the same flag a third behavior depending on -o.
		if len(w.Fields) > 0 {
			return errx.Usage("--fields cannot be combined with --output raw")
		}
		return w.encode(data)
	default:
		payload, err := w.project(data)
		if err != nil {
			return err
		}
		env := Envelope{OK: true, V: errx.EnvelopeVersion, Data: payload}
		// meta means "this is a countable collection", and nothing else. It is
		// deliberately absent for a single object and for static payloads like
		// the contract dump, so an agent can use its presence as a reliable
		// signal instead of having to know which commands return lists.
		if rows, collection, ok := asRows(data); ok && collection {
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

// selectFields resolves the --fields allowlist against one entity.
//
// Both the JSON and the text renderer go through this. They used to match
// names with separate loops, and the text one silently skipped names it could
// not resolve — so the same typo was a usage error in one format and a quietly
// dropped column in the other.
func selectFields(available []Field, want []string) ([]Field, error) {
	if len(want) == 0 {
		return available, nil
	}
	selected := make([]Field, 0, len(want))
	for _, name := range want {
		found := false
		for _, f := range available {
			if f.Name == name {
				selected = append(selected, f)
				found = true
				break
			}
		}
		if !found {
			return nil, errx.Usage("unknown field %q: available are %s", name, fieldNames(available))
		}
	}
	return selected, nil
}

// project renders data through its declared field set.
//
// Renderable values always go through Fields(), with or without --fields.
// Marshaling the struct directly when no projection was requested would give
// one command two different JSON shapes — different key names, and fields the
// view computes (a card's list name) missing entirely from the default output.
// A caller cannot write one parser against that.
func (w *Writer) project(data any) (any, error) {
	rows, collection, ok := asRows(data)
	if !ok {
		if len(w.Fields) > 0 {
			return nil, errx.Usage("--fields is not supported for this command")
		}
		// Not a Renderable: marshal whatever it is, as the contract dump does.
		return data, nil
	}
	projected := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		selected, err := selectFields(row.Fields(), w.Fields)
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, len(selected))
		for _, f := range selected {
			out[f.Name] = f.Raw
		}
		projected = append(projected, out)
	}
	// A single object stays an object under projection. Turning it into a
	// one-element array would make data's shape depend on whether --fields was
	// passed, which is exactly the kind of thing an agent cannot anticipate.
	if !collection && len(projected) == 1 {
		return projected[0], nil
	}
	return projected, nil
}

func (w *Writer) renderText(data any) error {
	rows, _, ok := asRows(data)
	if !ok {
		if len(w.Fields) > 0 {
			return errx.Usage("--fields is not supported for this command")
		}
		return w.encode(data)
	}
	for _, row := range rows {
		fields, err := selectFields(row.Fields(), w.Fields)
		if err != nil {
			return err
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

// renderableType is the reflect handle for the Renderable interface.
var renderableType = reflect.TypeOf((*Renderable)(nil)).Elem()

// asRows normalizes a single Renderable or a slice of them into rows.
//
// collection reports whether data was a slice, which is what decides if the
// envelope carries meta.
func asRows(data any) (rows []Renderable, collection, ok bool) {
	if data == nil {
		return nil, false, false
	}
	if rows, isSlice := sliceRows(data); isSlice {
		return rows, true, true
	}
	if row, isRow := data.(Renderable); isRow {
		return []Renderable{row}, false, true
	}
	return nil, false, false
}

// sliceRows boxes any slice whose element type implements Renderable.
//
// Reflection rather than a `data.([]Renderable)` assertion, because Go slices
// are not covariant: a []boardView does not assert to []Renderable even when
// boardView implements it. That assertion compiles, returns false at runtime,
// and the only symptom is a missing meta and a --fields flag that starts
// reporting "not supported" for one command. Making every future list command
// remember to hand-box its slice is a convention with a silent failure mode.
func sliceRows(data any) ([]Renderable, bool) {
	v := reflect.ValueOf(data)
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return nil, false
	}
	if !v.Type().Elem().Implements(renderableType) {
		return nil, false
	}
	rows := make([]Renderable, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		row, ok := v.Index(i).Interface().(Renderable)
		if !ok {
			// A nil element inside a slice of a Renderable interface type.
			return nil, false
		}
		rows = append(rows, row)
	}
	return rows, true
}

func fieldNames(fields []Field) string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}
