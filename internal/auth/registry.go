package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/abigotado/trello-cli/internal/lockfile"

	"github.com/abigotado/trello-cli/internal/errx"
)

const registryVersion = 1

// DefaultAccount is the name the legacy single-credential entry migrates to,
// and the name used when a caller logs in without choosing one.
const DefaultAccount = "default"

// Registry records which accounts exist and which is the stored default.
//
// It holds names only. Credentials stay in the OS keychain, so this file can
// never become the config file someone accidentally commits with a token in it.
// A keychain cannot be enumerated, which is the only reason this exists.
type Registry struct {
	path   string
	loaded bool
	data   registryFile
	// removed records names this invocation dropped, so merging with a
	// concurrent writer's copy does not bring them back.
	removed map[string]bool
}

type registryFile struct {
	Version  int      `json:"version"`
	Default  string   `json:"default,omitempty"`
	Accounts []string `json:"accounts"`
}

// NewRegistry builds a registry under the user config directory.
//
// A registry that cannot be located is inert rather than fatal, exactly like
// the resolver cache: losing the ability to enumerate accounts must not stop a
// caller who named one explicitly.
func NewRegistry() *Registry {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &Registry{}
	}
	return &Registry{path: filepath.Join(dir, "trello-cli", "accounts.json")}
}

// Path reports the on-disk location, or empty when the registry is inert.
func (r *Registry) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// List returns the known account names, sorted.
func (r *Registry) List() []string {
	if r == nil || r.path == "" {
		return nil
	}
	r.load()
	out := append([]string(nil), r.data.Accounts...)
	sort.Strings(out)
	return out
}

// Default returns the stored default account name, if any.
func (r *Registry) Default() string {
	if r == nil || r.path == "" {
		return ""
	}
	r.load()
	return r.data.Default
}

// Has reports whether name is registered.
func (r *Registry) Has(name string) bool {
	for _, a := range r.List() {
		if a == name {
			return true
		}
	}
	return false
}

// Add registers an account, and makes it the default when it is the first one.
func (r *Registry) Add(name string) error {
	if r == nil || r.path == "" {
		return nil
	}
	r.load()
	if !r.Has(name) {
		r.data.Accounts = append(r.data.Accounts, name)
	}
	// The first account becomes the default, so a single-account user never
	// has to think about accounts at all.
	if r.data.Default == "" {
		r.data.Default = name
	}
	return r.save()
}

// Remove deregisters an account and clears the default if it pointed there.
func (r *Registry) Remove(name string) error {
	if r == nil || r.path == "" {
		return nil
	}
	r.load()
	kept := r.data.Accounts[:0]
	for _, a := range r.data.Accounts {
		if a != name {
			kept = append(kept, a)
		}
	}
	r.data.Accounts = kept
	if r.removed == nil {
		r.removed = map[string]bool{}
	}
	r.removed[name] = true
	if r.data.Default == name {
		r.data.Default = ""
		// Falling back to the only remaining account keeps the tool usable
		// without a second command.
		if len(r.data.Accounts) == 1 {
			r.data.Default = r.data.Accounts[0]
		}
	}
	return r.save()
}

// SetDefault records which account is used when none is named.
func (r *Registry) SetDefault(name string) error {
	if r == nil || r.path == "" {
		return errx.Internal("no config directory is available to store a default account")
	}
	r.load()
	if !r.Has(name) {
		return errx.NotFound("account", name, candidateAccounts(r.List()))
	}
	r.data.Default = name
	return r.save()
}

func candidateAccounts(names []string) []errx.Candidate {
	out := make([]errx.Candidate, 0, len(names))
	for _, n := range names {
		out = append(out, errx.Candidate{ID: n, Name: n, Kind: "account"})
	}
	return out
}

// mergeFromDisk folds in any account another invocation added since this one
// loaded. Removals are this invocation's intent and are not undone.
func (r *Registry) mergeFromDisk() {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var onDisk registryFile
	if json.Unmarshal(raw, &onDisk) != nil || onDisk.Version != registryVersion {
		return
	}
	known := make(map[string]bool, len(r.data.Accounts))
	for _, a := range r.data.Accounts {
		known[a] = true
	}
	for _, a := range onDisk.Accounts {
		// removed holds names this invocation deliberately dropped, so a
		// concurrent writer's copy does not resurrect them.
		if !known[a] && !r.removed[a] {
			r.data.Accounts = append(r.data.Accounts, a)
		}
	}
	if r.data.Default == "" && onDisk.Default != "" && !r.removed[onDisk.Default] {
		r.data.Default = onDisk.Default
	}
}

func (r *Registry) load() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.data = registryFile{Version: registryVersion}

	raw, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var onDisk registryFile
	if json.Unmarshal(raw, &onDisk) != nil || onDisk.Version != registryVersion {
		return
	}
	r.data = onDisk
}

// save merges with whatever is on disk, then writes atomically.
//
// Atomic rename alone prevents a torn file, not a lost update: two logins that
// both load an empty registry would each write only their own account, and the
// second would erase the first. That is not a cache miss — the keychain entry
// survives while the name vanishes, so `auth list` hides it and the sole-account
// rung silently starts selecting a different workspace. Merging on write is
// what makes concurrent logins safe.
func (r *Registry) save() error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errx.Internal("create %s: %v", dir, err)
	}
	// Merge and write under one lock. Merging alone still loses an update,
	// because the merge is itself a read followed by a write.
	var writeErr error
	lockErr := lockfile.With(r.path, func() error {
		r.mergeFromDisk()
		r.data.Version = registryVersion
		writeErr = r.write(dir)
		return nil
	})
	if lockErr != nil {
		return errx.Internal("lock account registry: %v", lockErr)
	}
	return writeErr
}

func (r *Registry) write(dir string) error {
	raw, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		return errx.Internal("encode account registry: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".accounts-*.tmp")
	if err != nil {
		return errx.Internal("create temp file: %v", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return errx.Internal("write account registry: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return errx.Internal("close account registry: %v", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return errx.Internal("chmod account registry: %v", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return errx.Internal("replace account registry: %v", err)
	}
	return nil
}
