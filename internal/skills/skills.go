// Package skills installs the shipped agent skill into the directories the
// Claude Code, Codex, and Cursor CLIs read.
//
// It imports only errx, lockfile, the embedded assets, and stdlib. Nothing
// here needs a client, a credential, or a writer, and keeping it that way is
// what lets the whole package be tested against t.TempDir() with no network
// and no keychain.
package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abigotado/trello-cli/assets"
	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/lockfile"
)

// SkillName is the directory the skill installs into, under each provider's
// skills root.
const SkillName = "trello"

// manifestName records what this tool put in the skill directory. It lives
// inside that directory so ownership travels with the thing it describes.
const manifestName = ".trello-cli-install.json"

// Provider is a tool that reads installed skills.
type Provider string

// Supported providers.
const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
	ProviderCursor Provider = "cursor"
)

// Providers returns every supported target, in a stable order.
func Providers() []Provider {
	return []Provider{ProviderClaude, ProviderCodex, ProviderCursor}
}

// ParseProvider validates a --provider value.
func ParseProvider(s string) (Provider, error) {
	for _, p := range Providers() {
		if string(p) == s {
			return p, nil
		}
	}
	names := make([]string, 0, len(Providers()))
	for _, p := range Providers() {
		names = append(names, string(p))
	}
	return "", errx.Usage("unknown provider %q: want %s, or all", s, strings.Join(names, ", "))
}

// Scope selects a user-wide or project-local install.
type Scope string

// Supported scopes.
const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// ParseScope validates a --scope value.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case ScopeUser, ScopeProject:
		return Scope(s), nil
	default:
		return "", errx.Usage("unknown scope %q: want user or project", s)
	}
}

// Options describes one install or uninstall.
type Options struct {
	Provider   Provider
	Scope      Scope
	ProjectDir string // used when Scope is ScopeProject
	Dest       string // explicit install root, overriding scope derivation
	Confirmed  bool   // --yes
	DryRun     bool   // --dry-run

	// Injected for tests. nil means the real environment.
	HomeDir   func() (string, error)
	LookupEnv func(string) (string, bool)
}

func (o Options) homeDir() (string, error) {
	f := o.HomeDir
	if f == nil {
		f = os.UserHomeDir
	}
	home, err := f()
	if err != nil || home == "" {
		return "", &errx.Error{
			Code:    errx.CodeUsage,
			Reason:  "NO_HOME_DIR",
			Message: "cannot determine a home directory",
			Hint:    "set HOME, or pass --dest with an explicit directory",
		}
	}
	return home, nil
}

func (o Options) lookupEnv(key string) (string, bool) {
	if o.LookupEnv == nil {
		return os.LookupEnv(key)
	}
	return o.LookupEnv(key)
}

// Status describes one file's relationship to the embedded payload.
type Status string

// File statuses.
const (
	StatusAbsent   Status = "absent"   // not on disk
	StatusCurrent  Status = "current"  // ours, matches the payload
	StatusStale    Status = "stale"    // ours, unedited, payload changed
	StatusModified Status = "modified" // ours, edited since we wrote it
	StatusForeign  Status = "foreign"  // present, never installed by us
	StatusOrphan   Status = "orphan"   // ours, no longer in the payload
)

// FileResult is one file's outcome.
type FileResult struct {
	Path    string `json:"path"`
	Status  Status `json:"status"`
	Applied bool   `json:"applied"`
}

// Result is the outcome of an install or uninstall.
type Result struct {
	Provider string       `json:"provider"`
	Scope    string       `json:"scope"`
	Root     string       `json:"root"`
	Skill    string       `json:"skill"`
	InSync   bool         `json:"inSync"`
	DryRun   bool         `json:"dryRun"`
	Files    []FileResult `json:"files"`
}

// manifest records what was installed and what it hashed to.
type manifest struct {
	Version  int            `json:"version"`
	Skill    string         `json:"skill"`
	Provider string         `json:"provider"`
	Complete bool           `json:"complete"`
	Files    []manifestFile `json:"files"`
}

