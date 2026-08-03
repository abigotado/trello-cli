package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// isolateCacheDir points os.UserCacheDir at a temp directory.
//
// Both variables are set because the resolution differs by platform: darwin
// uses $HOME/Library/Caches, and Linux uses $XDG_CACHE_HOME. Without this a
// test would write into the developer's real cache.
func isolateCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "xdg"))
	return dir
}

func objects(names ...string) []Object {
	out := make([]Object, 0, len(names))
	for i, n := range names {
		out = append(out, Object{ID: string(rune('a'+i)) + "-id", Name: n})
	}
	return out
}

// A single shared index file would serve a work workspace's boards to a
// personal token, and the tool would then act on a board the caller cannot
// even see. Keying by token hash is what prevents that.
func TestCacheIsKeyedByToken(t *testing.T) {
	isolateCacheDir(t)

	work := NewCache("work-token", time.Minute)
	personal := NewCache("personal-token", time.Minute)

	if work.Path() == personal.Path() {
		t.Fatalf("two tokens share one cache file: %s", work.Path())
	}
	if strings.Contains(work.Path(), "work-token") {
		t.Errorf("the cache filename embeds the raw token: %s", work.Path())
	}

	work.Put("boards", objects("Work Roadmap"), time.Now())

	if _, ok := personal.Get("boards"); ok {
		t.Error("a different token read the first token's index")
	}
	got, ok := work.Get("boards")
	if !ok || len(got) != 1 || got[0].Name != "Work Roadmap" {
		t.Errorf("own index not readable: %v %v", got, ok)
	}
}

// The cache may only ever produce a hit. If a miss were authoritative,
// creating a list and immediately addressing it by name would fail against an
// index fetched before it existed.
func TestCacheMissIsNeverAuthoritative(t *testing.T) {
	isolateCacheDir(t)
	c := NewCache("tok", time.Minute)

	if _, ok := c.Get("boards"); ok {
		t.Error("an empty cache reported a hit")
	}
	c.Put("boards", objects("Roadmap"), time.Now())
	if _, ok := c.Get("lists:board-1"); ok {
		t.Error("an unpopulated scope reported a hit")
	}
}

func TestCacheExpiry(t *testing.T) {
	isolateCacheDir(t)
	c := NewCache("tok", 100*time.Millisecond)

	c.Put("boards", objects("Roadmap"), time.Now().Add(-time.Hour))
	if _, ok := c.Get("boards"); ok {
		t.Error("an entry older than the TTL was served")
	}

	c.Put("boards", objects("Roadmap"), time.Now())
	if _, ok := c.Get("boards"); !ok {
		t.Error("a fresh entry was not served")
	}
}

func TestInvalidateDropsOnlyItsScope(t *testing.T) {
	isolateCacheDir(t)
	c := NewCache("tok", time.Minute)
	now := time.Now()
	c.Put("boards", objects("Roadmap"), now)
	c.Put("lists:b1", objects("Doing"), now)

	c.Invalidate("boards")

	if _, ok := c.Get("boards"); ok {
		t.Error("the invalidated scope was still served")
	}
	if _, ok := c.Get("lists:b1"); !ok {
		t.Error("invalidating one scope dropped another")
	}
}

func TestClearRemovesTheFile(t *testing.T) {
	isolateCacheDir(t)
	c := NewCache("tok", time.Minute)
	c.Put("boards", objects("Roadmap"), time.Now())

	if _, err := os.Stat(c.Path()); err != nil {
		t.Fatalf("cache file was never written: %v", err)
	}
	if err := c.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, err := os.Stat(c.Path()); !os.IsNotExist(err) {
		t.Errorf("cache file survived Clear(): %v", err)
	}
	// Clearing an absent cache is not an error, so the command is idempotent.
	if err := c.Clear(); err != nil {
		t.Errorf("second Clear() error = %v", err)
	}
}

