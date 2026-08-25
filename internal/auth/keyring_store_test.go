package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abigotado/trello-cli/internal/errx"
)

type backendEntry struct {
	service string
	account string
}

type backendCall struct {
	operation     string
	entry         backendEntry
	legacyAccount string
	value         string
}

type fakeKeyringBackend struct {
	entries      map[backendEntry]string
	getErrors    map[backendEntry]error
	setError     error
	migrateError error
	deleteErrors map[backendEntry]error
	calls        []backendCall
}

func newFakeKeyringBackend(entries map[backendEntry]string) *fakeKeyringBackend {
	if entries == nil {
		entries = make(map[backendEntry]string)
	}
	return &fakeKeyringBackend{
		entries:      entries,
		getErrors:    make(map[backendEntry]error),
		deleteErrors: make(map[backendEntry]error),
	}
}

func (f *fakeKeyringBackend) get(_ context.Context, service, account string) (string, error) {
	entry := backendEntry{service: service, account: account}
	f.calls = append(f.calls, backendCall{operation: "get", entry: entry})
	if err := f.getErrors[entry]; err != nil {
		return "", err
	}
	value, ok := f.entries[entry]
	if !ok {
		return "", errKeyringNotFound
	}
	return value, nil
}

func (f *fakeKeyringBackend) set(_ context.Context, service, account, value string) error {
	entry := backendEntry{service: service, account: account}
	f.calls = append(f.calls, backendCall{operation: "set", entry: entry, value: value})
	if f.setError != nil {
		return f.setError
	}
	f.entries[entry] = value
	return nil
}

func (f *fakeKeyringBackend) setForLogin(_ context.Context, service, primaryAccount, legacyAccount, value string) error {
	entry := backendEntry{service: service, account: primaryAccount}
	f.calls = append(f.calls, backendCall{
		operation:     "setForLogin",
		entry:         entry,
		legacyAccount: legacyAccount,
		value:         value,
	})
	if f.setError != nil {
		return f.setError
	}
	f.entries[entry] = value
	return nil
}

func (f *fakeKeyringBackend) migrate(_ context.Context, service, primaryAccount, legacyAccount string) error {
	f.calls = append(f.calls, backendCall{
		operation:     "migrate",
		entry:         backendEntry{service: service, account: primaryAccount},
		legacyAccount: legacyAccount,
	})
	return f.migrateError
}

func (f *fakeKeyringBackend) delete(_ context.Context, service, account string) error {
	entry := backendEntry{service: service, account: account}
	f.calls = append(f.calls, backendCall{operation: "delete", entry: entry})
	if err := f.deleteErrors[entry]; err != nil {
		return err
	}
	if _, ok := f.entries[entry]; !ok {
		return errKeyringNotFound
	}
	delete(f.entries, entry)
	return nil
}

