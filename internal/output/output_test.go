package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/abigotado/trello-cli/internal/errx"
)

type card struct {
	id   string
	name string
	due  string
	open bool
}

func (c card) Fields() []Field {
	return []Field{
		{Name: "id", Value: c.id, Raw: c.id},
		{Name: "name", Value: c.name, Raw: c.name},
		{Name: "due", Value: c.due, Raw: c.due},
		{Name: "open", Value: boolText(c.open), Raw: c.open},
	}
}

func boolText(b bool) string {
	if b {
		return "open"
	}
	return "closed"
}

func newWriter(format Format, fields []string) (*Writer, *bytes.Buffer, *bytes.Buffer) {
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	return &Writer{Format: format, Fields: fields, Out: out, Err: errBuf}, out, errBuf
}

func TestSuccessEnvelopeShape(t *testing.T) {
	w, out, _ := newWriter(FormatJSON, nil)
	if err := w.Success(card{id: "1", name: "Ship it", open: true}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\n%s", err, out.String())
	}
	if env["ok"] != true {
		t.Errorf("ok = %v, want true", env["ok"])
	}
	if env["v"] != float64(errx.EnvelopeVersion) {
		t.Errorf("v = %v, want %d", env["v"], errx.EnvelopeVersion)
	}
	if _, ok := env["data"]; !ok {
		t.Error("envelope has no data")
	}
	if _, ok := env["error"]; ok {
		t.Error("a successful envelope must not carry an error key")
	}
	// meta means "this is a countable collection". A single object must not
	// carry it, or an agent cannot use its presence to tell a list from an
	// object without knowing every command by name.
	if _, ok := env["meta"]; ok {
		t.Errorf("a single object must not carry meta, got %v", env["meta"])
	}
}

func TestMetaMarksCollectionsOnly(t *testing.T) {
	tests := []struct {
		name     string
		data     any
		wantMeta bool
		wantN    float64
	}{
		{"single object has no meta", card{id: "1"}, false, 0},
		{"slice of two has meta", []card{{id: "1"}, {id: "2"}}, true, 2},
		{"empty slice still has meta", []card{}, true, 0},
		{"non-renderable payload has no meta", struct{ A int }{1}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, out, _ := newWriter(FormatJSON, nil)
			if err := w.Success(tt.data); err != nil {
				t.Fatalf("Success() error = %v", err)
			}
			var env map[string]any
			if err := json.Unmarshal(out.Bytes(), &env); err != nil {
				t.Fatalf("bad JSON: %v\n%s", err, out.String())
			}
			meta, has := env["meta"].(map[string]any)
			if has != tt.wantMeta {
				t.Fatalf("meta present = %v, want %v (envelope: %s)", has, tt.wantMeta, out.String())
			}
			if tt.wantMeta && meta["count"] != tt.wantN {
				t.Errorf("meta.count = %v, want %v", meta["count"], tt.wantN)
			}
		})
	}
}

