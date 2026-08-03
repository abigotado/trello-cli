package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abigotado/trello-cli/internal/errx"
)

func install(t *testing.T, opts Options) (Result, error) {
	t.Helper()
	return Install(context.Background(), opts)
}

func destOpts(t *testing.T, provider Provider) Options {
	t.Helper()
	return Options{Provider: provider, Scope: ScopeUser, Dest: t.TempDir()}
}

// //go:embed skills silently drops every file whose name starts with "." or
// "_" — no warning, no build error. The failure mode is silent omission, so
// only an equality test against the real tree catches it.
func TestEmbeddedPayloadMatchesTheTree(t *testing.T) {
	got, err := payload(ProviderCodex)
	if err != nil {
		t.Fatalf("payload() error = %v", err)
	}

	root := filepath.Join("..", "..", "assets", "skills", SkillName)
	want := map[string]bool{}
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		want[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk the source tree: %v", err)
	}

	for rel := range want {
		if _, ok := got[rel]; !ok {
			t.Errorf("%s is on disk but was not embedded", rel)
		}
	}
	for rel := range got {
		if !want[rel] {
			t.Errorf("%s was embedded but is not on disk", rel)
		}
	}
}

// embed.FS reports every file as 0444, so the payload's own modes carry no
// information. An executable that shipped would fail far from the cause.
func TestPayloadContainsNoExecutable(t *testing.T) {
	root := filepath.Join("..", "..", "assets", "skills", SkillName)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Mode()&0o111 != 0 {
			t.Errorf("%s is executable; the installer cannot preserve that", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// agents/openai.yaml is Codex UI metadata and is noise elsewhere.
func TestPayloadFilterPerProvider(t *testing.T) {
	tests := []struct {
		provider  Provider
		wantAgent bool
	}{
		{ProviderCodex, true},
		{ProviderClaude, false},
		{ProviderCursor, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			got, err := payload(tt.provider)
			if err != nil {
				t.Fatalf("payload() error = %v", err)
			}
			_, has := got["agents/openai.yaml"]
			if has != tt.wantAgent {
				t.Errorf("agents/openai.yaml present = %v, want %v", has, tt.wantAgent)
			}
			if _, ok := got["SKILL.md"]; !ok {
				t.Error("every provider must get SKILL.md")
			}
		})
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	opts := destOpts(t, ProviderClaude)

	first, err := install(t, opts)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	// After a real install in_sync means "it matches now", so a fresh install
	// is in sync — what tells a caller whether anything changed is the
	// per-file applied flags.
	if !first.InSync {
		t.Error("a completed install does not report in-sync")
	}
	applied := 0
	for _, f := range first.Files {
		if f.Applied {
			applied++
		}
	}
	if applied == 0 {
		t.Error("a fresh install applied nothing")
	}

	second, err := install(t, opts)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !second.InSync {
		t.Error("a re-install is not in sync")
	}
	for _, f := range second.Files {
		if f.Applied {
			t.Errorf("%s was rewritten on an unchanged re-install", f.Path)
		}
		if f.Status != StatusCurrent {
			t.Errorf("%s status = %s, want current", f.Path, f.Status)
		}
	}
}

// A dry run must be a pure read: the natural "plan then apply under one lock"
// refactor creates the directory and the lock file before it decides not to
// write, which is exactly what this catches.
func TestDryRunCreatesNothingAtAll(t *testing.T) {
	dest := t.TempDir()
	opts := Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: dest, DryRun: true}

	res, err := install(t, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun {
		t.Error("the result is not flagged as a dry run")
	}
	if len(res.Files) == 0 {
		t.Error("a dry run reported no plan")
	}
	// For a dry run in_sync means "nothing would change", and everything is
	// about to be written.
	if res.InSync {
		t.Error("a dry run against an empty destination reported in-sync")
	}
	for _, f := range res.Files {
		if f.Applied {
			t.Errorf("%s was applied during a dry run", f.Path)
		}
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a dry run created %v", names)
	}
}

// Clobbering a hand-written skill is the worst thing this command could do.
func TestForeignFileIsRefusedWithoutConfirmation(t *testing.T) {
	opts := destOpts(t, ProviderClaude)
	skillDir := filepath.Join(opts.Dest, SkillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handWritten := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(handWritten, []byte("# my own skill\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := install(t, opts)
	if errx.ExitCode(err) != errx.CodeConfirm {
		t.Fatalf("exit code = %d, want %d (confirmation required)", errx.ExitCode(err), errx.CodeConfirm)
	}
	got, readErr := os.ReadFile(handWritten)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(got) != "# my own skill\n" {
		t.Error("the hand-written file was overwritten despite the refusal")
	}

	// --yes is the caller asserting they accept the overwrite.
	opts.Confirmed = true
	if _, err := install(t, opts); err != nil {
		t.Fatalf("confirmed install: %v", err)
	}
	got, _ = os.ReadFile(handWritten)
	if !strings.Contains(string(got), "trello-cli") {
		t.Error("--yes did not overwrite the file")
	}
}

// A user edit to an installed file must be treated as theirs, not ours.
func TestEditedFileIsRefusedWithoutConfirmation(t *testing.T) {
	opts := destOpts(t, ProviderClaude)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	edited := filepath.Join(opts.Dest, SkillName, "SKILL.md")
	if err := os.WriteFile(edited, []byte("# edited by hand\n"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}

	_, err := install(t, opts)
	if errx.ExitCode(err) != errx.CodeConfirm {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeConfirm)
	}
}

// ~/.claude/skills contains symlinks into a shared tree on a real machine, so
// writing through one would land in someone else's canonical source. Not
// overridable by --yes: we cannot know whether the target is precious.
func TestSymlinksAreRefused(t *testing.T) {
	t.Run("skill directory is a symlink", func(t *testing.T) {
		dest := t.TempDir()
		elsewhere := t.TempDir()
		if err := os.Symlink(elsewhere, filepath.Join(dest, SkillName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		opts := Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: dest, Confirmed: true}
		_, err := install(t, opts)
		if err == nil {
			t.Fatal("a symlinked skill directory was accepted")
		}
		var typed *errx.Error
		if !asErr(err, &typed) || typed.Reason != "DEST_IS_SYMLINK" {
			t.Errorf("error = %v, want DEST_IS_SYMLINK", err)
		}
		// The link target must be untouched.
		entries, _ := os.ReadDir(elsewhere)
		if len(entries) != 0 {
			t.Error("the installer wrote through the symlink")
		}
	})

	t.Run("a payload file is a symlink", func(t *testing.T) {
		dest := t.TempDir()
		skillDir := filepath.Join(dest, SkillName)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		victim := filepath.Join(t.TempDir(), "precious.md")
		if err := os.WriteFile(victim, []byte("do not touch\n"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := os.Symlink(victim, filepath.Join(skillDir, "SKILL.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		opts := Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: dest, Confirmed: true}
		if _, err := install(t, opts); err == nil {
			t.Fatal("a symlinked payload file was accepted")
		}
		got, _ := os.ReadFile(victim)
		if string(got) != "do not touch\n" {
			t.Error("the symlink target was overwritten")
		}
	})
}

func TestDestValidation(t *testing.T) {
	tests := []struct {
		name   string
		dest   string
		reason string
	}{
		{"tilde is not expanded", "~/skills", "USAGE"},
		{"missing directory", filepath.Join(t.TempDir(), "nope"), "DEST_NOT_A_DIRECTORY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := install(t, Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: tt.dest})
			if err == nil {
				t.Fatal("expected an error")
			}
			var typed *errx.Error
			if !asErr(err, &typed) || typed.Reason != tt.reason {
				t.Errorf("error = %v, want reason %s", err, tt.reason)
			}
		})
	}

	t.Run("a file is not a directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err := install(t, Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: f})
		if errx.ExitCode(err) != errx.CodeUsage {
			t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
		}
	})
}

// sync-rules.py enforces an exact flat inventory of .cursor/rules and unlinks
// what it does not expect, so an install there would delete itself.
func TestNoDestinationLandsUnderCursorRules(t *testing.T) {
	for _, provider := range Providers() {
		for _, scope := range []Scope{ScopeUser, ScopeProject} {
			opts := Options{
				Provider:   provider,
				Scope:      scope,
				ProjectDir: t.TempDir(),
				HomeDir:    func() (string, error) { return "/home/tester", nil },
				LookupEnv:  func(string) (string, bool) { return "", false },
			}
			root, err := Root(opts)
			if err != nil {
				t.Fatalf("Root(%s/%s): %v", provider, scope, err)
			}
			if strings.Contains(filepath.ToSlash(root), ".cursor/rules") {
				t.Errorf("%s/%s resolves under .cursor/rules: %s", provider, scope, root)
			}
		}
	}
}

// In a repository with .agents/, the harness compiler owns .claude/ and
// .codex/ and prunes what it does not recognise, so installing there is an
// unbounded loop with no error at any step.
func TestProjectInstallRefusesAHarnessOwnedRepo(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".agents", "rules"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, provider := range []Provider{ProviderClaude, ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			_, err := Root(Options{Provider: provider, Scope: ScopeProject, ProjectDir: project})
			var typed *errx.Error
			if !asErr(err, &typed) || typed.Reason != "HARNESS_OWNED_DIRECTORY" {
				t.Errorf("error = %v, want HARNESS_OWNED_DIRECTORY", err)
			}
		})
	}

	// Cursor is exempt: the compiler has no cursor adapter.
	t.Run("cursor is exempt", func(t *testing.T) {
		if _, err := Root(Options{Provider: ProviderCursor, Scope: ScopeProject, ProjectDir: project}); err != nil {
			t.Errorf("cursor project install was refused: %v", err)
		}
	})
}

// Pruning must touch only files we recorded AND that still hash to what we
// wrote, or an install deletes something the user made theirs.
func TestOrphanPruningSparesEditedFiles(t *testing.T) {
	opts := destOpts(t, ProviderCodex)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	skillDir := filepath.Join(opts.Dest, SkillName)
	orphan := filepath.Join(skillDir, "agents", "openai.yaml")
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("expected the codex-only file: %v", err)
	}
	if err := os.WriteFile(orphan, []byte("# mine now\n"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}

	// Re-installing as claude drops agents/ from the payload, so the file
	// becomes an orphan — but it has been edited, so it must survive.
	claude := opts
	claude.Provider = ProviderClaude
	claude.Confirmed = true
	if _, err := install(t, claude); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	got, err := os.ReadFile(orphan)
	if err != nil {
		t.Fatalf("the edited orphan was deleted: %v", err)
	}
	if string(got) != "# mine now\n" {
		t.Error("the edited orphan was overwritten")
	}
}

func TestUninstallRemovesOnlyWhatWeOwn(t *testing.T) {
	opts := destOpts(t, ProviderClaude)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	skillDir := filepath.Join(opts.Dest, SkillName)
	edited := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(edited, []byte("# mine\n"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}

	res, err := Uninstall(context.Background(), opts)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(edited); err != nil {
		t.Error("uninstall removed a file the user had edited")
	}
	if _, err := os.Stat(filepath.Join(skillDir, "reference", "contract.md")); !os.IsNotExist(err) {
		t.Error("uninstall left an unedited file behind")
	}
	var reportedModified bool
	for _, f := range res.Files {
		if f.Status == StatusModified {
			reportedModified = true
		}
	}
	if !reportedModified {
		t.Error("uninstall did not report the file it declined to remove")
	}
}

// A torn install must self-heal rather than be reported as complete.
func TestATornInstallSelfHeals(t *testing.T) {
	opts := destOpts(t, ProviderClaude)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	victim := filepath.Join(opts.Dest, SkillName, "reference", "contract.md")
	if err := os.Remove(victim); err != nil {
		t.Fatalf("remove: %v", err)
	}

	res, err := install(t, opts)
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if !res.InSync {
		t.Error("a repaired install is not in sync")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the missing file was not restored: %v", err)
	}
}

func TestInstalledFilesAreNotExecutable(t *testing.T) {
	opts := destOpts(t, ProviderCodex)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	err := filepath.Walk(filepath.Join(opts.Dest, SkillName), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Mode()&0o111 != 0 {
			t.Errorf("%s installed executable (%v)", p, info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestParseProviderAndScope(t *testing.T) {
	for _, s := range []string{"claude", "codex", "cursor"} {
		if _, err := ParseProvider(s); err != nil {
			t.Errorf("ParseProvider(%q) = %v", s, err)
		}
	}
	if _, err := ParseProvider("windsurf"); errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("unknown provider exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
	}
	if _, err := ParseScope("global"); errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("unknown scope exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
	}
}

// os.UserConfigDir would give ~/Library/Application Support/.claude on macOS:
// the install reports success and no agent ever loads it.
func TestUserScopeUsesTheHomeDirectory(t *testing.T) {
	opts := Options{
		Provider:  ProviderClaude,
		Scope:     ScopeUser,
		HomeDir:   func() (string, error) { return "/home/tester", nil },
		LookupEnv: func(string) (string, bool) { return "", false },
	}
	root, err := Root(opts)
	if err != nil {
		t.Fatalf("Root(): %v", err)
	}
	want := filepath.Join("/home/tester", ".claude", "skills")
	if root != want {
		t.Errorf("root = %s, want %s", root, want)
	}
}

func TestCodexHonoursCodexHome(t *testing.T) {
	opts := Options{
		Provider:  ProviderCodex,
		Scope:     ScopeUser,
		HomeDir:   func() (string, error) { return "/home/tester", nil },
		LookupEnv: func(k string) (string, bool) { return "/custom/codex", k == "CODEX_HOME" },
	}
	root, err := Root(opts)
	if err != nil {
		t.Fatalf("Root(): %v", err)
	}
	if root != filepath.Join("/custom/codex", "skills") {
		t.Errorf("root = %s, want it under CODEX_HOME", root)
	}
}

// asErr is errors.As with the local error type, kept short for readability.
func asErr(err error, target **errx.Error) bool {
	for err != nil {
		if typed, ok := err.(*errx.Error); ok {
			*target = typed
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// The manifest is attacker-controlled input, not trusted state: a repository
// can ship one, and a project-scoped install reads it. filepath.Join does not
// reject "../" — it cleans it — so an entry naming an outside path was turned
// into a real path and then deleted by the orphan sweep, unconfirmed, with
// exit 0 and "ok": true.
func TestManifestCannotNameAPathOutsideTheSkillDirectory(t *testing.T) {
	escapes := []string{
		"../../../victim.md",
		"../sibling.md",
		"a/../../../victim.md",
		"/etc/passwd",
	}
	for _, rel := range escapes {
		t.Run(rel, func(t *testing.T) {
			dest := t.TempDir()
			skillDir := filepath.Join(dest, SkillName)
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			victim := filepath.Join(dest, "victim.md")
			if err := os.WriteFile(victim, []byte("precious\n"), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			seedManifest(t, skillDir, rel, sum([]byte("precious\n")))

			opts := Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: dest, Confirmed: true}
			_, err := install(t, opts)
			if err == nil {
				t.Fatal("an escaping manifest entry was accepted")
			}
			var typed *errx.Error
			if !asErr(err, &typed) || typed.Reason != "MANIFEST_CORRUPT" {
				t.Errorf("error = %v, want MANIFEST_CORRUPT", err)
			}
			if _, statErr := os.Stat(victim); statErr != nil {
				t.Error("the outside file was deleted")
			}

			// Uninstall reads the same manifest and must refuse identically.
			if _, err := Uninstall(context.Background(), opts); err == nil {
				t.Error("uninstall accepted an escaping manifest entry")
			}
			if _, statErr := os.Stat(victim); statErr != nil {
				t.Error("uninstall deleted the outside file")
			}
		})
	}
}

// Guarding only the leaf was not enough: MkdirAll succeeds silently when an
// intermediate directory is a link to an existing one, so the write landed
// inside the link target. That is the shared-skills-tree case the refusal
// exists for.
func TestASymlinkedSubdirectoryIsRefused(t *testing.T) {
	dest := t.TempDir()
	shared := t.TempDir()
	precious := filepath.Join(shared, "commands.md")
	if err := os.WriteFile(precious, []byte("hand maintained\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	skillDir := filepath.Join(dest, SkillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(shared, filepath.Join(skillDir, "reference")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	opts := Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: dest, Confirmed: true}
	if _, err := install(t, opts); err == nil {
		t.Fatal("a symlinked subdirectory was written through")
	}
	got, err := os.ReadFile(precious)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hand maintained\n" {
		t.Error("the link target was overwritten")
	}
}

// Install refuses to write through a link; removing through one would be the
// same mistake with a worse outcome.
func TestUninstallRefusesASymlinkedSkillDirectory(t *testing.T) {
	dest := t.TempDir()
	elsewhere := t.TempDir()
	keep := filepath.Join(elsewhere, "keep.md")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dest, SkillName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	opts := Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: dest, Confirmed: true}
	if _, err := Uninstall(context.Background(), opts); err == nil {
		t.Fatal("uninstall followed a symlinked skill directory")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("uninstall removed something through the link")
	}
}

// An install interrupted after the in-progress marker must still own its
// files. Writing an empty file list as that marker disowned everything the
// previous run installed, so the next run called them all foreign and refused
// with exit 7 — the opposite of self-healing.
func TestAnInterruptedInstallStillOwnsItsFiles(t *testing.T) {
	opts := destOpts(t, ProviderClaude)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	skillDir := filepath.Join(opts.Dest, SkillName)

	// Simulate the interruption: the marker is on disk, the payload is not.
	raw, err := os.ReadFile(filepath.Join(skillDir, manifestName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if !m.Complete {
		t.Fatal("a finished install left the manifest incomplete")
	}
	if len(m.Files) == 0 {
		t.Fatal("the manifest records no files, so nothing is owned")
	}
	m.Complete = false
	torn, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(skillDir, manifestName), torn, 0o644); err != nil {
		t.Fatalf("write torn manifest: %v", err)
	}
	if err := os.Remove(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Without --yes: the files are still ours, so this must not refuse.
	res, err := install(t, Options{Provider: ProviderClaude, Scope: ScopeUser, Dest: opts.Dest})
	if err != nil {
		t.Fatalf("repairing a torn install failed: %v", err)
	}
	if !res.InSync {
		t.Error("the repaired install is not in sync")
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Errorf("the missing file was not restored: %v", err)
	}
}

// A caller's own --dest is their mistake to fix, not a defect to report.
func TestCursorRulesDestinationIsAUsageError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), ".cursor", "rules")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := install(t, Options{Provider: ProviderCursor, Scope: ScopeUser, Dest: dest})
	if errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
	}
}

// Uninstall must not claim in_sync when it deliberately left a file behind.
func TestUninstallReportsWhatItLeft(t *testing.T) {
	opts := destOpts(t, ProviderClaude)
	if _, err := install(t, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	edited := filepath.Join(opts.Dest, SkillName, "SKILL.md")
	if err := os.WriteFile(edited, []byte("# mine\n"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	res, err := Uninstall(context.Background(), opts)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if res.InSync {
		t.Error("uninstall reported in_sync while leaving an edited file behind")
	}
}

// seedManifest writes a manifest naming one file, as a hostile repository would.
func seedManifest(t *testing.T, skillDir, rel, hash string) {
	t.Helper()
	m := manifest{
		Version: 1, Skill: SkillName, Provider: string(ProviderClaude), Complete: true,
		Files: []manifestFile{{Path: rel, SHA256: hash}},
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, manifestName), raw, 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
}