func TestKeyringStoreUsesExactServiceAndAccountSchema(t *testing.T) {
	ctx := context.Background()
	wantEntry := backendEntry{service: KeyringService, account: "account:work"}
	creds := Credentials{APIKey: "schema-key", Token: "schema-token"}
	backend := newFakeKeyringBackend(nil)
	store := KeyringStore{backend: backend}

	if err := store.Save(ctx, "work", creds); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(ctx, "work")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != creds {
		t.Error("Load() did not return the saved credentials")
	}
	if err := store.Delete(ctx, "work"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if len(backend.calls) != 3 {
		t.Fatalf("backend calls = %d, want 3", len(backend.calls))
	}
	for i, wantOperation := range []string{"set", "get", "delete"} {
		call := backend.calls[i]
		if call.operation != wantOperation {
			t.Errorf("call %d operation = %q, want %q", i, call.operation, wantOperation)
		}
		if call.entry != wantEntry {
			t.Errorf("call %d service/account = %q/%q, want %q/%q", i, call.entry.service, call.entry.account, wantEntry.service, wantEntry.account)
		}
	}
	if got, want := backend.calls[0].value, `{"api_key":"schema-key","token":"schema-token"}`; got != want {
		t.Error("Save() changed the logical JSON passed to the platform backend")
	}
}

func TestKeyringStoreSaveForLoginUsesExplicitCapability(t *testing.T) {
	tests := []struct {
		name              string
		account           string
		wantPrimary       string
		wantLegacyAccount string
	}{
		{
			name:              "default login includes legacy compatibility account",
			account:           DefaultAccount,
			wantPrimary:       entryName(DefaultAccount),
			wantLegacyAccount: legacyUser,
		},
		{
			name:        "named login has no legacy compatibility account",
			account:     "work",
			wantPrimary: entryName("work"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(nil)
			store := KeyringStore{backend: backend}
			if err := store.SaveForLogin(context.Background(), tt.account, Credentials{APIKey: "login-key", Token: "login-token"}); err != nil {
				t.Fatalf("SaveForLogin() error = %v", err)
			}
			if len(backend.calls) != 1 {
				t.Fatalf("backend calls = %d, want 1", len(backend.calls))
			}
			call := backend.calls[0]
			if call.operation != "setForLogin" || call.entry.service != KeyringService || call.entry.account != tt.wantPrimary || call.legacyAccount != tt.wantLegacyAccount {
				t.Errorf("SaveForLogin() backend target = %q %q/%q legacy %q, want setForLogin %q/%q legacy %q", call.operation, call.entry.service, call.entry.account, call.legacyAccount, KeyringService, tt.wantPrimary, tt.wantLegacyAccount)
			}
			if call.value != `{"api_key":"login-key","token":"login-token"}` {
				t.Error("SaveForLogin() changed the logical JSON passed to the platform backend")
			}
		})
	}
}

func TestKeyringStoreMigrateUsesExactSchemaWithoutCredentialAccess(t *testing.T) {
	const valueMarker = "stored-value-must-remain-untouched"
	tests := []struct {
		name        string
		account     string
		wantPrimary string
		wantLegacy  string
	}{
		{
			name:        "default includes primary then compatible legacy account",
			account:     DefaultAccount,
			wantPrimary: entryName(DefaultAccount),
			wantLegacy:  legacyUser,
		},
		{
			name:        "named account never includes the legacy credentials account",
			account:     "work",
			wantPrimary: entryName("work"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary := backendEntry{service: KeyringService, account: tt.wantPrimary}
			entries := map[backendEntry]string{primary: valueMarker}
			if tt.wantLegacy != "" {
				entries[backendEntry{service: KeyringService, account: tt.wantLegacy}] = valueMarker
			}
			backend := newFakeKeyringBackend(entries)

			if err := (KeyringStore{backend: backend}).MigrateKeychain(context.Background(), tt.account); err != nil {
				t.Fatalf("MigrateKeychain() error = %v", err)
			}
			if len(backend.calls) != 1 {
				t.Fatalf("backend calls = %d, want exactly one migration call", len(backend.calls))
			}
			call := backend.calls[0]
			if call.operation != "migrate" || call.entry != primary || call.legacyAccount != tt.wantLegacy {
				t.Errorf(
					"migration target = %q %q/%q legacy %q, want migrate %q/%q legacy %q",
					call.operation,
					call.entry.service,
					call.entry.account,
					call.legacyAccount,
					KeyringService,
					tt.wantPrimary,
					tt.wantLegacy,
				)
			}
			if call.value != "" {
				t.Error("migration passed a credential value to the backend")
			}
			for entry, value := range backend.entries {
				if value != valueMarker {
					t.Errorf("migration changed stored value for %q/%q", entry.service, entry.account)
				}
			}
		})
	}
}

func TestKeyringStoreMigrateErrorsUseExactAuthContractWithoutCredentialValues(t *testing.T) {
	const valueMarker = "migration-value-must-not-appear"
	backendFailure := errors.New("migration backend unavailable")
	tests := []struct {
		name       string
		backendErr error
		wantReason string
		wantHint   string
	}{
		{
			name:       "missing entry directs the account to login",
			backendErr: errKeyringNotFound,
			wantReason: "KEYRING_ENTRY_NOT_FOUND",
			wantHint:   "run 'trello-cli auth login --account work'",
		},
		{
			name:       "blocked interaction requires an interactive session for the exact account",
			backendErr: errKeyringInteractionNotAllowed,
			wantReason: "KEYRING_MIGRATION_BLOCKED",
			wantHint:   "run from an interactive macOS session: 'trello-cli auth migrate-keychain --account work'",
		},
		{
			name:       "unsupported migration directs the account to login",
			backendErr: errKeyringMigrationUnsupported,
			wantReason: "KEYRING_MIGRATION_UNAVAILABLE",
			wantHint:   "run 'trello-cli auth login --account work'",
		},
		{
			name:       "explicit migration cancellation directs the account to retry migration",
			backendErr: errKeyringUserCanceled,
			wantReason: "KEYRING_MIGRATION_CANCELED",
			wantHint:   "retry 'trello-cli auth migrate-keychain --account work'",
		},
		{
			name:       "other backend errors keep the recovery account specific",
			backendErr: backendFailure,
			wantReason: "KEYRING_MIGRATION_UNAVAILABLE",
			wantHint:   "run 'trello-cli auth login --account work'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := backendEntry{service: KeyringService, account: entryName("work")}
			backend := newFakeKeyringBackend(map[backendEntry]string{entry: valueMarker})
			backend.migrateError = tt.backendErr

			err := (KeyringStore{backend: backend}).MigrateKeychain(context.Background(), "work")
			assertAuthError(t, err, tt.wantReason)
			var typed *errx.Error
			if !errors.As(err, &typed) {
				t.Fatal("error is not an *errx.Error")
			}
			if typed.Hint != tt.wantHint {
				t.Errorf("hint = %q, want %q", typed.Hint, tt.wantHint)
			}
			if strings.Contains(err.Error(), valueMarker) {
				t.Error("migration error disclosed the stored value marker")
			}
			if len(backend.calls) != 1 || backend.calls[0].operation != "migrate" || backend.calls[0].value != "" {
				t.Errorf("backend calls = %+v, want one value-free migration call", backend.calls)
			}
		})
	}
}

