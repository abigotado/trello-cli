package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/abigotado/trello-cli/internal/auth"
	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/skills"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	creds          auth.Credentials
	deleted        bool
	saved          *auth.Credentials
	savedAccount   string
	deletedAccount string
	loadedAccount  string
}

func (f *fakeStore) Load(_ context.Context, account string) (auth.Credentials, error) {
	f.loadedAccount = account
	return f.creds, nil
}
func (f *fakeStore) Save(_ context.Context, account string, c auth.Credentials) error {
	f.savedAccount = account
	f.saved = &c
	return nil
}
func (f *fakeStore) Delete(_ context.Context, account string) error {
	f.deletedAccount = account
	f.deleted = true
	return nil
}

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
	// Redirected so the account registry never lands in the developer's real
	// config directory.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	app := &App{
		lookupEnv: func(k string) (string, bool) { v, ok := envs[k]; return v, ok },
		store:     store,
		registry:  auth.NewRegistry(),
		stdout:    stdout,
		stderr:    stderr,
	}
	return &harness{t: t, app: app, stdout: stdout, stderr: stderr}
}

func (h *harness) run(args ...string) errx.Code {
	h.t.Helper()
	// Truncate first, so out() and err() always describe the run that just
	// happened rather than every run this harness has ever made.
	for _, f := range []*os.File{h.stdout, h.stderr} {
		if err := f.Truncate(0); err != nil {
			h.t.Fatalf("truncate %s: %v", f.Name(), err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			h.t.Fatalf("seek %s: %v", f.Name(), err)
		}
	}
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

// `auth login MY-TOKEN` is the first thing someone tries. cobra.NoArgs embeds
// the offending argument in its message, which would put a live credential in
// the error envelope on stdout, the agent transcript, and any capturing log.
func TestAuthLoginNeverEchoesAPositionalArgument(t *testing.T) {
	const secret = "SECRET-TOKEN-abc123"
	h := newHarness(t, nil, nil)

	if got := h.run("auth", "login", secret); got != errx.CodeUsage {
		t.Fatalf("exit code = %d, want %d (usage)", got, errx.CodeUsage)
	}
	combined := h.out() + h.err()
	if strings.Contains(combined, secret) {
		t.Errorf("the rejected argument was echoed back:\n%s", combined)
	}
	if !strings.Contains(combined, "--api-key") {
		t.Errorf("the error should point at the correct flags:\n%s", combined)
	}
}

// login and logout write the same stored credential, so both must answer to
// read-only mode. Gating only logout meant locking the tool down still allowed
// a credential write.
func TestReadOnlyBlocksLoginAndLogoutAlike(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "logout"},
		{"auth", "login", "--api-key", "k", "--token", "t"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			store := &fakeStore{}
			h := newHarness(t, map[string]string{"TRELLO_CLI_READONLY": "1"}, store)
			if got := h.run(args...); got != errx.CodeUsage {
				t.Errorf("exit code = %d, want %d (usage)", got, errx.CodeUsage)
			}
			if store.saved != nil || store.deleted {
				t.Error("read-only mode still let the command touch the store")
			}
		})
	}
}