// Go slices are not covariant: a []card does not type-assert to []Renderable
// even though card implements it. The original code used that assertion, so
// every future list command would have silently lost meta and had --fields
// start reporting "not supported" — with no compile error and no panic.
func TestConcreteSliceIsRecognizedAsACollection(t *testing.T) {
	w, out, _ := newWriter(FormatJSON, []string{"id"})
	// Deliberately a []card, not a []Renderable.
	if err := w.Success([]card{{id: "1"}, {id: "2"}}); err != nil {
		t.Fatalf("Success() on a concrete slice failed: %v", err)
	}
	var env struct {
		Data []map[string]any `json:"data"`
		Meta *Meta            `json:"meta"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out.String())
	}
	if env.Meta == nil || env.Meta.Count != 2 {
		t.Errorf("meta = %+v, want count 2", env.Meta)
	}
	if len(env.Data) != 2 {
		t.Errorf("data has %d rows, want 2", len(env.Data))
	}
}

func TestFailureEnvelopeAndExitCode(t *testing.T) {
	candidates := []errx.Candidate{
		{ID: "a", Name: "Roadmap", Kind: "board"},
		{ID: "b", Name: "Roadmap 2025", Kind: "board"},
	}
	tests := []struct {
		name       string
		err        error
		wantCode   errx.Code
		wantReason string
		wantKey    string
	}{
		{"ambiguous carries candidates", errx.Ambiguous("board", "Road", candidates), errx.CodeAmbiguous, "AMBIGUOUS_BOARD", "candidates"},
		{"not found carries did_you_mean", errx.NotFound("board", "Rodmap", candidates), errx.CodeNotFound, "NOT_FOUND_BOARD", "did_you_mean"},
		{"auth", errx.Auth("NOT_AUTHENTICATED", "no credentials"), errx.CodeAuth, "NOT_AUTHENTICATED", ""},
		{"usage", errx.Usage("bad flag"), errx.CodeUsage, "USAGE", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, out, _ := newWriter(FormatJSON, nil)
			if got := w.Failure(tt.err); got != tt.wantCode {
				t.Errorf("Failure() = %d, want %d", got, tt.wantCode)
			}
			var env struct {
				OK    bool `json:"ok"`
				V     int  `json:"v"`
				Error struct {
					Code       string           `json:"code"`
					Message    string           `json:"message"`
					Candidates []errx.Candidate `json:"candidates"`
					DidYouMean []errx.Candidate `json:"did_you_mean"`
				} `json:"error"`
				Hint string `json:"hint"`
			}
			if err := json.Unmarshal(out.Bytes(), &env); err != nil {
				t.Fatalf("envelope is not valid JSON: %v\n%s", err, out.String())
			}
			if env.OK {
				t.Error("ok = true on a failure envelope")
			}
			if env.V != errx.EnvelopeVersion {
				t.Errorf("v = %d, want %d", env.V, errx.EnvelopeVersion)
			}
			if env.Error.Code != tt.wantReason {
				t.Errorf("error.code = %q, want %q", env.Error.Code, tt.wantReason)
			}
			if env.Hint == "" {
				t.Error("every failure must carry a hint stating the next action")
			}
			switch tt.wantKey {
			case "candidates":
				if len(env.Error.Candidates) != 2 {
					t.Errorf("candidates = %d, want 2", len(env.Error.Candidates))
				}
			case "did_you_mean":
				if len(env.Error.DidYouMean) != 2 {
					t.Errorf("did_you_mean = %d, want 2", len(env.Error.DidYouMean))
				}
			}
		})
	}
}

// An agent that parses only stdout would otherwise see nothing at all when a
// command fails, and could not tell an error from an empty result.
func TestFailureEnvelopeGoesToStdout(t *testing.T) {
	w, out, errBuf := newWriter(FormatJSON, nil)
	w.Failure(errx.Usage("nope"))
	if out.Len() == 0 {
		t.Error("the error envelope must be written to stdout")
	}
	if errBuf.Len() != 0 {
		t.Errorf("stderr should stay empty in json mode, got %q", errBuf.String())
	}
}

// In text mode the situation is reversed: stdout carries results only, so a
// human-readable error belongs on stderr.
func TestTextFailureGoesToStderr(t *testing.T) {
	w, out, errBuf := newWriter(FormatText, nil)
	w.Failure(errx.Ambiguous("board", "Road", []errx.Candidate{{ID: "a", Name: "Roadmap"}}))
	if out.Len() != 0 {
		t.Errorf("stdout should stay empty on a text-mode failure, got %q", out.String())
	}
	got := errBuf.String()
	for _, want := range []string{"error:", "hint:", "Roadmap"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr missing %q:\n%s", want, got)
		}
	}
}

func TestUntypedErrorIsReportedAsInternal(t *testing.T) {
	w, out, _ := newWriter(FormatJSON, nil)
	if got := w.Failure(errors.New("raw failure")); got != errx.CodeInternal {
		t.Errorf("Failure() = %d, want %d", got, errx.CodeInternal)
	}
	if !strings.Contains(out.String(), "INTERNAL") {
		t.Errorf("envelope should report INTERNAL:\n%s", out.String())
	}
}

func TestFieldProjection(t *testing.T) {
	rows := []Renderable{
		card{id: "1", name: "A", due: "2026-01-01", open: true},
		card{id: "2", name: "B", open: false},
	}

	t.Run("json keeps only the requested fields with their real types", func(t *testing.T) {
		w, out, _ := newWriter(FormatJSON, []string{"id", "open"})
		if err := w.Success(rows); err != nil {
			t.Fatalf("Success() error = %v", err)
		}
		var env struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, out.String())
		}
		if len(env.Data) != 2 {
			t.Fatalf("data has %d rows, want 2", len(env.Data))
		}
		for _, row := range env.Data {
			if len(row) != 2 {
				t.Errorf("row has %d keys, want 2: %v", len(row), row)
			}
			if _, ok := row["name"]; ok {
				t.Error("an unrequested field leaked into the projection")
			}
		}
		// Raw carries the real type, so a boolean does not become a string.
		if env.Data[0]["open"] != true {
			t.Errorf("open = %v (%T), want boolean true", env.Data[0]["open"], env.Data[0]["open"])
		}
	})

	t.Run("unknown field is a usage error naming the alternatives", func(t *testing.T) {
		w, _, _ := newWriter(FormatJSON, []string{"nope"})
		err := w.Success(rows)
		if errx.ExitCode(err) != errx.CodeUsage {
			t.Fatalf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
		}
		if !strings.Contains(err.Error(), "id") {
			t.Errorf("the error should list the available fields, got %q", err.Error())
		}
	})

	t.Run("fields on a non-renderable value is a usage error", func(t *testing.T) {
		w, _, _ := newWriter(FormatJSON, []string{"id"})
		err := w.Success(struct{ A int }{1})
		if errx.ExitCode(err) != errx.CodeUsage {
			t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
		}
	})
}

func TestTextRendering(t *testing.T) {
	t.Run("one compact line per entity", func(t *testing.T) {
		w, out, _ := newWriter(FormatText, nil)
		rows := []Renderable{card{id: "1", name: "A", open: true}, card{id: "2", name: "B", open: false}}
		if err := w.Success(rows); err != nil {
			t.Fatalf("Success() error = %v", err)
		}
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2:\n%s", len(lines), out.String())
		}
	})

	// Trello leaves most optional fields blank. Padding them would end every
	// line in a run of separators and bury the values that are set.
	t.Run("empty values are dropped, not padded", func(t *testing.T) {
		w, out, _ := newWriter(FormatText, nil)
		if err := w.Success(card{id: "1", name: "A", open: true}); err != nil {
			t.Fatalf("Success() error = %v", err)
		}
		line := strings.TrimRight(out.String(), "\n")
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line has trailing whitespace: %q", line)
		}
		if strings.Contains(line, "   ") {
			t.Errorf("line contains a run of separators from an empty field: %q", line)
		}
	})

	// The opposite rule applies once the caller names its columns. Dropping an
	// empty one there slides the rest left, so column N stops meaning the same
	// thing from row to row — the reason someone projects at all.
	t.Run("an explicitly projected column is kept even when empty", func(t *testing.T) {
		w, out, _ := newWriter(FormatText, []string{"id", "due", "name"})
		rows := []Renderable{
			card{id: "1", name: "A", due: "2026-09-01", open: true},
			card{id: "2", name: "B", open: true},
		}
		if err := w.Success(rows); err != nil {
			t.Fatalf("Success() error = %v", err)
		}
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2:\n%s", len(lines), out.String())
		}
		for i, line := range lines {
			if got := len(strings.Split(line, "  ")); got != 3 {
				t.Errorf("row %d has %d columns, want 3: %q", i, got, line)
			}
		}
		if got := strings.Split(lines[1], "  ")[2]; got != "B" {
			t.Errorf("name landed in column %q on the row whose due is empty; want it still third", got)
		}
	})

	// A field with no human form is exactly the field someone reaches for by
	// name. Answering --fields open with a blank line made it unreachable in
	// text, while JSON reported the value.
	t.Run("a value-less field renders its raw when projected by name", func(t *testing.T) {
		tests := []struct {
			name  string
			field Field
			want  string
		}{
			{"boolean", Field{Name: "x", Value: "", Raw: true}, "true"},
			{"number", Field{Name: "x", Value: "", Raw: 1.5}, "1.5"},
			{"string raw", Field{Name: "x", Value: "", Raw: "idList"}, "idList"},
			{"slice", Field{Name: "x", Value: "", Raw: []string{"a", "b"}}, "[a b]"},
			{"nil interface", Field{Name: "x", Value: "", Raw: nil}, ""},
			{"typed nil map", Field{Name: "x", Value: "", Raw: map[string]string(nil)}, ""},
			{"value wins over raw", Field{Name: "x", Value: "shown", Raw: "hidden"}, "shown"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := textCell(tt.field); got != tt.want {
					t.Errorf("textCell() = %q, want %q", got, tt.want)
				}
			})
		}
	})
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"text", FormatText, false},
		{"json", FormatJSON, false},
		{"raw", FormatRaw, false},
		{"xml", "", true},
		{"", "", true},
		{"JSON", "", true},
	}
	for _, tt := range tests {
		t.Run("input="+tt.in, func(t *testing.T) {
			got, err := ParseFormat(tt.in)
			if tt.wantErr {
				if errx.ExitCode(err) != errx.CodeUsage {
					t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFormat(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Requiring an agent to remember a flag to get parseable output is a whole
// class of failed runs that costs a human nothing to avoid.
func TestDefaultFormatIsJSONWhenStdoutIsNotATerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()

	if got := DefaultFormat(f); got != FormatJSON {
		t.Errorf("DefaultFormat(regular file) = %q, want %q", got, FormatJSON)
	}

	closed, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	closed.Close()
	if got := DefaultFormat(closed); got != FormatJSON {
		t.Errorf("DefaultFormat(unstattable) = %q, want %q", got, FormatJSON)
	}
}

// The same typo used to be a usage error in json and a silently dropped column
// in text. One flag must not have two contracts depending on -o.
func TestUnknownFieldIsAUsageErrorInEveryFormat(t *testing.T) {
	tests := []struct {
		format Format
		fields []string
	}{
		{FormatJSON, []string{"bogus"}},
		{FormatText, []string{"bogus"}},
		{FormatJSON, []string{"id", "bogus"}},
		{FormatText, []string{"id", "bogus"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.format)+"/"+strings.Join(tt.fields, ","), func(t *testing.T) {
			w, out, _ := newWriter(tt.format, tt.fields)
			err := w.Success(card{id: "1", name: "A"})
			if errx.ExitCode(err) != errx.CodeUsage {
				t.Fatalf("exit code = %d, want %d; stdout was %q", errx.ExitCode(err), errx.CodeUsage, out.String())
			}
			if !strings.Contains(err.Error(), "bogus") {
				t.Errorf("error should name the offending field, got %q", err.Error())
			}
		})
	}
}

// Accepting --fields under -o raw and discarding it would give one flag a
// third behavior.
func TestFieldsIsRejectedWithRawOutput(t *testing.T) {
	w, _, _ := newWriter(FormatRaw, []string{"id"})
	err := w.Success(card{id: "1"})
	if errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
	}
}

// --fields must not change the shape of data. If a single object became a
// one-element array only when projected, an agent could not write one parser.
func TestProjectionPreservesObjectVersusArrayShape(t *testing.T) {
	single, out, _ := newWriter(FormatJSON, []string{"id"})
	if err := single.Success(card{id: "1"}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(env.Data)), "{") {
		t.Errorf("a projected single object must stay an object, got %s", env.Data)
	}

	list, out2, _ := newWriter(FormatJSON, []string{"id"})
	if err := list.Success([]card{{id: "1"}}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	if err := json.Unmarshal(out2.Bytes(), &env); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(env.Data)), "[") {
		t.Errorf("a projected collection must stay an array, got %s", env.Data)
	}
}

// One command must produce one JSON shape. Marshaling the struct directly when
// no projection was requested gave different key names and dropped fields the
// view computes, so --fields silently changed the schema rather than narrowing
// it.
func TestJSONShapeIsTheDeclaredFieldSetWithOrWithoutProjection(t *testing.T) {
	full, out, _ := newWriter(FormatJSON, nil)
	if err := full.Success(card{id: "1", name: "A", open: true}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var unprojected struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &unprojected); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out.String())
	}

	want := []string{"id", "name", "due", "open"}
	if len(unprojected.Data) != len(want) {
		t.Errorf("default output has %d keys, want %d: %v", len(unprojected.Data), len(want), unprojected.Data)
	}
	for _, name := range want {
		if _, ok := unprojected.Data[name]; !ok {
			t.Errorf("default output is missing declared field %q: %v", name, unprojected.Data)
		}
	}
	// Raw types survive, so a boolean is a boolean in both modes.
	if unprojected.Data["open"] != true {
		t.Errorf("open = %v (%T), want boolean true", unprojected.Data["open"], unprojected.Data["open"])
	}

	narrowed, out2, _ := newWriter(FormatJSON, []string{"id", "open"})
	if err := narrowed.Success(card{id: "1", name: "A", open: true}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var projected struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out2.Bytes(), &projected); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	// Projection must be a strict subset of the default shape, never a
	// different vocabulary.
	for name, v := range projected.Data {
		full, ok := unprojected.Data[name]
		if !ok {
			t.Errorf("--fields produced key %q that the default output does not have", name)
			continue
		}
		if full != v {
			t.Errorf("key %q = %v projected but %v by default", name, v, full)
		}
	}
}

// The envelope key set is the contract. Before the first tagged release the
// shape is still provisional, so v stays 1 while it settles; from that tag on,
// any change here is breaking and must bump errx.EnvelopeVersion.
//
// This test is the mechanism that makes that rule real. It fails on any
// rename, removal, or addition, so no envelope change can happen by accident —
// it has to be an edit to this list, which is the moment to decide about v.
func TestEnvelopeKeySetIsPinned(t *testing.T) {
	tests := []struct {
		name string
		emit func(*Writer) error
		want []string
	}{
		{
			name: "success with a single object",
			emit: func(w *Writer) error { return w.Success(card{id: "1", name: "A"}) },
			want: []string{"ok", "v", "data"},
		},
		{
			name: "success with a collection",
			emit: func(w *Writer) error { return w.Success([]card{{id: "1"}}) },
			want: []string{"ok", "v", "data", "meta"},
		},
		{
			name: "failure",
			emit: func(w *Writer) error { w.Failure(errx.Usage("nope")); return nil },
			want: []string{"ok", "v", "error", "hint"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, out, _ := newWriter(FormatJSON, nil)
			if err := tt.emit(w); err != nil {
				t.Fatalf("emit: %v", err)
			}
			var env map[string]any
			if err := json.Unmarshal(out.Bytes(), &env); err != nil {
				t.Fatalf("bad JSON: %v\n%s", err, out.String())
			}
			got := make([]string, 0, len(env))
			for k := range env {
				got = append(got, k)
			}
			sort.Strings(got)
			want := append([]string(nil), tt.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("envelope keys = %v, want %v.\nThis is a contract change: "+
					"decide whether errx.EnvelopeVersion must bump before editing this list.", got, want)
			}
		})
	}

	// Nested key sets, pinned for the same reason.
	t.Run("meta", func(t *testing.T) {
		w, out, _ := newWriter(FormatJSON, nil)
		if err := w.SuccessPage([]card{{id: "1"}}, true); err != nil {
			t.Fatalf("emit: %v", err)
		}
		var env struct {
			Meta map[string]any `json:"meta"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if len(env.Meta) != 2 || env.Meta["count"] == nil || env.Meta["truncated"] != true {
			t.Errorf("meta = %v, want exactly count and truncated", env.Meta)
		}
	})

	t.Run("error", func(t *testing.T) {
		w, out, _ := newWriter(FormatJSON, nil)
		w.Failure(errx.Ambiguous("board", "R", []errx.Candidate{{ID: "1", Name: "R1"}}))
		var env struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		for _, want := range []string{"code", "message", "candidates"} {
			if _, ok := env.Error[want]; !ok {
				t.Errorf("error body is missing %q: %v", want, env.Error)
			}
		}
	})
}