func TestKeyringStoreMigratePropagatesBackendContextCancellation(t *testing.T) {
	backend := newFakeKeyringBackend(nil)
	backend.migrateError = context.Canceled

	err := (KeyringStore{backend: backend}).MigrateKeychain(context.Background(), "work")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestKeyringStoreWithoutMigrationCapabilityReturnsTypedUnavailable(t *testing.T) {
	backend := newFakeKeyringBackend(nil)
	store := KeyringStore{backend: struct{ keyringBackend }{keyringBackend: backend}}

	err := store.MigrateKeychain(context.Background(), "work")
	assertAuthError(t, err, "KEYRING_MIGRATION_UNAVAILABLE")
	if len(backend.calls) != 0 {
		t.Errorf("unsupported migration touched the backend: %+v", backend.calls)
	}
}

func TestKeyringStoreLoadLegacyFallback(t *testing.T) {
	modernEntry := backendEntry{service: KeyringService, account: entryName(DefaultAccount)}
	legacyEntry := backendEntry{service: KeyringService, account: legacyUser}
	namedEntry := backendEntry{service: KeyringService, account: entryName("work")}
	modernJSON := `{"api_key":"modern-key","token":"modern-token"}`
	legacyJSON := `{"api_key":"legacy-key","token":"legacy-token"}`

	tests := []struct {
		name         string
		account      string
		entries      map[backendEntry]string
		wantKey      string
		wantAccounts []string
	}{
		{
			name:         "modern default entry wins without reading legacy",
			account:      DefaultAccount,
			entries:      map[backendEntry]string{modernEntry: modernJSON, legacyEntry: legacyJSON},
			wantKey:      "modern-key",
			wantAccounts: []string{entryName(DefaultAccount)},
		},
		{
			name:         "missing modern default falls back to legacy entry",
			account:      DefaultAccount,
			entries:      map[backendEntry]string{legacyEntry: legacyJSON},
			wantKey:      "legacy-key",
			wantAccounts: []string{entryName(DefaultAccount), legacyUser},
		},
		{
			name:         "missing modern and legacy entries return an empty result",
			account:      DefaultAccount,
			wantAccounts: []string{entryName(DefaultAccount), legacyUser},
		},
		{
			name:         "named account never falls back to legacy entry",
			account:      "work",
			entries:      map[backendEntry]string{legacyEntry: legacyJSON},
			wantAccounts: []string{namedEntry.account},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(tt.entries)
			got, err := (KeyringStore{backend: backend}).Load(context.Background(), tt.account)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.APIKey != tt.wantKey {
				t.Errorf("API key did not come from the expected entry")
			}
			if len(backend.calls) != len(tt.wantAccounts) {
				t.Fatalf("backend calls = %d, want %d", len(backend.calls), len(tt.wantAccounts))
			}
			for i, wantAccount := range tt.wantAccounts {
				call := backend.calls[i]
				if call.operation != "get" || call.entry.service != KeyringService || call.entry.account != wantAccount {
					t.Errorf("call %d = %q %q/%q, want get %q/%q", i, call.operation, call.entry.service, call.entry.account, KeyringService, wantAccount)
				}
			}
		})
	}
}