// Every entry is reconstructible from one request, so a damaged file is
// discarded rather than repaired or reported.
func TestCorruptAndStaleFilesAreDiscarded(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"not json", "{{{not json"},
		{"wrong version", `{"version":999,"entries":{"boards":{"fetched_at":"2030-01-01T00:00:00Z","objects":[{"id":"x","name":"X"}]}}}`},
		{"empty file", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateCacheDir(t)
			c := NewCache("tok", time.Minute)
			if err := os.MkdirAll(filepath.Dir(c.Path()), 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(c.Path(), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, ok := c.Get("boards"); ok {
				t.Error("a damaged cache file produced a hit")
			}
			// And it must still be usable afterwards.
			c.Put("boards", objects("Roadmap"), time.Now())
			if _, ok := c.Get("boards"); !ok {
				t.Error("cache was not usable after discarding a damaged file")
			}
		})
	}
}

// Two invocations of a short-lived binary can write at the same moment. The
// temp-file-plus-rename write means a reader sees the old file or the new one,
// never a half-written one — so no lock is needed.
func TestConcurrentWritesLeaveAValidFile(t *testing.T) {
	isolateCacheDir(t)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Separate Cache values, as separate processes would have.
			c := NewCache("tok", time.Minute)
			c.Put("boards", objects("Board"+string(rune('A'+i))), time.Now())
		}(i)
	}
	wg.Wait()

	c := NewCache("tok", time.Minute)
	got, ok := c.Get("boards")
	if !ok {
		t.Fatal("no readable entry after concurrent writes")
	}
	if len(got) != 1 {
		t.Errorf("entry has %d objects, want 1 (last writer wins)", len(got))
	}

	raw, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var parsed cacheFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Errorf("cache file is not valid JSON after concurrent writes: %v", err)
	}
}

// No temp files may survive a write, or the cache directory grows without
// bound across invocations.
func TestWritesLeaveNoTempFiles(t *testing.T) {
	isolateCacheDir(t)
	c := NewCache("tok", time.Minute)
	for i := 0; i < 5; i++ {
		c.Put("boards", objects("Roadmap"), time.Now())
	}
	entries, err := os.ReadDir(filepath.Dir(c.Path()))
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// Failing a command because a cache directory is unavailable would trade a
// small optimization for an outage.
func TestDisabledCacheIsInertNotFatal(t *testing.T) {
	c := NewCache("", time.Minute)
	if !c.Disabled() {
		t.Fatal("a cache with no token should be disabled")
	}
	if _, ok := c.Get("boards"); ok {
		t.Error("a disabled cache reported a hit")
	}
	// None of these may panic.
	c.Put("boards", objects("Roadmap"), time.Now())
	c.Invalidate("boards")
	if err := c.Clear(); err != nil {
		t.Errorf("Clear() on a disabled cache = %v", err)
	}
	if c.Path() != "" {
		t.Errorf("a disabled cache reported a path: %q", c.Path())
	}
}

// A nil *Cache is what a resolver holds when --no-cache is set.
func TestNilCacheIsSafe(t *testing.T) {
	var c *Cache
	if !c.Disabled() {
		t.Error("a nil cache should report disabled")
	}
	if _, ok := c.Get("boards"); ok {
		t.Error("a nil cache reported a hit")
	}
}

// The index maps names a caller typed to ids; it is not a secret, but it does
// describe a workspace, so it must not be world-readable.
func TestCacheFileIsNotWorldReadable(t *testing.T) {
	isolateCacheDir(t)
	c := NewCache("tok", time.Minute)
	c.Put("boards", objects("Roadmap"), time.Now())

	info, err := os.Stat(c.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("cache file mode is %o, want no group or other access", perm)
	}
}

// Rename makes the file never torn, but it does not make the write per-entry.
// Two invocations resolving different scopes would each rewrite the whole file
// from their own snapshot and discard the other's work.
func TestConcurrentWritesToDifferentScopesBothSurvive(t *testing.T) {
	isolateCacheDir(t)

	var wg sync.WaitGroup
	scopes := []string{"boards", "lists:b1", "lists:b2", "cards:b1"}
	for _, scope := range scopes {
		wg.Add(1)
		go func(scope string) {
			defer wg.Done()
			// Separate Cache values, as separate processes would have.
			NewCache("tok", time.Minute).Put(scope, objects("X-"+scope), time.Now())
		}(scope)
	}
	wg.Wait()

	c := NewCache("tok", time.Minute)
	for _, scope := range scopes {
		if _, ok := c.Get(scope); !ok {
			t.Errorf("scope %q was lost to a concurrent writer", scope)
		}
	}
}
