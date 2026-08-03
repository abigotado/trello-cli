package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abigotado-niko/trello-cli/internal/auth"
	"github.com/abigotado-niko/trello-cli/internal/errx"
	"github.com/spf13/cobra"
)

// harness runs the real command tree against injected streams, environment,
// and credential store, so no test touches the developer's environment, the OS
// keychain, or the network.
type harness struct {
	t      *testing.T
	app    *App
	stdout *os.File
	stderr *os.File
}

type fakeStore struct {
	creds   auth.Credentials
	deleted bool
	saved   *auth.Credentials
}

func (f *fakeStore) Load(context.Context) (auth.Credentials, error) { return f.creds, nil }
func (f *fakeStore) Save(_ context.Context, c auth.Credentials) error {
	f.saved = &c
	return nil
}
func (f *fakeStore) Delete(context.Context) error { f.deleted = true; return nil }

func newHarness(t *testing.T, envs map[string]string, store auth.Store) *harness {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatalf("create stderr: %v", err)
	}
	t.Cleanup(func() { stdout.Close(); stderr.Close() })

	if store == nil {
		store = &fakeStore{}
	}
	app := &App{
		lookupEnv: func(k string) (string, bool) { v, ok := envs[k]; return v, ok },
		store:     store,
		stdout:    stdout,
		stderr:    stderr,
	}
	return &harness{t: t, app: app, stdout: stdout, stderr: stderr}
}

func (h *harness) run(args ...string) errx.Code {
	h.t.Helper()
	root := h.app.NewRootCommand()
	// Regular files are not terminals, so the default format is already JSON;
	// this makes that explicit rather than incidental.
	return h.app.Run(context.Background(), root, args)
}

func (h *harness) out() string { return readAll(h.t, h.stdout) }
func (h *harness) err() string { return readAll(h.t, h.stderr) }

func readAll(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read %s: %v", f.Name(), err)
	}
	return string(b)
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want errx.Code
	}{
		{"contract succeeds", nil, []string{"contract"}, errx.CodeOK},
		{"help succeeds", nil, []string{"--help"}, errx.CodeOK},
		// Cobra rejects these before RunE with a plain error, which would exit
		// 1 — the code reserved for defects in this tool — unless the
		// positional-argument validator re-types them.
		{"unknown command is usage", nil, []string{"bogus"}, errx.CodeUsage},
		{"unknown subcommand is usage", nil, []string{"auth", "bogus"}, errx.CodeUsage},
		{"unknown flag is usage", nil, []string{"--nope"}, errx.CodeUsage},
		{"extra args are usage", nil, []string{"contract", "extra"}, errx.CodeUsage},
		{"bad output format is usage", nil, []string{"--output", "xml", "contract"}, errx.CodeUsage},
		{"bad timeout is usage", map[string]string{"TRELLO_CLI_TIMEOUT": "soon"}, []string{"contract"}, errx.CodeUsage},
		{"me without credentials is auth", nil, []string{"me"}, errx.CodeAuth},
		{"partial credentials is auth", map[string]string{auth.EnvAPIKey: "k"}, []string{"me"}, errx.CodeAuth},
		{"auth status is never an error", nil, []string{"auth", "status"}, errx.CodeOK},
		{"read-only blocks a mutating command", map[string]string{"TRELLO_CLI_READONLY": "1"}, []string{"auth", "logout"}, errx.CodeUsage},
		{"login without flags is usage", nil, []string{"auth", "login"}, errx.CodeUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.env, nil)
			if got := h.run(tt.args...); got != tt.want {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
					got, tt.want, h.out(), h.err())
			}
		})
	}
}

// The Go runtime terminates a panicking binary with status 2, which the
// contract assigns to a usage error. Without recovery an agent would read a
// nil dereference as "you called it wrong" and retry indefinitely.
func TestPanicSurfacesAsInternalNotUsage(t *testing.T) {
	h := newHarness(t, nil, nil)
	root := h.app.NewRootCommand()
	root.AddCommand(h.app.newCommand(
		&cobra.Command{Use: "explode", Args: cobra.NoArgs},
		func(context.Context, *cobra.Command, []string) error {
			panic("simulated defect")
		},
	))

	got := h.app.Run(context.Background(), root, []string{"explode"})
	if got == errx.CodeUsage {
		t.Fatal("a panic was reported as a usage error; an agent would retry it forever")
	}
	if got != errx.CodeInternal {
		t.Errorf("exit code = %d, want %d (internal)", got, errx.CodeInternal)
	}
	if !strings.Contains(h.err(), "simulated defect") {
		t.Errorf("the panic value should reach stderr, got:\n%s", h.err())
	}
}