func TestKeyringStoreDeleteLegacyPolicy(t *testing.T) {
	modernEntry := backendEntry{service: KeyringService, account: entryName(DefaultAccount)}
	legacyEntry := backendEntry{service: KeyringService, account: legacyUser}
	namedEntry := backendEntry{service: KeyringService, account: entryName("work")}

	tests := []struct {
		name         string
		account      string
		entries      map[backendEntry]string
		wantAccounts []string
	}{
		{
			name:         "default with both entries deletes legacy before primary",
			account:      DefaultAccount,
			entries:      map[backendEntry]string{modernEntry: "modern", legacyEntry: "legacy"},
			wantAccounts: []string{legacyEntry.account, modernEntry.account},
		},
		{
			name:         "legacy-only default still deletes primary after legacy",
			account:      DefaultAccount,
			entries:      map[backendEntry]string{legacyEntry: "legacy"},
			wantAccounts: []string{legacyEntry.account, modernEntry.account},
		},
		{
			name:         "primary-only default checks legacy before deleting primary",
			account:      DefaultAccount,
			entries:      map[backendEntry]string{modernEntry: "modern"},
			wantAccounts: []string{legacyEntry.account, modernEntry.account},
		},
		{
			name:         "repeated default logout remains idempotent",
			account:      DefaultAccount,
			wantAccounts: []string{legacyEntry.account, modernEntry.account},
		},
		{
			name:         "missing named account is idempotent without legacy deletion",
			account:      "work",
			entries:      map[backendEntry]string{legacyEntry: "legacy"},
			wantAccounts: []string{namedEntry.account},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(tt.entries)
			if err := (KeyringStore{backend: backend}).Delete(context.Background(), tt.account); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			if len(backend.calls) != len(tt.wantAccounts) {
				t.Fatalf("backend calls = %d, want %d", len(backend.calls), len(tt.wantAccounts))
			}
			for i, wantAccount := range tt.wantAccounts {
				call := backend.calls[i]
				if call.operation != "delete" || call.entry.service != KeyringService || call.entry.account != wantAccount {
					t.Errorf("call %d = %q %q/%q, want delete %q/%q", i, call.operation, call.entry.service, call.entry.account, KeyringService, wantAccount)
				}
			}
		})
	}
}

func TestKeyringStoreDefaultDeleteStopsWhenLegacyDeletionFails(t *testing.T) {
	legacyEntry := backendEntry{service: KeyringService, account: legacyUser}
	primaryEntry := backendEntry{service: KeyringService, account: entryName(DefaultAccount)}
	backendFailure := errors.New("legacy deletion unavailable")
	backend := newFakeKeyringBackend(map[backendEntry]string{
		legacyEntry:  "legacy",
		primaryEntry: "primary",
	})
	backend.deleteErrors[legacyEntry] = backendFailure

	err := (KeyringStore{backend: backend}).Delete(context.Background(), DefaultAccount)
	assertAuthError(t, err, "KEYRING_UNAVAILABLE")
	if len(backend.calls) != 1 || backend.calls[0].entry != legacyEntry {
		t.Error("default deletion continued to the primary entry after legacy deletion failed")
	}
	if _, ok := backend.entries[primaryEntry]; !ok {
		t.Error("primary entry was deleted after legacy deletion failed")
	}
}