func TestAuthLoginStoresTheGivenCredentials(t *testing.T) {
	store := &fakeStore{}
	h := newHarness(t, nil, store)

	if got := h.run("auth", "login", "--api-key", "key-1", "--token", "tok-1"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	if store.saved == nil {
		t.Fatal("login did not write to the store")
	}
	if store.saved.APIKey != "key-1" || store.saved.Token != "tok-1" {
		t.Errorf("stored %+v, want key-1/tok-1", *store.saved)
	}
	// The success envelope must not contain the token it just stored.
	if strings.Contains(h.out()+h.err(), "tok-1") {
		t.Error("login echoed the stored token")
	}
}

// A dry run of a credential write must be distinguishable from a real one on
// stdout alone. Returning the success shape with "authenticated": true is the
// one place a false "it worked" would be most costly.
func TestAuthDryRunIsDistinguishableFromSuccess(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		action string
	}{
		{"login", []string{"--dry-run", "auth", "login", "--api-key", "k", "--token", "t"}, "auth login"},
		{"logout", []string{"--dry-run", "auth", "logout"}, "auth logout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{creds: auth.Credentials{APIKey: "k", Token: "t"}}
			h := newHarness(t, nil, store)
			if got := h.run(tt.args...); got != errx.CodeOK {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
			}
			if store.saved != nil || store.deleted {
				t.Error("--dry-run touched the credential store")
			}
			var env struct {
				Data struct {
					DryRun bool   `json:"dryRun"`
					Action string `json:"action"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
				t.Fatalf("bad envelope: %v\n%s", err, h.out())
			}
			if !env.Data.DryRun {
				t.Errorf("stdout does not mark this as a dry run:\n%s", h.out())
			}
			if env.Data.Action != tt.action {
				t.Errorf("action = %q, want %q", env.Data.Action, tt.action)
			}
		})
	}
}

// End-to-end counterpart of the transport test: a timeout must reach the
// caller as retryable, not as an internal defect it is told never to retry.
func TestTimeoutReachesTheCallerAsRetryable(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(block); srv.Close() }()

	h := newHarness(t, map[string]string{
		auth.EnvAPIKey:        "k",
		auth.EnvToken:         "t",
		"TRELLO_CLI_BASE_URL": srv.URL,
		"TRELLO_CLI_TIMEOUT":  "80ms",
	}, nil)

	if got := h.run("me"); got != errx.CodeRetryable {
		t.Fatalf("exit code = %d, want %d (retryable)\nstdout: %s", got, errx.CodeRetryable, h.out())
	}
	if strings.Contains(h.out(), "do not retry") {
		t.Errorf("a timeout told the caller not to retry:\n%s", h.out())
	}
}

// 429 must surface through the whole stack with the server's error code and a
// retry_after the caller can act on.
func TestRateLimitReachesTheCallerWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"API_TOKEN_LIMIT_EXCEEDED","message":"Rate limit exceeded"}`))
	}))
	defer srv.Close()

	h := newHarness(t, map[string]string{
		auth.EnvAPIKey:        "k",
		auth.EnvToken:         "t",
		"TRELLO_CLI_BASE_URL": srv.URL,
	}, nil)

	if got := h.run("me"); got != errx.CodeRetryable {
		t.Fatalf("exit code = %d, want %d\nstdout: %s", got, errx.CodeRetryable, h.out())
	}
	var env struct {
		Error struct {
			Code       string `json:"code"`
			RetryAfter string `json:"retry_after"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if env.Error.Code != "API_TOKEN_LIMIT_EXCEEDED" {
		t.Errorf("error.code = %q, want the server's code", env.Error.Code)
	}
	if env.Error.RetryAfter == "" {
		t.Error("retry_after is empty; the caller has nothing to back off by")
	}
}

// trelloStub serves the handful of endpoints the read commands use, so the
// resolver is exercised end to end without a network.
func trelloStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/members/me/boards", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"000000000000000000000001","name":"Roadmap","shortLink":"aaaaaaaa"},
			{"id":"000000000000000000000002","name":"Roadmap 2026","shortLink":"bbbbbbbb"},
			{"id":"000000000000000000000003","name":"Personal","shortLink":"cccccccc"}]`))
	})
	mux.HandleFunc("/boards/000000000000000000000001/lists", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"000000000000000000000010","name":"Doing"},{"id":"000000000000000000000011","name":"Done"}]`))
	})
	mux.HandleFunc("/boards/000000000000000000000001/cards", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"000000000000000000000020","name":"Ship it","shortLink":"dddddddd","idList":"000000000000000000000010"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func readHarness(t *testing.T, srv *httptest.Server) *harness {
	t.Helper()
	// XDG_CACHE_HOME and HOME are redirected so the resolver's index never
	// lands in the developer's real cache directory.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "xdg"))
	return newHarness(t, map[string]string{
		auth.EnvAPIKey:        "k",
		auth.EnvToken:         "t",
		"TRELLO_CLI_BASE_URL": srv.URL,
	}, nil)
}

func TestNameResolutionThroughCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want errx.Code
	}{
		{"exact board name resolves", []string{"lists", "list", "--board", "Roadmap"}, errx.CodeOK},
		{"board id resolves", []string{"lists", "list", "--board", "000000000000000000000001"}, errx.CodeOK},
		{"explicit board id flag resolves", []string{"lists", "list", "--board-id", "000000000000000000000001"}, errx.CodeOK},
		// Eight alphanumerics is also an ordinary name, so a shortLink is
		// matched against the index rather than short-circuited on its shape.
		{"short link resolves", []string{"lists", "list", "--board", "aaaaaaaa"}, errx.CodeOK},
		{"ambiguous prefix exits 4", []string{"lists", "list", "--board", "Road"}, errx.CodeAmbiguous},
		{"unknown board exits 3", []string{"lists", "list", "--board", "Nope"}, errx.CodeNotFound},
		{"missing board is a usage error", []string{"lists", "list"}, errx.CodeUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := readHarness(t, trelloStub(t))
			if got := h.run(tt.args...); got != tt.want {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s", got, tt.want, h.out(), h.err())
			}
		})
	}
}

// Exit 4 is only useful if the caller can act on it in one more call, which
// means the candidates have to be in the envelope.
func TestAmbiguousBoardEnvelopeCarriesCandidates(t *testing.T) {
	h := readHarness(t, trelloStub(t))
	if got := h.run("lists", "list", "--board", "Road"); got != errx.CodeAmbiguous {
		t.Fatalf("exit code = %d, want %d", got, errx.CodeAmbiguous)
	}
	var env struct {
		Error struct {
			Code       string `json:"code"`
			Candidates []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"candidates"`
		} `json:"error"`
		Hint string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if env.Error.Code != "AMBIGUOUS_BOARD" {
		t.Errorf("error.code = %q", env.Error.Code)
	}
	if len(env.Error.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(env.Error.Candidates))
	}
	for _, c := range env.Error.Candidates {
		if c.ID == "" || c.Name == "" {
			t.Errorf("candidate is missing data: %+v", c)
		}
	}
	if env.Hint == "" {
		t.Error("no hint telling the caller how to disambiguate")
	}
}

