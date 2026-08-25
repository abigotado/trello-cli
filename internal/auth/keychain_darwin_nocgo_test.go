//go:build darwin && !cgo

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abigotado/trello-cli/internal/errx"
)

func TestNoCGOResolverEnvironmentBypassesUnsupportedStore(t *testing.T) {
	const (
		apiKeySentinel = "nocgo-env-key-sentinel"
		tokenSentinel  = "nocgo-env-token-sentinel"
	)
	resolver := Resolver{
		Lookup: env(map[string]string{
			EnvAPIKey: apiKeySentinel,
			EnvToken:  tokenSentinel,
		}),
		Store:    KeyringStore{},
		Registry: NewRegistry(),
	}

	got, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Source != SourceEnv || got.Credentials.APIKey != apiKeySentinel || got.Credentials.Token != tokenSentinel {
		t.Error("Resolve() did not return the complete environment credential pair")
	}
}

func TestNoCGOStoredOperationsReturnTypedUnavailable(t *testing.T) {
	const (
		apiKeySentinel = "nocgo-store-key-sentinel"
		tokenSentinel  = "nocgo-store-token-sentinel"
	)
	tests := []struct {
		name string
		run  func(KeyringStore) error
	}{
		{
			name: "load",
			run: func(store KeyringStore) error {
				_, err := store.Load(context.Background(), "work")
				return err
			},
		},
		{
			name: "save",
			run: func(store KeyringStore) error {
				return store.Save(context.Background(), "work", Credentials{APIKey: apiKeySentinel, Token: tokenSentinel})
			},
		},
		{
			name: "save for login",
			run: func(store KeyringStore) error {
				return store.SaveForLogin(context.Background(), "work", Credentials{APIKey: apiKeySentinel, Token: tokenSentinel})
			},
		},
		{
			name: "delete",
			run: func(store KeyringStore) error {
				return store.Delete(context.Background(), "work")
			},
		},
		{
			name: "migrate",
			run: func(store KeyringStore) error {
				return store.MigrateKeychain(context.Background(), "work")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(KeyringStore{})
			wantReason := "KEYRING_UNAVAILABLE"
			wantHint := "set TRELLO_API_KEY and TRELLO_TOKEN, or use a macOS build with cgo enabled"
			if tt.name == "migrate" {
				wantReason = "KEYRING_MIGRATION_UNAVAILABLE"
				wantHint = "use a macOS build with cgo enabled, or set TRELLO_API_KEY and TRELLO_TOKEN"
			}
			assertAuthError(t, err, wantReason)
			var typed *errx.Error
			if !errors.As(err, &typed) {
				t.Fatal("error is not an *errx.Error")
			}
			if typed.Hint != wantHint {
				t.Errorf("hint = %q, want %q", typed.Hint, wantHint)
			}
			for _, forbidden := range []string{apiKeySentinel, tokenSentinel} {
				if strings.Contains(err.Error(), forbidden) {
					t.Error("stored-operation error disclosed a credential sentinel")
				}
			}
		})
	}
}