type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (m manifest) hashes() map[string]string {
	out := make(map[string]string, len(m.Files))
	for _, f := range m.Files {
		out[f.Path] = f.SHA256
	}
	return out
}

// Root reports where opts would install.
func Root(opts Options) (string, error) {
	if opts.Dest != "" {
		return resolveDest(opts.Dest)
	}
	base, err := providerBase(opts)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "skills"), nil
}

// providerBase returns the provider's config directory for the chosen scope.
func providerBase(opts Options) (string, error) {
	dir := "." + string(opts.Provider)

	if opts.Scope == ScopeProject {
		if opts.ProjectDir == "" {
			return "", errx.Usage("--scope project needs a project directory")
		}
		abs, err := filepath.Abs(opts.ProjectDir)
		if err != nil {
			return "", errx.Internal("resolve project directory: %v", err)
		}
		if err := refuseHarnessOwned(abs, opts.Provider); err != nil {
			return "", err
		}
		return filepath.Join(abs, dir), nil
	}

	// Codex honours CODEX_HOME for its whole config tree.
	if opts.Provider == ProviderCodex {
		if v, ok := opts.lookupEnv("CODEX_HOME"); ok && v != "" {
			return v, nil
		}
	}
	home, err := opts.homeDir()
	if err != nil {
		return "", err
	}
	// os.UserHomeDir, deliberately not os.UserConfigDir, even though
	// internal/auth/registry.go uses the latter. On macOS UserConfigDir
	// returns ~/Library/Application Support, so following that precedent would
	// install to ~/Library/Application Support/.claude/skills: the command
	// reports success, no agent ever loads it, and nothing errors.
	return filepath.Join(home, dir), nil
}

// refuseHarnessOwned blocks a project install into a directory the agent
// harness compiler owns.
//
// In a repository with .agents/, the compiler writes .claude/ and .codex/
// wholesale and prunes what it does not recognise. Installing there is either
// overwritten on the next compile or pruned — an unbounded loop with no error
// at any step. Cursor is exempt: the compiler has no cursor adapter.
func refuseHarnessOwned(projectDir string, provider Provider) error {
	if provider == ProviderCursor {
		return nil
	}
	for _, marker := range []string{"rules", "skills"} {
		if info, err := os.Stat(filepath.Join(projectDir, ".agents", marker)); err == nil && info.IsDir() {
			return &errx.Error{
				Code:   errx.CodeUsage,
				Reason: "HARNESS_OWNED_DIRECTORY",
				Message: fmt.Sprintf(
					"%s has an .agents/ harness, which owns .%s/ and would overwrite or prune anything installed there",
					projectDir, provider),
				Hint: "add the skill under .agents/skills/ and recompile the harness, or install with --scope user",
			}
		}
	}
	return nil
}

