package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abigotado-niko/trello-cli/internal/errx"
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

// fakeStore stands in for the keychain in tests that assert lookup order.
type fakeStore struct {
	creds  Credentials
	err    error
	loaded bool
}

func (f *fakeStore) Load(context.Context) (Credentials, error) {
	f.loaded = true
	return f.creds, f.err
}
func (f *fakeStore) Save(context.Context, Credentials) error { return nil }
func (f *fakeStore) Delete(context.Context) error            { return nil }

func TestResolveOrder(t *testing.T) {
	stored := Credentials{APIKey: "stored-key", Token: "stored-token"}

	tests := []struct {
		name         string
		env          map[string]string
		store        *fakeStore
		wantSource   Source
		wantKey      string
		wantCode     errx.Code
		wantReason   string
		storeTouched bool
	}{
		{
			// The order is load bearing: on macOS an unsigned, freshly built
			// binary raises a modal keychain prompt, which would hang an agent
			// on a dialog it cannot see. The environment path must not touch
			// the keychain at all.
			name:         "environment wins and the keychain is never read",
			env:          map[string]string{EnvAPIKey: "env-key", EnvToken: "env-token"},
			store:        &fakeStore{creds: stored},
			wantSource:   SourceEnv,
			wantKey:      "env-key",
			storeTouched: false,
		},
		{
			name:         "empty environment falls back to the keychain",
			env:          map[string]string{},
			store:        &fakeStore{creds: stored},
			wantSource:   SourceKeyring,
			wantKey:      "stored-key",
			storeTouched: true,
		},
		{
			// Falling through to the keychain here would silently use a
			// different account than the one the caller just tried to select.
			name:       "only the key set is a hard error",
			env:        map[string]string{EnvAPIKey: "env-key"},
			store:      &fakeStore{creds: stored},
			wantCode:   errx.CodeAuth,
			wantReason: "PARTIAL_ENV_CREDENTIALS",
		},
		{
			name:       "only the token set is a hard error",
			env:        map[string]string{EnvToken: "env-token"},
			store:      &fakeStore{creds: stored},
			wantCode:   errx.CodeAuth,
			wantReason: "PARTIAL_ENV_CREDENTIALS",
		},
		{
			name:         "empty string counts as unset",
			env:          map[string]string{EnvAPIKey: "", EnvToken: ""},
			store:        &fakeStore{creds: stored},
			wantSource:   SourceKeyring,
			wantKey:      "stored-key",
			storeTouched: true,
		},
		{
			name:         "nothing anywhere reports not authenticated",
			env:          map[string]string{},
			store:        &fakeStore{},
			wantCode:     errx.CodeAuth,
			wantReason:   "NOT_AUTHENTICATED",
			storeTouched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Resolver{Lookup: env(tt.env), Store: tt.store}
			creds, source, err := r.Resolve(context.Background())

			if tt.wantReason != "" {
				var typed *errx.Error
				if !errors.As(err, &typed) {
					t.Fatalf("error = %v, want an *errx.Error", err)
				}
				if typed.Reason != tt.wantReason {
					t.Errorf("reason = %q, want %q", typed.Reason, tt.wantReason)
				}
				if errx.ExitCode(err) != tt.wantCode {
					t.Errorf("exit code = %d, want %d", errx.ExitCode(err), tt.wantCode)
				}
			} else {
				if err != nil {
					t.Fatalf("Resolve() error = %v", err)
				}
				if source != tt.wantSource {
					t.Errorf("source = %q, want %q", source, tt.wantSource)
				}
				if creds.APIKey != tt.wantKey {
					t.Errorf("APIKey = %q, want %q", creds.APIKey, tt.wantKey)
				}
			}
			if tt.store.loaded != tt.storeTouched {
				t.Errorf("keychain read = %v, want %v", tt.store.loaded, tt.storeTouched)
			}
		})
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
	// Stable, so `auth status` can be used to tell two accounts apart.
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

// keyring.MockInit swaps in an in-memory provider. Without it these tests
// would hit the real OS keychain: a modal prompt on macOS, and a D-Bus failure
// or hang on a Linux CI runner.
func TestKeyringStoreRoundTrip(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	store := KeyringStore{}

	// A missing entry is not an error; it reports empty so the caller can fall
	// through to reporting "not authenticated".
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() on empty keychain error = %v", err)
	}
	if got.Valid() {
		t.Errorf("Load() on empty keychain returned %+v", got)
	}

	want := Credentials{APIKey: "abc123", Token: "tok456"}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}

	if err := store.Delete(ctx); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	// Deleting an already-absent entry succeeds, so logout is idempotent.
	if err := store.Delete(ctx); err != nil {
		t.Errorf("second Delete() error = %v, want nil", err)
	}
}

func TestKeyringStoreRejectsPartialCredentials(t *testing.T) {
	keyring.MockInit()
	err := KeyringStore{}.Save(context.Background(), Credentials{APIKey: "only-key"})
	if errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("exit code = %d, want %d (usage)", errx.ExitCode(err), errx.CodeUsage)
	}
}

func TestKeyringStoreReportsCorruptEntry(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set(KeyringService, keyringUser, "not json"); err != nil {
		t.Fatalf("seeding the keychain failed: %v", err)
	}
	_, err := KeyringStore{}.Load(context.Background())
	if errx.ExitCode(err) != errx.CodeAuth {
		t.Errorf("exit code = %d, want %d (auth)", errx.ExitCode(err), errx.CodeAuth)
	}
}

func TestCancelledContextStopsKeychainAccess(t *testing.T) {
	keyring.MockInit()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (KeyringStore{}).Load(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Load() error = %v, want context.Canceled", err)
	}
}