func TestKeyringStoreRepeatedDefaultDeleteRemainsOrderedAndIdempotent(t *testing.T) {
	legacyEntry := backendEntry{service: KeyringService, account: legacyUser}
	primaryEntry := backendEntry{service: KeyringService, account: entryName(DefaultAccount)}
	backend := newFakeKeyringBackend(map[backendEntry]string{
		legacyEntry:  "legacy",
		primaryEntry: "primary",
	})
	store := KeyringStore{backend: backend}

	for i := 0; i < 2; i++ {
		if err := store.Delete(context.Background(), DefaultAccount); err != nil {
			t.Fatalf("Delete() attempt %d error = %v", i+1, err)
		}
	}
	wantAccounts := []string{legacyUser, entryName(DefaultAccount), legacyUser, entryName(DefaultAccount)}
	if len(backend.calls) != len(wantAccounts) {
		t.Fatalf("backend calls = %d, want %d", len(backend.calls), len(wantAccounts))
	}
	for i, wantAccount := range wantAccounts {
		if backend.calls[i].operation != "delete" || backend.calls[i].entry.account != wantAccount {
			t.Errorf("call %d = %q/%q, want delete/%q", i, backend.calls[i].operation, backend.calls[i].entry.account, wantAccount)
		}
	}
}

func TestKeyringStoreIsolatesAccounts(t *testing.T) {
	backend := newFakeKeyringBackend(nil)
	store := KeyringStore{backend: backend}
	ctx := context.Background()
	work := Credentials{APIKey: "work-key", Token: "work-token"}
	personal := Credentials{APIKey: "personal-key", Token: "personal-token"}

	for name, creds := range map[string]Credentials{"work": work, "personal": personal} {
		if err := store.Save(ctx, name, creds); err != nil {
			t.Fatalf("Save(%s) error = %v", name, err)
		}
	}
	if err := store.Delete(ctx, "work"); err != nil {
		t.Fatalf("Delete(work) error = %v", err)
	}
	if got, err := store.Load(ctx, "work"); err != nil || got.Valid() {
		t.Error("deleted account still has credentials")
	}
	if got, err := store.Load(ctx, "personal"); err != nil || got != personal {
		t.Error("deleting one account disturbed another account")
	}
}

func TestKeyringStoreRejectsPartialCredentialsWithoutBackendAccess(t *testing.T) {
	tests := []struct {
		name     string
		creds    Credentials
		forLogin bool
	}{
		{name: "missing token", creds: Credentials{APIKey: "only-key"}},
		{name: "missing API key", creds: Credentials{Token: "only-token"}},
		{name: "both values missing", creds: Credentials{}},
		{name: "login missing token", creds: Credentials{APIKey: "only-key"}, forLogin: true},
		{name: "login missing API key", creds: Credentials{Token: "only-token"}, forLogin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(nil)
			store := KeyringStore{backend: backend}
			var err error
			if tt.forLogin {
				err = store.SaveForLogin(context.Background(), "work", tt.creds)
			} else {
				err = store.Save(context.Background(), "work", tt.creds)
			}
			if errx.ExitCode(err) != errx.CodeUsage {
				t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
			}
			if len(backend.calls) != 0 {
				t.Errorf("backend calls = %d, want 0", len(backend.calls))
			}
		})
	}
}

