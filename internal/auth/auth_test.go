package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/zalando/go-keyring"
)

// env builds a lookup over a fixed map, so no test depends on the developer's
// real environment.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

// fakeStore stands in for the keychain, recording which accounts were read.
type fakeStore struct {
	creds map[string]Credentials
	err   error
	read  []string
}

func newFakeStore(creds map[string]Credentials) *fakeStore {
	if creds == nil {
		creds = map[string]Credentials{}
	}
	return &fakeStore{creds: creds}
}

func (f *fakeStore) Load(_ context.Context, account string) (Credentials, error) {
	f.read = append(f.read, account)
	return f.creds[account], f.err
}
func (f *fakeStore) Save(_ context.Context, account string, c Credentials) error {
	f.creds[account] = c
	return f.err
}
func (f *fakeStore) Delete(_ context.Context, account string) error {
	delete(f.creds, account)
	return f.err
}

// mockKeyring swaps in go-keyring's in-memory provider and clears entries
// afterwards. Without it these tests would hit the real OS keychain: a modal
// prompt on macOS, and a D-Bus failure or hang on a Linux CI runner. The
// cleanup matters because MockInit is process-global.
func mockKeyring(t *testing.T, accounts ...string) {
	t.Helper()
	keyring.MockInit()
	t.Cleanup(func() {
		_ = keyring.Delete(KeyringService, legacyUser)
		for _, a := range accounts {
			_ = keyring.Delete(KeyringService, entryName(a))
		}
	})
}

// isolateConfigDir points os.UserConfigDir at a temp directory so no test
// writes into the developer's real config.
func isolateConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
}