func TestUnknownBoardEnvelopeCarriesSuggestions(t *testing.T) {
	h := readHarness(t, trelloStub(t))
	if got := h.run("lists", "list", "--board", "Rodmap"); got != errx.CodeNotFound {
		t.Fatalf("exit code = %d, want %d\n%s", got, errx.CodeNotFound, h.out())
	}
	var env struct {
		Error struct {
			Code       string `json:"code"`
			DidYouMean []struct {
				Name string `json:"name"`
			} `json:"did_you_mean"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if len(env.Error.DidYouMean) == 0 {
		t.Fatal("a one-character typo produced no suggestions")
	}
	if env.Error.DidYouMean[0].Name != "Roadmap" {
		t.Errorf("closest suggestion = %q", env.Error.DidYouMean[0].Name)
	}
}

// A listing is a collection, so it must carry meta the caller can count on.
func TestListingsCarryMeta(t *testing.T) {
	h := readHarness(t, trelloStub(t))
	if got := h.run("boards", "list"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\n%s", got, h.err())
	}
	var env struct {
		Data []map[string]any `json:"data"`
		Meta *struct {
			Count int `json:"count"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if env.Meta == nil {
		t.Fatal("a collection has no meta")
	}
	if env.Meta.Count != 3 || len(env.Data) != 3 {
		t.Errorf("count = %v, rows = %d, want 3 and 3", env.Meta, len(env.Data))
	}
}

// A card listing labels each card with its list name, so reading it does not
// force the caller into a second lookup.
func TestCardListingLabelsTheListName(t *testing.T) {
	h := readHarness(t, trelloStub(t))
	if got := h.run("cards", "list", "--board", "Roadmap"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	var env struct {
		Data []struct {
			Name string `json:"name"`
			List string `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if len(env.Data) != 1 || env.Data[0].List != "Doing" {
		t.Errorf("data = %+v, want the card labelled with list Doing", env.Data)
	}
}

// --fields must project the read commands too, since that is where output size
// actually matters.
func TestFieldsProjectionOnAListing(t *testing.T) {
	h := readHarness(t, trelloStub(t))
	if got := h.run("boards", "list", "--fields", "id,name"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\n%s", got, h.err())
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	for _, row := range env.Data {
		if len(row) != 2 {
			t.Errorf("row has %d keys, want 2: %v", len(row), row)
		}
	}
}

// writeStub extends the read stub with the mutation routes, and records every
// non-GET request so a test can prove a dry run sent none.
type writeStub struct {
	*httptest.Server
	mutations []string
}

func newWriteStub(t *testing.T) *writeStub {
	t.Helper()
	stub := &writeStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			stub.mutations = append(stub.mutations, r.Method+" "+r.URL.Path)
		}
		switch {
		case r.URL.Path == "/members/me/boards":
			_, _ = w.Write([]byte(`[{"id":"000000000000000000000001","name":"Roadmap","shortLink":"aaaaaaaa"},
				{"id":"000000000000000000000002","name":"Roadmap 2026","shortLink":"bbbbbbbb"}]`))
		case r.URL.Path == "/boards/000000000000000000000001/lists":
			_, _ = w.Write([]byte(`[{"id":"000000000000000000000010","name":"Doing"},{"id":"000000000000000000000011","name":"Done"}]`))
		case r.URL.Path == "/boards/000000000000000000000001/cards":
			_, _ = w.Write([]byte(`[{"id":"000000000000000000000020","name":"Ship it","idList":"000000000000000000000010"}]`))
		default:
			_, _ = w.Write([]byte(`{"id":"000000000000000000000099","name":"created"}`))
		}
	})
	stub.Server = httptest.NewServer(mux)
	t.Cleanup(stub.Close)
	return stub
}

func writeHarness(t *testing.T, stub *writeStub, extraEnv map[string]string) *harness {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "xdg"))
	envs := map[string]string{
		auth.EnvAPIKey:        "k",
		auth.EnvToken:         "t",
		"TRELLO_CLI_BASE_URL": stub.URL,
	}
	for k, v := range extraEnv {
		envs[k] = v
	}
	return newHarness(t, envs, nil)
}

// A dry run that only echoed the caller's input back would validate nothing.
// It must resolve every name to an id and then send no mutation at all.
func TestDryRunResolvesNamesAndSendsNoMutation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantAction string
		wantTarget map[string]string
	}{
		{
			name:       "cards create",
			args:       []string{"--dry-run", "cards", "create", "--board", "Roadmap", "--list", "Doing", "--name", "New"},
			wantAction: "cards create",
			wantTarget: map[string]string{"board": "000000000000000000000001", "list": "000000000000000000000010"},
		},
		{
			name:       "cards move",
			args:       []string{"--dry-run", "cards", "move", "--board", "Roadmap", "--card", "Ship it", "--list", "Done"},
			wantAction: "cards move",
			wantTarget: map[string]string{"board": "000000000000000000000001", "card": "000000000000000000000020"},
		},
		{
			name:       "lists create",
			args:       []string{"--dry-run", "lists", "create", "--board", "Roadmap", "--name", "Backlog"},
			wantAction: "lists create",
			wantTarget: map[string]string{"board": "000000000000000000000001"},
		},
		{
			// --dry-run stands in for --yes on a destructive command, so a
			// caller can always preview before committing to it.
			name:       "cards delete",
			args:       []string{"--dry-run", "cards", "delete", "--board", "Roadmap", "--card", "Ship it"},
			wantAction: "cards delete",
			wantTarget: map[string]string{"card": "000000000000000000000020"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newWriteStub(t)
			h := writeHarness(t, stub, nil)

			if got := h.run(tt.args...); got != errx.CodeOK {
				t.Fatalf("exit code = %d, want 0\nstdout: %s\nstderr: %s", got, h.out(), h.err())
			}
			if len(stub.mutations) != 0 {
				t.Errorf("--dry-run sent mutating requests: %v", stub.mutations)
			}
			var env struct {
				Data struct {
					DryRun bool              `json:"dryRun"`
					Action string            `json:"action"`
					Target map[string]string `json:"target"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
				t.Fatalf("bad envelope: %v\n%s", err, h.out())
			}
			if !env.Data.DryRun {
				t.Error("the plan is not flagged as a dry run")
			}
			if env.Data.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", env.Data.Action, tt.wantAction)
			}
			for k, want := range tt.wantTarget {
				if env.Data.Target[k] != want {
					t.Errorf("target[%q] = %q, want %q — a dry run must report the resolved id",
						k, env.Data.Target[k], want)
				}
			}
		})
	}
}