func TestKeyringStoreBackendFailuresUseAuthContractWithoutCredentials(t *testing.T) {
	backendFailure := errors.New("backend unavailable")
	apiKeySentinel := "api-key-must-not-appear"
	tokenSentinel := "token-must-not-appear"
	entry := backendEntry{service: KeyringService, account: entryName("work")}

	tests := []struct {
		name string
		run  func(*fakeKeyringBackend) error
	}{
		{
			name: "read failure",
			run: func(backend *fakeKeyringBackend) error {
				backend.getErrors[entry] = backendFailure
				_, err := (KeyringStore{backend: backend}).Load(context.Background(), "work")
				return err
			},
		},
		{
			name: "write failure",
			run: func(backend *fakeKeyringBackend) error {
				backend.setError = backendFailure
				return (KeyringStore{backend: backend}).Save(context.Background(), "work", Credentials{APIKey: apiKeySentinel, Token: tokenSentinel})
			},
		},
		{
			name: "login write failure",
			run: func(backend *fakeKeyringBackend) error {
				backend.setError = backendFailure
				return (KeyringStore{backend: backend}).SaveForLogin(context.Background(), "work", Credentials{APIKey: apiKeySentinel, Token: tokenSentinel})
			},
		},
		{
			name: "delete failure",
			run: func(backend *fakeKeyringBackend) error {
				backend.deleteErrors[entry] = backendFailure
				return (KeyringStore{backend: backend}).Delete(context.Background(), "work")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(nil)
			err := tt.run(backend)
			assertAuthError(t, err, "KEYRING_UNAVAILABLE")
			for _, forbidden := range []string{apiKeySentinel, tokenSentinel} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error disclosed a credential sentinel")
				}
			}
		})
	}
}

func TestKeyringStoreSaveForLoginFailuresPreserveTheExactAccount(t *testing.T) {
	for _, backendErr := range []error{
		errKeyringInteractionNotAllowed,
		errKeyringUserCanceled,
		errors.New("login backend unavailable"),
	} {
		backend := newFakeKeyringBackend(nil)
		backend.setError = backendErr

		err := (KeyringStore{backend: backend}).SaveForLogin(
			context.Background(),
			"work",
			Credentials{APIKey: "key", Token: "token"},
		)
		assertAuthError(t, err, "KEYRING_UNAVAILABLE")
		var typed *errx.Error
		if !errors.As(err, &typed) {
			t.Fatal("error is not an *errx.Error")
		}
		if want := "retry 'trello-cli auth login --account work'"; typed.Hint != want {
			t.Errorf("hint = %q, want %q", typed.Hint, want)
		}
	}
}

