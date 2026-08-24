// Package auth resolves Trello credentials, across any number of accounts.
//
// Two rules shape everything here.
//
// Lookup prefers the environment over the OS keychain. The macOS backend
// refuses authentication UI and reports a typed failure when Keychain access
// would require it, so the environment path must remain reachable without
// touching the keychain at all for headless and CI callers.
//
// Account selection is per invocation and never stateful. There is deliberately
// no "switch accounts" command: an active-account setting is hidden global
// state, two concurrent invocations would race over it, and the loser would
// silently act on the wrong board — the exact failure this tool is built to
// prevent everywhere else.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/abigotado/trello-cli/internal/errx"
)

const (
	// KeyringService is the keychain service name.
	KeyringService = "trello-cli"
	// accountPrefix namespaces one keychain entry per account.
	accountPrefix = "account:"
	// legacyUser is the single-account entry written before accounts existed.
	// It is read as [DefaultAccount] so an existing login keeps working.
	legacyUser = "credentials"

	// EnvAPIKey and EnvToken supply credentials directly.
	EnvAPIKey = "TRELLO_API_KEY"
	EnvToken  = "TRELLO_TOKEN"
	// EnvAccount names a stored account to use.
	EnvAccount = "TRELLO_CLI_ACCOUNT"
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
	// SourceEnv means the environment supplied them directly.
	SourceEnv Source = "env"
	// SourceKeyring means the OS keychain supplied them.
	SourceKeyring Source = "keyring"
	// SourceNone means none were found.
	SourceNone Source = "none"
)

// ValidateAccountName rejects names that would be ambiguous on a command line
// or unusable as a keychain key.
func ValidateAccountName(name string) error {
	if name == "" {
		return errx.Usage("an account name is required")
	}
	if strings.TrimSpace(name) != name {
		return errx.Usage("account name %q has leading or trailing whitespace", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return errx.Usage("account name %q may use only letters, digits, dash, underscore, and dot", name)
		}
	}
	return nil
}

// Store persists credentials per account. It is an interface so tests can
// substitute a fake and never touch the real OS keychain.
type Store interface {
	Load(ctx context.Context, account string) (Credentials, error)
	Save(ctx context.Context, account string, creds Credentials) error
	Delete(ctx context.Context, account string) error
}

// LoginStore extends Store with the explicit capability to migrate an
// existing credential's access policy during an explicit auth login.
type LoginStore interface {
	Store
	SaveForLogin(ctx context.Context, account string, creds Credentials) error
}

// KeyringStore is the OS keychain implementation of [Store]. Its zero value
// selects the backend for the current platform.
type KeyringStore struct {
	backend keyringBackend
}

type keyringBackend interface {
	get(ctx context.Context, service, account string) (string, error)
	set(ctx context.Context, service, account, value string) error
	setForLogin(ctx context.Context, service, primaryAccount, legacyAccount, value string) error
	delete(ctx context.Context, service, account string) error
}

var errKeyringNotFound = errors.New("keyring item not found")

func (s KeyringStore) selectedBackend() keyringBackend {
	if s.backend != nil {
		return s.backend
	}
	return platformKeyringBackend()
}

func entryName(account string) string { return accountPrefix + account }

// Load reads one account's credentials. A missing entry is not an error: it
// returns the zero Credentials so the caller can fall through.
func (s KeyringStore) Load(ctx context.Context, account string) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	backend := s.selectedBackend()
	raw, err := backend.get(ctx, KeyringService, entryName(account))
	if errors.Is(err, errKeyringNotFound) && account == DefaultAccount {
		// Fall back to the pre-accounts entry so its service/account schema
		// remains supported. On macOS, auth login may first need to migrate
		// that legacy item's creator-scoped access policy.
		raw, err = backend.get(ctx, KeyringService, legacyUser)
	}
	if err != nil {
		if errors.Is(err, errKeyringNotFound) {
			return Credentials{}, nil
		}
		return Credentials{}, errx.Auth("KEYRING_UNAVAILABLE", "read keychain: %v", err).Wrap(err)
	}
	var creds Credentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return Credentials{}, errx.Auth("KEYRING_CORRUPT", "stored credentials for account %q are unreadable", account).Wrap(err)
	}
	return creds, nil
}

// Save writes one account's credentials as a single atomic entry.
func (s KeyringStore) Save(ctx context.Context, account string, creds Credentials) error {
	return s.save(ctx, account, creds, false)
}

// SaveForLogin writes credentials and, on macOS, permits the interactive ACL
// migration required to keep an existing item stable across binary rebuilds.
func (s KeyringStore) SaveForLogin(ctx context.Context, account string, creds Credentials) error {
	return s.save(ctx, account, creds, true)
}

func (s KeyringStore) save(ctx context.Context, account string, creds Credentials, forLogin bool) error {
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
	backend := s.selectedBackend()
	if !forLogin {
		err = backend.set(ctx, KeyringService, entryName(account), string(blob))
	} else {
		legacyAccount := ""
		if account == DefaultAccount {
			legacyAccount = legacyUser
		}
		err = backend.setForLogin(ctx, KeyringService, entryName(account), legacyAccount, string(blob))
	}
	if err != nil {
		return errx.Auth("KEYRING_UNAVAILABLE", "write keychain: %v", err).Wrap(err)
	}
	return nil
}