func TestMeAgainstFakeTrello(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"5f","username":"nik","fullName":"Nik K","url":"https://trello.com/nik"}`))
	}))
	defer srv.Close()

	h := newHarness(t, map[string]string{
		auth.EnvAPIKey:        "k",
		auth.EnvToken:         "t",
		"TRELLO_CLI_BASE_URL": srv.URL,
	}, nil)

	if got := h.run("me"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	var env struct {
		OK   bool `json:"ok"`
		V    int  `json:"v"`
		Data struct {
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("stdout is not a valid envelope: %v\n%s", err, h.out())
	}
	if !env.OK || env.V != errx.EnvelopeVersion || env.Data.Username != "nik" {
		t.Errorf("envelope = %+v", env)
	}
}

// stdout carries only the envelope. A single stray line there corrupts every
// downstream parse, so verbose logging must not be tempted onto it.
func TestVerboseLoggingStaysOffStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"5f","username":"nik"}`))
	}))
	defer srv.Close()

	h := newHarness(t, map[string]string{
		auth.EnvAPIKey:        "k",
		auth.EnvToken:         "t",
		"TRELLO_CLI_BASE_URL": srv.URL,
	}, nil)

	if got := h.run("-v", "me"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	if err := json.Unmarshal([]byte(h.out()), &map[string]any{}); err != nil {
		t.Errorf("stdout is not pure JSON with -v: %v\n%s", err, h.out())
	}
}

func TestDryRunDoesNotMutate(t *testing.T) {
	store := &fakeStore{creds: auth.Credentials{APIKey: "k", Token: "t"}}
	h := newHarness(t, nil, store)

	if got := h.run("--dry-run", "auth", "logout"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	if store.deleted {
		t.Error("--dry-run deleted the stored credentials")
	}

	h2 := newHarness(t, nil, store)
	if got := h2.run("auth", "logout"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h2.err())
	}
	if !store.deleted {
		t.Error("logout without --dry-run should have deleted the credentials")
	}
}

// A destructive command must refuse without --yes. No command carries the
// annotation yet, so this asserts the middleware itself rather than waiting
// for the first delete command to get it wrong.
func TestDestructiveCommandRequiresConfirmation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want errx.Code
	}{
		{"unconfirmed is refused", []string{"nuke"}, errx.CodeConfirm},
		{"--yes proceeds", []string{"nuke", "--yes"}, errx.CodeOK},
		{"--dry-run proceeds without --yes", []string{"nuke", "--dry-run"}, errx.CodeOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			root := h.app.NewRootCommand()
			root.AddCommand(h.app.newCommand(
				&cobra.Command{
					Use:  "nuke",
					Args: cobra.NoArgs,
					Annotations: map[string]string{
						annotationMutates:     "true",
						annotationDestructive: "true",
					},
				},
				func(context.Context, *cobra.Command, []string) error { return nil },
			))
			if got := h.app.Run(context.Background(), root, tt.args); got != tt.want {
				t.Errorf("exit code = %d, want %d\nstderr: %s", got, tt.want, h.err())
			}
		})
	}
}

func TestAuthStatusNeverPrintsTheToken(t *testing.T) {
	const secret = "super-secret-token"
	h := newHarness(t, map[string]string{
		auth.EnvAPIKey: "abcdef123456",
		auth.EnvToken:  secret,
	}, nil)

	if got := h.run("auth", "status"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0", got)
	}
	combined := h.out() + h.err()
	if strings.Contains(combined, secret) {
		t.Errorf("auth status disclosed the token:\n%s", combined)
	}
	// The full API key must not appear either; only a short suffix.
	if strings.Contains(combined, "abcdef123456") {
		t.Errorf("auth status disclosed the full API key:\n%s", combined)
	}
	if !strings.Contains(combined, "3456") {
		t.Errorf("auth status should show a key suffix to tell accounts apart:\n%s", combined)
	}
}

func TestJSONAliasSelectsJSON(t *testing.T) {
	h := newHarness(t, nil, nil)
	if got := h.run("--json", "contract"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if err := json.Unmarshal([]byte(h.out()), &map[string]any{}); err != nil {
		t.Errorf("--json did not produce JSON: %v\n%s", err, h.out())
	}
}