func TestKeyringStoreSaveForLoginPropagatesBackendContextCancellation(t *testing.T) {
	backend := newFakeKeyringBackend(nil)
	backend.setError = context.Canceled

	err := (KeyringStore{backend: backend}).SaveForLogin(
		context.Background(),
		"work",
		Credentials{APIKey: "key", Token: "token"},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestKeyringStoreProtectedOperationSignalsRequireAccountSpecificMigration(t *testing.T) {
	const (
		apiKeySentinel = "protected-operation-api-key-must-not-appear"
		tokenSentinel  = "protected-operation-token-must-not-appear"
	)
	entry := backendEntry{service: KeyringService, account: entryName("work")}
	tests := []struct {
		name       string
		backendErr error
		run        func(*fakeKeyringBackend) error
	}{
		{
			name:       "load interaction not allowed",
			backendErr: errKeyringInteractionNotAllowed,
			run: func(backend *fakeKeyringBackend) error {
				_, err := (KeyringStore{backend: backend}).Load(context.Background(), "work")
				return err
			},
		},
		{
			name:       "load user canceled native authorization",
			backendErr: errKeyringUserCanceled,
			run: func(backend *fakeKeyringBackend) error {
				_, err := (KeyringStore{backend: backend}).Load(context.Background(), "work")
				return err
			},
		},
		{
			name:       "save interaction not allowed",
			backendErr: errKeyringInteractionNotAllowed,
			run: func(backend *fakeKeyringBackend) error {
				return (KeyringStore{backend: backend}).Save(
					context.Background(),
					"work",
					Credentials{APIKey: apiKeySentinel, Token: tokenSentinel},
				)
			},
		},
		{
			name:       "save user canceled native authorization",
			backendErr: errKeyringUserCanceled,
			run: func(backend *fakeKeyringBackend) error {
				return (KeyringStore{backend: backend}).Save(
					context.Background(),
					"work",
					Credentials{APIKey: apiKeySentinel, Token: tokenSentinel},
				)
			},
		},
		{
			name:       "delete interaction not allowed",
			backendErr: errKeyringInteractionNotAllowed,
			run: func(backend *fakeKeyringBackend) error {
				return (KeyringStore{backend: backend}).Delete(context.Background(), "work")
			},
		},
		{
			name:       "delete user canceled native authorization",
			backendErr: errKeyringUserCanceled,
			run: func(backend *fakeKeyringBackend) error {
				return (KeyringStore{backend: backend}).Delete(context.Background(), "work")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(nil)
			backend.getErrors[entry] = tt.backendErr
			backend.setError = tt.backendErr
			backend.deleteErrors[entry] = tt.backendErr

			err := tt.run(backend)
			assertAuthError(t, err, "KEYRING_MIGRATION_REQUIRED")
			if !errors.Is(err, tt.backendErr) {
				t.Errorf("error = %v, want wrapped backend signal %v", err, tt.backendErr)
			}
			var typed *errx.Error
			if !errors.As(err, &typed) {
				t.Fatal("error is not an *errx.Error")
			}
			wantHint := "run 'trello-cli auth migrate-keychain --account work'"
			if typed.Hint != wantHint {
				t.Errorf("hint = %q, want %q", typed.Hint, wantHint)
			}
			for _, forbidden := range []string{apiKeySentinel, tokenSentinel} {
				if strings.Contains(err.Error()+typed.Hint, forbidden) {
					t.Errorf("protected-operation error disclosed credential sentinel %q", forbidden)
				}
			}
		})
	}
}

func TestKeyringStoreReportsRawNonJSONAsCorruptWithoutDisclosingIt(t *testing.T) {
	rawSentinel := "raw-credential-must-not-appear"
	entry := backendEntry{service: KeyringService, account: entryName("work")}
	backend := newFakeKeyringBackend(map[backendEntry]string{entry: rawSentinel})

	_, err := (KeyringStore{backend: backend}).Load(context.Background(), "work")
	assertAuthError(t, err, "KEYRING_CORRUPT")
	if strings.Contains(err.Error(), rawSentinel) {
		t.Error("corrupt-entry error disclosed the raw keychain value")
	}
}

func TestCancelledContextStopsKeychainAccess(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, KeyringStore) error
	}{
		{
			name: "load",
			run: func(ctx context.Context, store KeyringStore) error {
				_, err := store.Load(ctx, "work")
				return err
			},
		},
		{
			name: "save",
			run: func(ctx context.Context, store KeyringStore) error {
				return store.Save(ctx, "work", Credentials{APIKey: "key", Token: "token"})
			},
		},
		{
			name: "save for login",
			run: func(ctx context.Context, store KeyringStore) error {
				return store.SaveForLogin(ctx, "work", Credentials{APIKey: "key", Token: "token"})
			},
		},
		{
			name: "delete",
			run: func(ctx context.Context, store KeyringStore) error {
				return store.Delete(ctx, "work")
			},
		},
		{
			name: "migrate",
			run: func(ctx context.Context, store KeyringStore) error {
				return store.MigrateKeychain(ctx, "work")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeKeyringBackend(nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := tt.run(ctx, KeyringStore{backend: backend})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", err)
			}
			if len(backend.calls) != 0 {
				t.Errorf("backend calls = %d, want 0", len(backend.calls))
			}
		})
	}
}

func assertAuthError(t *testing.T, err error, wantReason string) {
	t.Helper()
	if errx.ExitCode(err) != errx.CodeAuth {
		t.Fatalf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeAuth)
	}
	var typed *errx.Error
	if !errors.As(err, &typed) {
		t.Fatal("error is not an *errx.Error")
	}
	if typed.Reason != wantReason {
		t.Errorf("reason = %q, want %q", typed.Reason, wantReason)
	}
}