func TestMutationsActuallySendWithoutDryRun(t *testing.T) {
	stub := newWriteStub(t)
	h := writeHarness(t, stub, nil)

	if got := h.run("cards", "create", "--board", "Roadmap", "--list", "Doing", "--name", "New"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	if len(stub.mutations) == 0 {
		t.Fatal("no mutating request was sent")
	}
	if stub.mutations[0] != "POST /cards" {
		t.Errorf("sent %q, want POST /cards", stub.mutations[0])
	}
}

// Every mutating command must answer to read-only mode. Checking them as a set
// is what catches the next one that forgets the annotation.
func TestReadOnlyBlocksEveryMutatingCommand(t *testing.T) {
	commands := [][]string{
		{"lists", "create", "--board", "Roadmap", "--name", "X"},
		{"lists", "archive", "--board", "Roadmap", "--list", "Doing"},
		{"cards", "create", "--board", "Roadmap", "--list", "Doing", "--name", "X"},
		{"cards", "update", "--card-id", "c1", "--name", "X"},
		{"cards", "move", "--board", "Roadmap", "--card", "Ship it", "--list", "Done"},
		{"cards", "archive", "--card-id", "c1"},
		{"cards", "delete", "--card-id", "c1", "--yes"},
		{"labels", "add", "--board", "Roadmap", "--card", "Ship it", "--label", "red"},
		{"labels", "remove", "--board", "Roadmap", "--card", "Ship it", "--label", "red"},
		{"members", "assign", "--board", "Roadmap", "--card", "Ship it", "--member", "nik"},
		{"members", "unassign", "--board", "Roadmap", "--card", "Ship it", "--member", "nik"},
		{"comments", "add", "--card-id", "c1", "--text", "hi"},
		{"checklists", "create", "--card-id", "c1", "--name", "Steps"},
		{"checklists", "add-item", "--checklist-id", "cl1", "--name", "Step"},
		{"checklists", "toggle", "--card-id", "c1", "--item-id", "i1"},
		{"attachments", "add", "--card-id", "c1", "--url", "https://example.com"},
		{"auth", "login", "--api-key", "k", "--token", "t"},
		{"auth", "logout"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			stub := newWriteStub(t)
			h := writeHarness(t, stub, map[string]string{"TRELLO_CLI_READONLY": "1"})

			if got := h.run(args...); got != errx.CodeUsage {
				t.Errorf("exit code = %d, want %d (usage)\nstdout: %s", got, errx.CodeUsage, h.out())
			}
			if len(stub.mutations) != 0 {
				t.Errorf("read-only mode still sent %v", stub.mutations)
			}
		})
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	stub := newWriteStub(t)
	h := writeHarness(t, stub, nil)

	if got := h.run("cards", "delete", "--card-id", "000000000000000000000020"); got != errx.CodeConfirm {
		t.Fatalf("exit code = %d, want %d (confirmation required)", got, errx.CodeConfirm)
	}
	if len(stub.mutations) != 0 {
		t.Errorf("an unconfirmed delete still sent %v", stub.mutations)
	}

	stub2 := newWriteStub(t)
	h2 := writeHarness(t, stub2, nil)
	if got := h2.run("cards", "delete", "--card-id", "000000000000000000000020", "--yes"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h2.err())
	}
	if len(stub2.mutations) != 1 || !strings.HasPrefix(stub2.mutations[0], "DELETE ") {
		t.Errorf("confirmed delete sent %v", stub2.mutations)
	}
}