// -o raw was documented as Trello's untouched response payload. It is not: the
// client decodes into its own types first, so a field the tool does not model
// is already gone by the time the renderer sees it. Shipping that claim in the
// agent-facing skill would send a caller looking for a field that cannot
// appear.
func TestRawIsNotAPassthrough(t *testing.T) {
	w, out, _ := newWriter(FormatRaw, nil)
	if err := w.Success(card{id: "1", name: "A"}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out.String())
	}
	// No envelope, which is what raw is actually for.
	for _, wrapper := range []string{"ok", "v", "data", "meta"} {
		if _, ok := got[wrapper]; ok {
			t.Errorf("raw output carries the envelope key %q", wrapper)
		}
	}
	// And it cannot carry anything the type does not declare, which is the
	// claim that must never be made in the shipped skill.
	if _, ok := got["somethingTrelloReturnedButWeDoNotModel"]; ok {
		t.Error("raw somehow produced an unmodelled field")
	}
}

// A field can be worth having and still not be worth printing by default. A
// card description is the case: it can run to paragraphs, so putting one in
// every row of a large listing spends the caller's context on something it did
// not ask for — but leaving it unreadable altogether meant `cards update
// --desc` overwrote text nothing could show first.
func TestOnRequestFieldsAreSelectableButNotDefault(t *testing.T) {
	available := []Field{
		{Name: "id", Value: "1", Raw: "1"},
		{Name: "desc", Value: "long", Raw: "long", OnRequest: true},
		{Name: "name", Value: "Fix login", Raw: "Fix login"},
	}

	tests := []struct {
		name    string
		fields  []string
		want    []string
		wantErr bool
	}{
		{name: "default output omits it", want: []string{"id", "name"}},
		{name: "naming it selects it", fields: []string{"id", "desc"}, want: []string{"id", "desc"}},
		{name: "it can be the only field", fields: []string{"desc"}, want: []string{"desc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectFields(available, tt.fields)
			if err != nil {
				t.Fatalf("selectFields: %v", err)
			}
			names := make([]string, 0, len(got))
			for _, f := range got {
				names = append(names, f.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.want, ",") {
				t.Errorf("fields = %v, want %v", names, tt.want)
			}
		})
	}

	// Still rejected when misspelled, rather than silently dropped.
	if _, err := selectFields(available, []string{"descr"}); err == nil {
		t.Error("an unknown field was accepted")
	}
}
