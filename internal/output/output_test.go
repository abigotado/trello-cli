package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abigotado-niko/trello-cli/internal/errx"
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
	meta, ok := env["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta missing: %v", env["meta"])
	}
	if meta["count"] != float64(1) {
		t.Errorf("meta.count = %v, want 1", meta["count"])
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
