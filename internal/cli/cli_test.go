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

	"github.com/abigotado/trello-cli/internal/auth"
	"github.com/abigotado/trello-cli/internal/errx"
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

func TestAuthLoginDryRunDoesNotStore(t *testing.T) {
	store := &fakeStore{}
	h := newHarness(t, nil, store)
	if got := h.run("--dry-run", "auth", "login", "--api-key", "k", "--token", "t"); got != errx.CodeOK {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if store.saved != nil {
		t.Error("--dry-run wrote credentials to the store")
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