func TestResolvePrecedence(t *testing.T) {
	stored := map[string]Credentials{
		"work":     {APIKey: "work-key", Token: "work-token"},
		"personal": {APIKey: "personal-key", Token: "personal-token"},
		"default":  {APIKey: "default-key", Token: "default-token"},
	}

	tests := []struct {
		name        string
		account     string
		env         map[string]string
		regDefault  string
		regAccounts []string
		wantKey     string
		wantSource  Source
		wantAccount string
		wantReason  string
	}{
		{
			name:        "--account wins over everything",
			account:     "personal",
			env:         map[string]string{EnvAPIKey: "env-key", EnvToken: "env-token", EnvAccount: "work"},
			regDefault:  "work",
			wantKey:     "personal-key",
			wantSource:  SourceKeyring,
			wantAccount: "personal",
		},
		{
			// Raw credentials are the CI and headless path, and must not be
			// overridden by a stored default the runner knows nothing about.
			name:       "raw env credentials beat a stored default",
			env:        map[string]string{EnvAPIKey: "env-key", EnvToken: "env-token"},
			regDefault: "work",
			wantKey:    "env-key",
			wantSource: SourceEnv,
		},
		{
			name:        "TRELLO_CLI_ACCOUNT beats the stored default",
			env:         map[string]string{EnvAccount: "personal"},
			regDefault:  "work",
			wantKey:     "personal-key",
			wantSource:  SourceKeyring,
			wantAccount: "personal",
		},
		{
			name:        "stored default is used when nothing is named",
			regDefault:  "work",
			wantKey:     "work-key",
			wantSource:  SourceKeyring,
			wantAccount: "work",
		},
		{
			name:        "a sole account is used without a default",
			regAccounts: []string{"personal"},
			wantKey:     "personal-key",
			wantSource:  SourceKeyring,
			wantAccount: "personal",
		},
		{
			name:        "falls back to the conventional default account",
			wantKey:     "default-key",
			wantSource:  SourceKeyring,
			wantAccount: "default",
		},
		{
			name:       "half-set environment is a hard error",
			env:        map[string]string{EnvAPIKey: "env-key"},
			regDefault: "work",
			wantReason: "PARTIAL_ENV_CREDENTIALS",
		},
		{
			name:       "a named account with no credentials is reported by name",
			account:    "missing",
			wantReason: "UNKNOWN_ACCOUNT",
		},
		{
			name:       "an invalid account name is a usage error",
			account:    "has space",
			wantReason: "USAGE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfigDir(t)
			reg := NewRegistry()
			for _, a := range tt.regAccounts {
				if err := reg.Add(a); err != nil {
					t.Fatalf("registry Add: %v", err)
				}
			}
			if tt.regDefault != "" {
				if err := reg.Add(tt.regDefault); err != nil {
					t.Fatalf("registry Add: %v", err)
				}
				if err := reg.SetDefault(tt.regDefault); err != nil {
					t.Fatalf("registry SetDefault: %v", err)
				}
			}

			r := Resolver{
				Lookup:   env(tt.env),
				Store:    newFakeStore(stored),
				Registry: reg,
				Account:  tt.account,
			}
			res, err := r.Resolve(context.Background())

			if tt.wantReason != "" {
				var typed *errx.Error
				if !errors.As(err, &typed) {
					t.Fatalf("error = %v, want an *errx.Error", err)
				}
				if typed.Reason != tt.wantReason {
					t.Errorf("reason = %q, want %q", typed.Reason, tt.wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if res.Credentials.APIKey != tt.wantKey {
				t.Errorf("APIKey = %q, want %q", res.Credentials.APIKey, tt.wantKey)
			}
			if res.Source != tt.wantSource {
				t.Errorf("source = %q, want %q", res.Source, tt.wantSource)
			}
			if res.Account != tt.wantAccount {
				t.Errorf("account = %q, want %q", res.Account, tt.wantAccount)
			}
		})
	}
}

// The environment path must be reachable without touching the keychain, or an
// agent on macOS hangs on a modal prompt it cannot see.
func TestRawEnvCredentialsNeverTouchTheKeychain(t *testing.T) {
	isolateConfigDir(t)
	store := newFakeStore(map[string]Credentials{"work": {APIKey: "k", Token: "t"}})
	reg := NewRegistry()
	if err := reg.Add("work"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	r := Resolver{
		Lookup:   env(map[string]string{EnvAPIKey: "env-key", EnvToken: "env-token"}),
		Store:    store,
		Registry: reg,
	}
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(store.read) != 0 {
		t.Errorf("the keychain was read for accounts %v", store.read)
	}
}

// An existing pre-accounts login must keep working rather than silently
// becoming "not authenticated" after an upgrade.
func TestLegacyEntryIsReadAsTheDefaultAccount(t *testing.T) {
	mockKeyring(t, DefaultAccount)
	if err := keyring.Set(KeyringService, legacyUser, `{"api_key":"old-key","token":"old-token"}`); err != nil {
		t.Fatalf("seed legacy entry: %v", err)
	}

	got, err := KeyringStore{}.Load(context.Background(), DefaultAccount)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.APIKey != "old-key" {
		t.Errorf("APIKey = %q, want the legacy entry", got.APIKey)
	}

	// Only the default account inherits it; a named account must not.
	other, err := KeyringStore{}.Load(context.Background(), "work")
	if err != nil {
		t.Fatalf("Load(work) error = %v", err)
	}
	if other.Valid() {
		t.Errorf("a named account read the legacy entry: %+v", other)
	}
}

func TestKeyringStoreIsolatesAccounts(t *testing.T) {
	mockKeyring(t, "work", "personal")
	ctx := context.Background()
	store := KeyringStore{}

	work := Credentials{APIKey: "wk", Token: "wt"}
	personal := Credentials{APIKey: "pk", Token: "pt"}
	if err := store.Save(ctx, "work", work); err != nil {
		t.Fatalf("Save(work): %v", err)
	}
	if err := store.Save(ctx, "personal", personal); err != nil {
		t.Fatalf("Save(personal): %v", err)
	}

	if got, _ := store.Load(ctx, "work"); got != work {
		t.Errorf("work = %+v, want %+v", got, work)
	}
	if got, _ := store.Load(ctx, "personal"); got != personal {
		t.Errorf("personal = %+v, want %+v", got, personal)
	}

	// Removing one must not disturb the other.
	if err := store.Delete(ctx, "work"); err != nil {
		t.Fatalf("Delete(work): %v", err)
	}
	if got, _ := store.Load(ctx, "work"); got.Valid() {
		t.Error("work survived deletion")
	}
	if got, _ := store.Load(ctx, "personal"); got != personal {
		t.Errorf("deleting work disturbed personal: %+v", got)
	}
	// Deleting an absent account succeeds, so logout is idempotent.
	if err := store.Delete(ctx, "work"); err != nil {
		t.Errorf("second Delete() error = %v", err)
	}
}

func TestValidateAccountName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"work", true},
		{"work-2", true},
		{"work_2", true},
		{"team.io", true},
		{"", false},
		{"has space", false},
		{" work", false},
		{"work ", false},
		{"work/other", false},
		{"account:work", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAccountName(tt.name)
			if tt.ok && err != nil {
				t.Errorf("ValidateAccountName(%q) = %v, want nil", tt.name, err)
			}
			if !tt.ok && errx.ExitCode(err) != errx.CodeUsage {
				t.Errorf("ValidateAccountName(%q) exit code = %d, want %d", tt.name, errx.ExitCode(err), errx.CodeUsage)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	isolateConfigDir(t)
	reg := NewRegistry()

	if got := reg.List(); len(got) != 0 {
		t.Errorf("a fresh registry lists %v", got)
	}

	// The first account becomes the default, so a single-account user never
	// has to think about accounts.
	if err := reg.Add("work"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if reg.Default() != "work" {
		t.Errorf("default = %q, want work", reg.Default())
	}

	if err := reg.Add("personal"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if reg.Default() != "work" {
		t.Errorf("adding a second account changed the default to %q", reg.Default())
	}
	if got := reg.List(); len(got) != 2 || got[0] != "personal" || got[1] != "work" {
		t.Errorf("List() = %v, want sorted [personal work]", got)
	}

	// Adding the same name twice must not duplicate it.
	if err := reg.Add("work"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := reg.List(); len(got) != 2 {
		t.Errorf("List() = %v after a duplicate Add", got)
	}

	if err := reg.SetDefault("personal"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if reg.Default() != "personal" {
		t.Errorf("default = %q", reg.Default())
	}

	// A default pointing at an unknown account would silently resolve to
	// nothing later, so it is refused up front.
	if err := reg.SetDefault("nope"); errx.ExitCode(err) != errx.CodeNotFound {
		t.Errorf("SetDefault(unknown) exit code = %d, want %d", errx.ExitCode(err), errx.CodeNotFound)
	}

	// Removing the default falls back to the only survivor.
	if err := reg.Remove("personal"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if reg.Default() != "work" {
		t.Errorf("after removing the default, default = %q, want work", reg.Default())
	}
}

func TestRegistryPersistsAcrossInstances(t *testing.T) {
	isolateConfigDir(t)
	first := NewRegistry()
	if err := first.Add("work"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := first.Add("personal"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := first.SetDefault("personal"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}

	// A separate process would see exactly this.
	second := NewRegistry()
	if got := second.List(); len(got) != 2 {
		t.Errorf("List() = %v, want two accounts", got)
	}
	if second.Default() != "personal" {
		t.Errorf("default = %q, want personal", second.Default())
	}
}

// The registry holds names so the keychain can be enumerated. It must never
// hold anything else.
func TestRegistryNeverStoresSecrets(t *testing.T) {
	isolateConfigDir(t)
	reg := NewRegistry()
	if err := reg.Add("work"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	raw, err := readFile(reg.Path())
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, forbidden := range []string{"token", "api_key", "secret"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Errorf("the registry file mentions %q:\n%s", forbidden, raw)
		}
	}
}

func TestFingerprintNeverContainsTheToken(t *testing.T) {
	creds := Credentials{APIKey: "key", Token: "super-secret-token-value"}
	fp := creds.Fingerprint()
	if fp == "" {
		t.Fatal("Fingerprint() is empty for a real token")
	}
	if strings.Contains(fp, creds.Token) || strings.Contains(creds.Token, fp) {
		t.Errorf("fingerprint %q discloses the token", fp)
	}
	if fp != (Credentials{Token: creds.Token}).Fingerprint() {
		t.Error("Fingerprint() is not deterministic")
	}
	if (Credentials{}).Fingerprint() != "" {
		t.Error("Fingerprint() should be empty when there is no token")
	}
}

func TestCredentialsValid(t *testing.T) {
	tests := []struct {
		name  string
		creds Credentials
		want  bool
	}{
		{"both set", Credentials{APIKey: "k", Token: "t"}, true},
		{"missing token", Credentials{APIKey: "k"}, false},
		{"missing key", Credentials{Token: "t"}, false},
		{"empty", Credentials{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.creds.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeyringStoreRejectsPartialCredentials(t *testing.T) {
	mockKeyring(t, "work")
	err := KeyringStore{}.Save(context.Background(), "work", Credentials{APIKey: "only-key"})
	if errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("exit code = %d, want %d (usage)", errx.ExitCode(err), errx.CodeUsage)
	}
}

func TestKeyringStoreReportsCorruptEntry(t *testing.T) {
	mockKeyring(t, "work")
	if err := keyring.Set(KeyringService, entryName("work"), "not json"); err != nil {
		t.Fatalf("seeding the keychain failed: %v", err)
	}
	_, err := KeyringStore{}.Load(context.Background(), "work")
	if errx.ExitCode(err) != errx.CodeAuth {
		t.Errorf("exit code = %d, want %d (auth)", errx.ExitCode(err), errx.CodeAuth)
	}
}

func TestCancelledContextStopsKeychainAccess(t *testing.T) {
	mockKeyring(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (KeyringStore{}).Load(ctx, DefaultAccount); !errors.Is(err, context.Canceled) {
		t.Errorf("Load() error = %v, want context.Canceled", err)
	}
}

// A registry that cannot be located must be inert, not fatal: losing the
// ability to enumerate accounts must not stop a caller who named one.
func TestNilRegistryIsSafe(t *testing.T) {
	var reg *Registry
	if got := reg.List(); got != nil {
		t.Errorf("List() = %v", got)
	}
	if got := reg.Default(); got != "" {
		t.Errorf("Default() = %q", got)
	}
	if reg.Has("work") {
		t.Error("Has() reported true on a nil registry")
	}
	if err := reg.Add("work"); err != nil {
		t.Errorf("Add() = %v", err)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