// Delete removes one account's entry. Deleting a missing entry succeeds.
func (s KeyringStore) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	backend := s.selectedBackend()
	if account == DefaultAccount {
		err := backend.delete(ctx, KeyringService, legacyUser)
		if err != nil && !errors.Is(err, errKeyringNotFound) {
			return errx.Auth("KEYRING_UNAVAILABLE", "delete from keychain: %v", err).Wrap(err)
		}
	}
	err := backend.delete(ctx, KeyringService, entryName(account))
	if err != nil && !errors.Is(err, errKeyringNotFound) {
		return errx.Auth("KEYRING_UNAVAILABLE", "delete from keychain: %v", err).Wrap(err)
	}
	return nil
}

// Resolver finds the credentials one invocation should use.
type Resolver struct {
	// Lookup is normally os.LookupEnv. Tests supply their own.
	Lookup func(string) (string, bool)
	// Store is consulted only when the environment does not supply both raw
	// values, so a headless caller can bypass Keychain availability entirely.
	Store Store
	// Registry supplies the stored default and the known account names.
	Registry *Registry
	// Account is the explicit --account selection, if any.
	Account string
}

// Resolution is the outcome of resolving credentials.
type Resolution struct {
	Credentials Credentials
	Source      Source
	// Account is the name the credentials came from, empty for the raw
	// environment pair, which belongs to no named account.
	Account string
}

// Resolve returns the credentials to use, and where they came from.
//
// Precedence, from most to least specific. The rule is that explicit beats
// implicit and per-invocation beats stored:
//
//  1. --account NAME
//  2. TRELLO_API_KEY plus TRELLO_TOKEN
//  3. TRELLO_CLI_ACCOUNT
//  4. the stored default account
//  5. the only account, when exactly one exists
func (r Resolver) Resolve(ctx context.Context) (Resolution, error) {
	if r.Account != "" {
		if err := ValidateAccountName(r.Account); err != nil {
			return Resolution{}, err
		}
		return r.fromStore(ctx, r.Account, true)
	}

	if creds, err := r.fromEnv(); err != nil {
		return Resolution{}, err
	} else if creds.Valid() {
		return Resolution{Credentials: creds, Source: SourceEnv}, nil
	}

	if r.Lookup != nil {
		if name, ok := r.Lookup(EnvAccount); ok && name != "" {
			if err := ValidateAccountName(name); err != nil {
				return Resolution{}, err
			}
			return r.fromStore(ctx, name, true)
		}
	}

	if name := r.Registry.Default(); name != "" {
		return r.fromStore(ctx, name, false)
	}
	if names := r.Registry.List(); len(names) == 1 {
		return r.fromStore(ctx, names[0], false)
	}
	// Nothing named anything: try the implicit default, which is also where a
	// pre-accounts login is found.
	return r.fromStore(ctx, DefaultAccount, false)
}

// fromEnv reads the raw credential pair.
func (r Resolver) fromEnv() (Credentials, error) {
	if r.Lookup == nil {
		return Credentials{}, nil
	}
	key, hasKey := r.Lookup(EnvAPIKey)
	token, hasToken := r.Lookup(EnvToken)
	haveKey := hasKey && key != ""
	haveToken := hasToken && token != ""

	if haveKey && haveToken {
		return Credentials{APIKey: key, Token: token}, nil
	}
	// A half-configured environment is almost always a typo in a shell profile
	// or a CI secret. Falling through would use a different account than the
	// one the caller just tried to select.
	if haveKey != haveToken {
		return Credentials{}, errx.Auth(
			"PARTIAL_ENV_CREDENTIALS",
			"only one of %s and %s is set; set both or neither",
			EnvAPIKey, EnvToken,
		)
	}
	return Credentials{}, nil
}

// fromStore loads a named account. explicit reports whether the caller named
// it, which decides how a miss is reported.
func (r Resolver) fromStore(ctx context.Context, account string, explicit bool) (Resolution, error) {
	if r.Store == nil {
		return Resolution{}, notConfigured(r.Registry)
	}
	creds, err := r.Store.Load(ctx, account)
	if err != nil {
		return Resolution{}, err
	}
	if !creds.Valid() {
		if explicit {
			// The caller named this account, so say that it specifically is
			// missing rather than reporting a generic "not authenticated".
			return Resolution{}, &errx.Error{
				// Auth, not NotFound. The caller's recovery is always "run
				// auth login", which is code 5's stated action; the sibling
				// notConfigured() path returns 5 for the same condition
				// reached implicitly, and two codes for one recovery makes an
				// agent special-case an implementation detail.
				Code:       errx.CodeAuth,
				Reason:     "UNKNOWN_ACCOUNT",
				Message:    "no credentials are stored for account " + account,
				Hint:       "run 'trello-cli auth login --account " + account + "', or 'trello-cli auth list' to see the accounts you have",
				DidYouMean: candidateAccounts(r.Registry.List()),
			}
		}
		return Resolution{}, notConfigured(r.Registry)
	}
	return Resolution{Credentials: creds, Source: SourceKeyring, Account: account}, nil
}

func notConfigured(registry *Registry) error {
	err := errx.Auth("NOT_AUTHENTICATED", "no Trello credentials found")
	if names := registry.List(); len(names) > 1 {
		err.Hint = "several accounts are stored; pick one with --account, or set a default with 'trello-cli auth default'"
		err.DidYouMean = candidateAccounts(names)
	}
	return err
}
