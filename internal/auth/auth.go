// Package auth resolves Trello credentials.
//
// Lookup order is environment first, OS keychain second. The order is load
// bearing: on macOS a freshly rebuilt, unsigned binary triggers a modal
// keychain-access prompt, and an agent shelling out to it would hang on a
// dialog it cannot see. The environment path must therefore be reachable
// without touching the keychain at all.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/abigotado-niko/trello-cli/internal/errx"
	"github.com/zalando/go-keyring"
)

const (
	// KeyringService is the keychain service name.
	KeyringService = "trello-cli"
	// keyringUser stores both values as one JSON blob, so a partial write
	// cannot leave a key without its token.
	keyringUser = "credentials"

	// EnvAPIKey and EnvToken are the environment overrides.
	EnvAPIKey = "TRELLO_API_KEY"
	EnvToken  = "TRELLO_TOKEN"
)

// Credentials is a Trello API key and token.
//
// It has no String or Format method on purpose: adding one that returned the
// token would leak it into every %v in the codebase. Use [Credentials.Fingerprint].
type Credentials struct {
	APIKey string `json:"api_key"`
	Token  string `json:"token"`
}

// Valid reports whether both halves are present.
func (c Credentials) Valid() bool { return c.APIKey != "" && c.Token != "" }

// Fingerprint returns a short, non-reversible identifier safe to print.
//
// It is what `auth status` shows. It never contains the token itself.
func (c Credentials) Fingerprint() string {
	if c.Token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.Token))
	return hex.EncodeToString(sum[:])[:12]
}

// Source names where credentials came from, for `auth status`.
type Source string

const (
	// SourceEnv means the environment supplied them.
	SourceEnv Source = "env"
	// SourceKeyring means the OS keychain supplied them.
	SourceKeyring Source = "keyring"
	// SourceNone means none were found.
	SourceNone Source = "none"
)

// Store persists credentials. It is an interface so tests can substitute a
// fake and never touch the real OS keychain.
type Store interface {
	Load(ctx context.Context) (Credentials, error)
	Save(ctx context.Context, creds Credentials) error
	Delete(ctx context.Context) error
}

// KeyringStore is the OS keychain implementation of [Store].
type KeyringStore struct{}

// Load reads credentials from the keychain. A missing entry is not an error:
// it returns the zero Credentials so the caller can fall through.
func (KeyringStore) Load(ctx context.Context) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	raw, err := keyring.Get(KeyringService, keyringUser)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Credentials{}, nil
		}
		return Credentials{}, errx.Auth("KEYRING_UNAVAILABLE", "read keychain: %v", err).Wrap(err)
	}
	var creds Credentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return Credentials{}, errx.Auth("KEYRING_CORRUPT", "stored credentials are unreadable").Wrap(err)
	}
	return creds, nil
}

// Save writes credentials to the keychain as a single atomic entry.
func (KeyringStore) Save(ctx context.Context, creds Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !creds.Valid() {
		return errx.Usage("both an API key and a token are required")
	}
	blob, err := json.Marshal(creds)
	if err != nil {
		return errx.Internal("encode credentials: %v", err)
	}
	if err := keyring.Set(KeyringService, keyringUser, string(blob)); err != nil {
		return errx.Auth("KEYRING_UNAVAILABLE", "write keychain: %v", err).Wrap(err)
	}
	return nil
}

// Delete removes the keychain entry. Deleting a missing entry succeeds.
func (KeyringStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := keyring.Delete(KeyringService, keyringUser); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return errx.Auth("KEYRING_UNAVAILABLE", "delete from keychain: %v", err).Wrap(err)
	}
	return nil
}

// Resolver finds credentials, preferring the environment over the keychain.
type Resolver struct {
	// Lookup is normally os.LookupEnv. Tests supply their own.
	Lookup func(string) (string, bool)
	// Store is consulted only when the environment does not supply both
	// values, so a headless caller never triggers a keychain prompt.
	Store Store
}

// Resolve returns credentials and where they came from.
func (r Resolver) Resolve(ctx context.Context) (Credentials, Source, error) {
	if r.Lookup != nil {
		key, hasKey := r.Lookup(EnvAPIKey)
		token, hasToken := r.Lookup(EnvToken)
		if hasKey && hasToken && key != "" && token != "" {
			return Credentials{APIKey: key, Token: token}, SourceEnv, nil
		}
		// A half-configured environment is almost always a typo in a shell
		// profile or CI secret. Falling through to the keychain would use a
		// different account than the one the caller just tried to select, so
		// this fails loudly instead.
		if (hasKey && key != "") != (hasToken && token != "") {
			return Credentials{}, SourceNone, errx.Auth(
				"PARTIAL_ENV_CREDENTIALS",
				"only one of %s and %s is set; set both or neither",
				EnvAPIKey, EnvToken,
			)
		}
	}
	if r.Store == nil {
		return Credentials{}, SourceNone, notConfigured()
	}
	creds, err := r.Store.Load(ctx)
	if err != nil {
		return Credentials{}, SourceNone, err
	}
	if !creds.Valid() {
		return Credentials{}, SourceNone, notConfigured()
	}
	return creds, SourceKeyring, nil
}

func notConfigured() error {
	return errx.Auth("NOT_AUTHENTICATED", "no Trello credentials found")
}