// Saving one round trip is not worth deleting the wrong card because a prefix
// happened to be unique at that moment.
func TestDeleteRefusesAPrefixMatch(t *testing.T) {
	stub := newWriteStub(t)
	h := writeHarness(t, stub, nil)

	// "Ship" is a unique prefix of "Ship it" and resolves fine for a read.
	if got := h.run("cards", "list", "--board", "Roadmap"); got != errx.CodeOK {
		t.Fatalf("setup listing failed: %d", got)
	}

	stub2 := newWriteStub(t)
	h2 := writeHarness(t, stub2, nil)
	got := h2.run("cards", "delete", "--board", "Roadmap", "--card", "Ship", "--yes")
	if got != errx.CodeNotFound {
		t.Errorf("exit code = %d, want %d: a destructive command must not accept a prefix", got, errx.CodeNotFound)
	}
	if len(stub2.mutations) != 0 {
		t.Errorf("a refused delete still sent %v", stub2.mutations)
	}

	// The exact name still works.
	stub3 := newWriteStub(t)
	h3 := writeHarness(t, stub3, nil)
	if got := h3.run("cards", "delete", "--board", "Roadmap", "--card", "Ship it", "--yes"); got != errx.CodeOK {
		t.Errorf("exit code = %d, want 0: an exact name must still resolve\n%s", got, h3.out())
	}
}