// resolveDest validates an explicit --dest.
func resolveDest(dest string) (string, error) {
	// Go does not expand ~, so a quoted --dest '~/x' would create a literal
	// ./~/x tree the user never finds.
	if dest == "~" || strings.HasPrefix(dest, "~/") {
		return "", errx.Usage("--dest %q is not expanded by this tool; pass an absolute path", dest)
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", errx.Internal("resolve --dest: %v", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", &errx.Error{
			Code:    errx.CodeUsage,
			Reason:  "DEST_NOT_A_DIRECTORY",
			Message: fmt.Sprintf("--dest %s does not exist", abs),
			Hint:    "create the directory first, or omit --dest to use the provider's own location",
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", symlinkError(abs)
	}
	if !info.IsDir() {
		return "", &errx.Error{
			Code:    errx.CodeUsage,
			Reason:  "DEST_NOT_A_DIRECTORY",
			Message: fmt.Sprintf("--dest %s is not a directory", abs),
			Hint:    "pass a directory",
		}
	}
	return abs, nil
}

func symlinkError(p string) error {
	return &errx.Error{
		Code:    errx.CodeUsage,
		Reason:  "DEST_IS_SYMLINK",
		Message: fmt.Sprintf("%s is a symlink", p),
		Hint:    "install to a real directory; this tool will not write through a link it did not create",
	}
}

// payload returns the embedded files for a provider, keyed by slash-separated
// relative path.
func payload(provider Provider) (map[string][]byte, error) {
	root := path.Join("skills", SkillName)
	out := map[string][]byte{}

	err := fs.WalkDir(assets.FS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// agents/openai.yaml is Codex UI metadata and is meaningless noise in
		// a Claude or Cursor package.
		if provider != ProviderCodex && strings.HasPrefix(rel, "agents/") {
			return nil
		}
		data, readErr := assets.FS.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		out[rel] = data
		return nil
	})
	if err != nil {
		return nil, errx.Internal("read embedded skill: %v", err)
	}
	if len(out) == 0 {
		return nil, errx.Internal("the embedded skill payload is empty")
	}
	return out, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Install makes the destination match the embedded payload.
func Install(ctx context.Context, opts Options) (Result, error) {
	root, err := Root(opts)
	if err != nil {
		return Result{}, err
	}
	skillDir := filepath.Join(root, SkillName)

	// No computed destination may sit under .cursor/rules: sync-rules.py
	// enforces an exact flat inventory there and unlinks what it does not
	// expect, so an install would delete itself on the next sync.
	if containsComponent(skillDir, filepath.Join(".cursor", "rules")) {
		return Result{}, errx.Internal("refusing to install into a .cursor/rules path: %s", skillDir)
	}

	want, err := payload(opts.Provider)
	if err != nil {
		return Result{}, err
	}
	if err := assertNoSymlink(root, skillDir); err != nil {
		return Result{}, err
	}

	res := Result{
		Provider: string(opts.Provider),
		Scope:    string(opts.Scope),
		Root:     root,
		Skill:    SkillName,
		DryRun:   opts.DryRun,
	}

	plan := func() ([]FileResult, error) { return classify(skillDir, want) }

	// A dry run is a pure read: no directory, no lock file, no manifest.
	// Building the plan under the lock would create both.
	if opts.DryRun {
		files, planErr := plan()
		if planErr != nil {
			return Result{}, planErr
		}
		if err := refuseUnmanaged(files, opts.Confirmed); err != nil {
			return Result{}, err
		}
		res.Files = files
		res.InSync = inSync(files, false)
		return res, nil
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return Result{}, translateFS("create", skillDir, err)
	}

	var applyErr error
	lockErr := lockfile.With(filepath.Join(skillDir, manifestName), func() error {
		files, planErr := plan()
		if planErr != nil {
			applyErr = planErr
			return nil
		}
		if err := refuseUnmanaged(files, opts.Confirmed); err != nil {
			applyErr = err
			return nil
		}
		applyErr = apply(ctx, skillDir, want, files, opts.Provider)
		res.Files = files
		return nil
	})
	if lockErr != nil {
		return Result{}, errx.Internal("lock the skill directory: %v", lockErr)
	}
	if applyErr != nil {
		return Result{}, applyErr
	}
	res.InSync = inSync(res.Files, true)
	return res, nil
}

// classify decides what must happen to every file, before anything is written.
func classify(skillDir string, want map[string][]byte) ([]FileResult, error) {
	recorded := readManifest(skillDir).hashes()
	seen := map[string]bool{}
	var out []FileResult

	for rel, data := range want {
		seen[rel] = true
		target := filepath.Join(skillDir, filepath.FromSlash(rel))
		status, err := classifyOne(target, recorded[rel], sum(data), recorded != nil && hasKey(recorded, rel))
		if err != nil {
			return nil, err
		}
		out = append(out, FileResult{Path: target, Status: status})
	}

	// Anything we recorded but no longer ship.
	for rel, recordedHash := range recorded {
		if seen[rel] {
			continue
		}
		target := filepath.Join(skillDir, filepath.FromSlash(rel))
		onDisk, err := os.ReadFile(target)
		if err != nil {
			continue // already gone
		}
		status := StatusOrphan
		if sum(onDisk) != recordedHash {
			// Edited since we wrote it; not ours to remove.
			status = StatusModified
		}
		out = append(out, FileResult{Path: target, Status: status})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

func classifyOne(target, recordedHash, wantHash string, wasRecorded bool) (Status, error) {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return StatusAbsent, nil
	}
	if err != nil {
		return "", translateFS("inspect", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", symlinkError(target)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		return "", translateFS("read", target, err)
	}
	diskHash := sum(onDisk)

	if !wasRecorded {
		return StatusForeign, nil
	}
	if diskHash != recordedHash {
		return StatusModified, nil
	}
	if diskHash != wantHash {
		return StatusStale, nil
	}
	return StatusCurrent, nil
}

// refuseUnmanaged stops before writing when the destination holds a file this
// tool did not write, or one the user has since edited.
func refuseUnmanaged(files []FileResult, confirmed bool) error {
	if confirmed {
		return nil
	}
	var blocked []string
	for _, f := range files {
		if f.Status == StatusModified || f.Status == StatusForeign {
			blocked = append(blocked, f.Path)
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	return &errx.Error{
		Code:   errx.CodeConfirm,
		Reason: "UNMANAGED_FILES",
		Message: fmt.Sprintf("%d file(s) in the destination were not written by this tool, or were edited since: %s",
			len(blocked), strings.Join(blocked, ", ")),
		Hint: "re-run with --yes to overwrite them, or with --dry-run to see what would change",
	}
}

// apply writes what the plan says to write.
//
// Every write is unconditional when the hash differs and is never skipped
// because the directory exists, so a torn install self-heals on the next run.
// Per-file temp-and-rename prevents a torn file; nothing prevents a torn set.
func apply(ctx context.Context, skillDir string, want map[string][]byte, files []FileResult, provider Provider) error {
	// Mark the manifest incomplete first, so an interrupted run is visible.
	if err := writeManifest(skillDir, manifest{
		Version: 1, Skill: SkillName, Provider: string(provider), Complete: false,
	}); err != nil {
		return err
	}

	for i := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := &files[i]
		rel, err := filepath.Rel(skillDir, f.Path)
		if err != nil {
			return errx.Internal("resolve %s: %v", f.Path, err)
		}
		data, shipped := want[filepath.ToSlash(rel)]

		switch f.Status {
		case StatusCurrent:
			continue
		case StatusOrphan:
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				return translateFS("remove", f.Path, err)
			}
			f.Applied = true
			continue
		case StatusModified, StatusForeign:
			if !shipped {
				// Not ours and not shipped: leave it alone entirely.
				continue
			}
		}
		if !shipped {
			continue
		}
		if err := writeFile(f.Path, data); err != nil {
			return err
		}
		f.Applied = true
	}

	// Record only what is actually shipped now.
	record := make([]manifestFile, 0, len(want))
	for rel, data := range want {
		record = append(record, manifestFile{Path: rel, SHA256: sum(data)})
	}
	sort.Slice(record, func(i, j int) bool { return record[i].Path < record[j].Path })
	return writeManifest(skillDir, manifest{
		Version: 1, Skill: SkillName, Provider: string(provider), Complete: true, Files: record,
	})
}

// writeFile writes one payload file atomically, refusing to follow a symlink.
func writeFile(target string, data []byte) error {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// Checked immediately before the write, not only during planning:
		// os.WriteFile follows a symlinked file and overwrites whatever it
		// points at, and os.Rename replaces the link and orphans the target.
		return symlinkError(target)
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return translateFS("create", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".trello-skill-*.tmp")
	if err != nil {
		return translateFS("create a temporary file in", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return translateFS("write", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return translateFS("close", tmpName, err)
	}
	// embed.FS reports every file as 0444, so the payload's own modes carry no
	// information. Everything ships non-executable by construction.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return translateFS("chmod", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return translateFS("replace", target, err)
	}
	return nil
}

func readManifest(skillDir string) manifest {
	raw, err := os.ReadFile(filepath.Join(skillDir, manifestName))
	if err != nil {
		return manifest{}
	}
	var m manifest
	if json.Unmarshal(raw, &m) != nil || m.Version != 1 {
		return manifest{}
	}
	return m
}

func writeManifest(skillDir string, m manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return errx.Internal("encode the install manifest: %v", err)
	}
	return writeFile(filepath.Join(skillDir, manifestName), raw)
}

// Uninstall removes only the files this tool installed and still owns.
func Uninstall(ctx context.Context, opts Options) (Result, error) {
	root, err := Root(opts)
	if err != nil {
		return Result{}, err
	}
	skillDir := filepath.Join(root, SkillName)
	res := Result{
		Provider: string(opts.Provider),
		Scope:    string(opts.Scope),
		Root:     root,
		Skill:    SkillName,
		DryRun:   opts.DryRun,
	}

	recorded := readManifest(skillDir).hashes()
	var files []FileResult
	for rel, hash := range recorded {
		target := filepath.Join(skillDir, filepath.FromSlash(rel))
		onDisk, readErr := os.ReadFile(target)
		if readErr != nil {
			files = append(files, FileResult{Path: target, Status: StatusAbsent})
			continue
		}
		if sum(onDisk) != hash {
			// Edited since we wrote it, so it is no longer ours to remove.
			files = append(files, FileResult{Path: target, Status: StatusModified})
			continue
		}
		if !opts.DryRun {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return Result{}, translateFS("remove", target, err)
			}
		}
		files = append(files, FileResult{Path: target, Status: StatusOrphan, Applied: !opts.DryRun})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	if !opts.DryRun {
		_ = os.Remove(filepath.Join(skillDir, manifestName))
		// Only if nothing the user owns is left behind.
		_ = os.Remove(skillDir)
	}
	res.Files = files
	res.InSync = true
	return res, nil
}

// inSync reports whether the destination matches the payload.
//
// The meaning differs by mode on purpose. For a dry run it answers "would
// anything change", so only an already-matching file counts. For a real run it
// answers "does it match now", so a file this call just wrote counts too —
// otherwise repairing a torn install would report itself as still broken.
func inSync(files []FileResult, applied bool) bool {
	for _, f := range files {
		if f.Status == StatusCurrent {
			continue
		}
		if applied && f.Applied {
			continue
		}
		return false
	}
	return true
}

// assertNoSymlink refuses a symlink anywhere from root down to skillDir.
//
// ~/.claude/skills already contains symlinks into a shared skills tree on a
// real machine, so a linked entry here would write into someone else's
// canonical source. Not overridable by --yes: we cannot know whether the
// link's target is precious.
func assertNoSymlink(root, skillDir string) error {
	rel, err := filepath.Rel(root, skillDir)
	if err != nil {
		return errx.Internal("resolve %s: %v", skillDir, err)
	}
	current := root
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return symlinkError(current)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil // nothing below exists yet
		}
		if err != nil {
			return translateFS("inspect", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return symlinkError(current)
		}
	}
	return nil
}

// containsComponent reports whether p contains the given path segment run.
func containsComponent(p, component string) bool {
	return strings.Contains(filepath.ToSlash(p)+"/", filepath.ToSlash(component)+"/")
}

// translateFS turns a filesystem failure into a typed error.
//
// Left untyped, a read-only home or a permissions problem reaches the top
// level as CodeInternal with the hint "this is a bug in trello-cli; do not
// retry" — telling the caller to file a bug when the fix is one chmod.
func translateFS(op, p string, err error) error {
	if os.IsPermission(err) {
		return &errx.Error{
			Code:    errx.CodeUsage,
			Reason:  "WRITE_DENIED",
			Message: fmt.Sprintf("cannot %s %s: permission denied", op, p),
			Hint:    "check the directory's permissions, or pass --dest with a writable location",
		}
	}
	return errx.Internal("cannot %s %s: %v", op, p, err)
}