func TestAuthLoginStoresUnderTheNamedAccount(t *testing.T) {
	store := &fakeStore{}
	h := newHarness(t, nil, store)

	if got := h.run("--account", "work", "auth", "login", "--api-key", "k", "--token", "t"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	if store.savedAccount != "work" {
		t.Errorf("saved under account %q, want work", store.savedAccount)
	}
	// Without --account it goes to the conventional default.
	store2 := &fakeStore{}
	h2 := newHarness(t, nil, store2)
	if got := h2.run("auth", "login", "--api-key", "k", "--token", "t"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if store2.savedAccount != auth.DefaultAccount {
		t.Errorf("saved under account %q, want %q", store2.savedAccount, auth.DefaultAccount)
	}
}

func TestAuthLogoutRemovesOnlyTheNamedAccount(t *testing.T) {
	store := &fakeStore{creds: auth.Credentials{APIKey: "k", Token: "t"}}
	h := newHarness(t, nil, store)

	if got := h.run("--account", "personal", "auth", "logout"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	if store.deletedAccount != "personal" {
		t.Errorf("deleted account %q, want personal", store.deletedAccount)
	}
}

func TestAuthListEnumeratesAccountsAndMarksTheDefault(t *testing.T) {
	store := &fakeStore{creds: auth.Credentials{APIKey: "abcd1234", Token: "tok"}}
	h := newHarness(t, nil, store)

	for _, name := range []string{"work", "personal"} {
		if got := h.run("--account", name, "auth", "login", "--api-key", "abcd1234", "--token", "tok"); got != errx.CodeOK {
			t.Fatalf("login %s exit code = %d", name, got)
		}
	}
	if got := h.run("auth", "list", "--check"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}

	var env struct {
		Data []struct {
			Account       string `json:"account"`
			Default       bool   `json:"default"`
			Authenticated bool   `json:"authenticated"`
			Fingerprint   string `json:"tokenFingerprint"`
		} `json:"data"`
		Meta *struct {
			Count int `json:"count"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if env.Meta == nil || env.Meta.Count != 2 {
		t.Fatalf("meta = %+v, want a collection of 2", env.Meta)
	}
	// The first account logged in becomes the default.
	defaults := 0
	for _, a := range env.Data {
		if a.Default {
			defaults++
			if a.Account != "work" {
				t.Errorf("default is %q, want work", a.Account)
			}
		}
	}
	if defaults != 1 {
		t.Errorf("%d accounts marked default, want exactly 1", defaults)
	}
	// The listing must never disclose a token.
	if strings.Contains(h.out(), "tok\"") {
		t.Errorf("auth list disclosed a token:\n%s", h.out())
	}
	for _, a := range env.Data {
		if a.Fingerprint == "" {
			t.Errorf("account %q has no fingerprint to tell it apart", a.Account)
		}
	}
}

// Listing accounts must not touch the keychain. On macOS an unsigned binary
// raises a modal prompt per account, and an agent calling this to discover
// accounts would hang on the first invisible dialog.
func TestAuthListDoesNotReadTheKeychainByDefault(t *testing.T) {
	store := &fakeStore{creds: auth.Credentials{APIKey: "abcd1234", Token: "tok"}}
	h := newHarness(t, nil, store)
	for _, name := range []string{"work", "personal"} {
		if got := h.run("--account", name, "auth", "login", "--api-key", "abcd1234", "--token", "tok"); got != errx.CodeOK {
			t.Fatalf("login %s exit code = %d", name, got)
		}
	}

	store.loadedAccount = ""
	if got := h.run("auth", "list"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if store.loadedAccount != "" {
		t.Errorf("auth list read the keychain for %q without --check", store.loadedAccount)
	}
	// The names still have to be there — that is the point of the command.
	if !strings.Contains(h.out(), "work") || !strings.Contains(h.out(), "personal") {
		t.Errorf("auth list omitted account names:\n%s", h.out())
	}

	if got := h.run("auth", "list", "--check"); got != errx.CodeOK {
		t.Fatalf("--check exit code = %d, want 0", got)
	}
	if store.loadedAccount == "" {
		t.Error("--check did not verify any account")
	}
}

func TestAuthDefaultRequiresAKnownAccount(t *testing.T) {
	store := &fakeStore{}
	h := newHarness(t, nil, store)
	if got := h.run("--account", "work", "auth", "login", "--api-key", "k", "--token", "t"); got != errx.CodeOK {
		t.Fatalf("login exit code = %d", got)
	}

	// A default pointing at an account that does not exist would resolve to
	// nothing later, so it is refused up front.
	if got := h.run("auth", "default", "nope"); got != errx.CodeNotFound {
		t.Errorf("exit code = %d, want %d for an unknown account", got, errx.CodeNotFound)
	}
	if got := h.run("auth", "default", "work"); got != errx.CodeOK {
		t.Errorf("exit code = %d, want 0 for a known account\n%s", got, h.out())
	}
}

// --account must reach the data commands, not only the auth ones.
func TestAccountFlagSelectsCredentialsForDataCommands(t *testing.T) {
	stub := newWriteStub(t)
	store := &fakeStore{creds: auth.Credentials{APIKey: "k", Token: "t"}}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	h := newHarness(t, map[string]string{"TRELLO_CLI_BASE_URL": stub.URL}, store)

	if got := h.run("--account", "personal", "boards", "list"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstdout: %s\nstderr: %s", got, h.out(), h.err())
	}
	if store.loadedAccount != "personal" {
		t.Errorf("credentials were read from account %q, want personal", store.loadedAccount)
	}
}

// An account the caller named explicitly must be reported by name, not as a
// generic "not authenticated" the caller cannot act on. The code is auth, not
// not-found: the recovery is always "run auth login", the same as every other
// missing-credential case, and two codes for one recovery would make an agent
// branch on an implementation detail.
func TestUnknownAccountIsReportedByName(t *testing.T) {
	h := newHarness(t, nil, &fakeStore{})
	if got := h.run("--account", "ghost", "me"); got != errx.CodeAuth {
		t.Fatalf("exit code = %d, want %d\n%s", got, errx.CodeAuth, h.out())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(h.out()), &env); err != nil {
		t.Fatalf("bad envelope: %v\n%s", err, h.out())
	}
	if env.Error.Code != "UNKNOWN_ACCOUNT" {
		t.Errorf("error.code = %q, want UNKNOWN_ACCOUNT", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "ghost") {
		t.Errorf("the message should name the account, got %q", env.Error.Message)
	}
}

// There must be no command that mutates a global "current account".
func TestThereIsNoStatefulAccountSwitch(t *testing.T) {
	h := newHarness(t, nil, nil)
	for _, args := range [][]string{
		{"auth", "use", "work"},
		{"auth", "switch", "work"},
	} {
		if got := h.run(args...); got != errx.CodeUsage {
			t.Errorf("%v exit code = %d, want %d: account selection must stay per-invocation",
				args, got, errx.CodeUsage)
		}
	}
}

// cobra writes a bound flag straight into the variable behind it, so binding
// one objectRef to two subcommands makes them alias each other's parsed value.
// It is invisible while one command runs per process and becomes a wrong-target
// bug the moment that stops being true.
func TestNoObjectRefIsSharedBetweenSubcommands(t *testing.T) {
	app := NewApp()
	root := app.NewRootCommand()

	// Map every flag's underlying value pointer to the commands that bind it.
	owners := map[any][]string{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if c.HasSubCommands() {
				return // group commands hold no target flags of their own
			}
			key := any(f.Value)
			owners[key] = append(owners[key], c.CommandPath()+" --"+f.Name)
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	for _, sub := range root.Commands() {
		walk(sub)
	}

	for _, paths := range owners {
		if len(paths) > 1 {
			t.Errorf("one flag value is shared by %v", paths)
		}
	}
}

// Refusing a prefix for the card while accepting one for the board still
// deletes from whichever board the prefix happened to hit.
func TestDeleteRefusesAPrefixBoardToo(t *testing.T) {
	stub := newWriteStub(t)
	h := writeHarness(t, stub, nil)

	// "Road" prefix-matches both Roadmap and Roadmap 2026.
	got := h.run("cards", "delete", "--board", "Road", "--card", "Ship it", "--yes")
	if got == errx.CodeOK {
		t.Fatal("a prefix board was accepted for a destructive command")
	}
	if len(stub.mutations) != 0 {
		t.Errorf("a refused delete still sent %v", stub.mutations)
	}
}

// Every object was addressed unambiguously, so demanding a board the call
// never uses is pure friction.
func TestIdOnlyInvocationsDoNotDemandABoard(t *testing.T) {
	tests := [][]string{
		{"labels", "add", "--card-id", "000000000000000000000020", "--label-id", "000000000000000000000030"},
		{"labels", "remove", "--card-id", "000000000000000000000020", "--label-id", "000000000000000000000030"},
		{"members", "assign", "--card-id", "000000000000000000000020", "--member-id", "000000000000000000000040"},
		{"cards", "move", "--card-id", "000000000000000000000020", "--list-id", "000000000000000000000011"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			stub := newWriteStub(t)
			h := writeHarness(t, stub, nil)
			if got := h.run(args...); got != errx.CodeOK {
				t.Errorf("exit code = %d, want 0; nothing here needs a board\nstdout: %s", got, h.out())
			}
		})
	}
}

// Flags that are bound and never read are worse than absent: a caller passing
// them to guard against a wrong target gets no guard at all.
func TestChecklistAddItemHasNoUnusedFlags(t *testing.T) {
	app := NewApp()
	root := app.NewRootCommand()
	var addItem *cobra.Command
	for _, g := range root.Commands() {
		if g.Name() != "checklists" {
			continue
		}
		for _, sub := range g.Commands() {
			if sub.Name() == "add-item" {
				addItem = sub
			}
		}
	}
	if addItem == nil {
		t.Fatal("checklists add-item not found")
	}
	for _, unused := range []string{"board", "card"} {
		if addItem.Flags().Lookup(unused) != nil {
			t.Errorf("--%s is bound on checklists add-item but nothing reads it", unused)
		}
	}
}

// A requirement enforced in a command body with errx.Usage is invisible to the
// tree walk that generates the command reference. This derives the list from
// the tree so a new required flag cannot ship undocumented.
func TestCommandsEnforcingRequiredFlagsAreAnnotated(t *testing.T) {
	// Commands whose body rejects a missing flag, keyed by command path.
	// Derived by reading the errx.Usage("--x is required") sites.
	wantAnnotated := map[string][]string{
		"trello-cli lists create":        {"name"},
		"trello-cli cards create":        {"name"},
		"trello-cli comments add":        {"text"},
		"trello-cli checklists create":   {"name"},
		"trello-cli checklists add-item": {"checklist-id", "name"},
		"trello-cli checklists toggle":   {"item-id"},
		"trello-cli attachments add":     {"url"},
		"trello-cli auth login":          {"api-key", "token"},
	}

	app := NewApp()
	root := app.NewRootCommand()
	found := map[string]string{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if v, ok := c.Annotations[annotationRequires]; ok {
			found[c.CommandPath()] = v
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)

	for path, flags := range wantAnnotated {
		got, ok := found[path]
		if !ok {
			t.Errorf("%s enforces required flags but carries no %q annotation, so the generated reference omits them",
				path, annotationRequires)
			continue
		}
		want := strings.Join(flags, " ")
		if got != want {
			t.Errorf("%s annotation = %q, want %q", path, got, want)
		}
	}
}

// skills install writes to the local filesystem only. TRELLO_CLI_READONLY
// locks Trello, not this machine, so gating it there would make the lock mean
// two different things.
func TestSkillsInstallIsNotGatedByReadOnly(t *testing.T) {
	dest := t.TempDir()
	h := newHarness(t, map[string]string{"TRELLO_CLI_READONLY": "1"}, nil)
	if got := h.run("skills", "install", "--provider", string(skills.ProviderClaude), "--dest", dest); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstdout: %s\nstderr: %s", got, h.out(), h.err())
	}
	if _, err := os.Stat(filepath.Join(dest, "trello", "SKILL.md")); err != nil {
		t.Errorf("the skill was not installed: %v", err)
	}
}

// One --dest applied to three providers lands three payloads in one directory,
// each overwriting the last.
func TestSkillsInstallRefusesDestWithEveryProvider(t *testing.T) {
	h := newHarness(t, nil, nil)
	if got := h.run("skills", "install", "--dest", t.TempDir()); got != errx.CodeUsage {
		t.Errorf("exit code = %d, want %d", got, errx.CodeUsage)
	}
}

func TestSkillsInstallDryRunWritesNothing(t *testing.T) {
	dest := t.TempDir()
	h := newHarness(t, nil, nil)
	if got := h.run("--dry-run", "skills", "install", "--provider", string(skills.ProviderClaude), "--dest", dest); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("--dry-run created %d entries", len(entries))
	}
}

// The version is read from Go's build stamping, not injected at link time, so
// the parsing is what can silently go wrong. read is injected rather than
// relying on how the test binary itself happened to be built.
func TestBuildVersionReadsTheStamp(t *testing.T) {
	tests := []struct {
		name       string
		info       *debug.BuildInfo
		ok         bool
		wantVer    string
		wantCommit string
	}{
		{
			name: "a tagged build reports its tag",
			info: &debug.BuildInfo{
				GoVersion: "go1.24.1",
				Main:      debug.Module{Version: "v0.1.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc123"},
					{Key: "vcs.time", Value: "2026-08-03T00:00:00Z"},
				},
			},
			ok: true, wantVer: "v0.1.0", wantCommit: "abc123",
		},
		{
			// go install from the module proxy has no VCS information at all.
			name:    "a proxy build has a version but no commit",
			info:    &debug.BuildInfo{GoVersion: "go1.24.1", Main: debug.Module{Version: "v0.1.0"}},
			ok:      true,
			wantVer: "v0.1.0",
		},
		{
			// "(devel)" is a toolchain spelling with parentheses in it; the
			// contract reports a plain token.
			name:    "devel is normalised",
			info:    &debug.BuildInfo{GoVersion: "go1.24.1", Main: debug.Module{Version: "(devel)"}},
			ok:      true,
			wantVer: "devel",
		},
		{
			name:    "no build info at all",
			ok:      false,
			wantVer: "devel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildVersion(func() (*debug.BuildInfo, bool) { return tt.info, tt.ok })
			if got.Version != tt.wantVer {
				t.Errorf("version = %q, want %q", got.Version, tt.wantVer)
			}
			if got.Commit != tt.wantCommit {
				t.Errorf("commit = %q, want %q", got.Commit, tt.wantCommit)
			}
			if got.Go == "" || got.OS == "" || got.Arch == "" {
				t.Errorf("toolchain fields are incomplete: %+v", got)
			}
		})
	}
}

// Setting cobra.Command.Version would register a --version flag whose output
// is printed before PersistentPreRunE runs, so no output.Writer exists yet and
// a bare, envelope-less line lands on stdout with exit 0 — silent corruption
// for every parser. With no flag registered it falls through to the usage path.
func TestVersionIsASubcommandNotAFlag(t *testing.T) {
	h := newHarness(t, nil, nil)
	if got := h.run("--version"); got != errx.CodeUsage {
		t.Errorf("--version exit code = %d, want %d", got, errx.CodeUsage)
	}

	h2 := newHarness(t, nil, nil)
	if got := h2.run("version"); got != errx.CodeOK {
		t.Fatalf("version exit code = %d, want 0\nstderr: %s", got, h2.err())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(h2.out()), &env); err != nil {
		t.Fatalf("version did not print an envelope: %v\n%s", err, h2.out())
	}
	for _, key := range []string{"version", "commit", "commitTime", "go", "os", "arch"} {
		if _, ok := env.Data[key]; !ok {
			t.Errorf("version output is missing %q: %v", key, env.Data)
		}
	}
}

// version must work with no credentials and no home directory: it is the first
// thing anyone runs in a bug report.
func TestVersionNeedsNoCredentials(t *testing.T) {
	h := newHarness(t, map[string]string{}, nil)
	if got := h.run("version"); got != errx.CodeOK {
		t.Errorf("exit code = %d, want 0\nstderr: %s", got, h.err())
	}
}
