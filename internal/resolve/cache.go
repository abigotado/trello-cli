package resolve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/abigotado/trello-cli/internal/lockfile"
)

// cacheVersion is bumped when the on-disk shape changes. A file with a
// different version is ignored rather than migrated: it is a cache, and
// discarding it costs one extra request.
const cacheVersion = 1

// DefaultTTL bounds how long an index entry is trusted.
//
// Short on purpose. The cache exists to save round trips within and between a
// few consecutive agent invocations, not to be a database.
const DefaultTTL = 5 * time.Minute

// Cache stores name-to-id indexes between invocations.
//
// It is keyed by a hash of the token. A single shared file would serve a work
// workspace's board index to a personal token, and the tool would then act on
// the wrong board — the worst possible failure for something an agent drives.
type Cache struct {
	path string
	ttl  time.Duration

	loaded bool
	data   cacheFile
	// dropped records scopes invalidated here, so merging with a concurrent
	// writer's copy does not restore an entry we just proved stale.
	dropped map[string]bool
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

type cacheEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	Objects   []Object  `json:"objects"`
}

// NewCache builds a cache for the given credential.
//
// A cache that cannot be located is disabled rather than fatal: failing a
// command because a cache directory is unavailable would trade a small
// optimization for an outage.
func NewCache(token string, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	dir, err := os.UserCacheDir()
	if err != nil || token == "" {
		return &Cache{ttl: ttl}
	}
	sum := sha256.Sum256([]byte(token))
	name := "index-" + hex.EncodeToString(sum[:])[:12] + ".json"
	return &Cache{path: filepath.Join(dir, "trello-cli", name), ttl: ttl}
}

// Disabled reports whether this cache does nothing.
func (c *Cache) Disabled() bool { return c == nil || c.path == "" }

// Get returns the cached objects for scope.
//
// The cache may only ever produce a hit. A miss falls through to a live
// lookup, which is what keeps `lists create Done` followed by
// `cards create --list Done` working: a TTL alone would serve the index from
// before the list existed and report it missing.
func (c *Cache) Get(scope string) ([]Object, bool) {
	if c.Disabled() {
		return nil, false
	}
	c.load()
	entry, ok := c.data.Entries[scope]
	if !ok || time.Since(entry.FetchedAt) > c.ttl {
		return nil, false
	}
	return entry.Objects, true
}

// Put records objects for scope.
func (c *Cache) Put(scope string, objects []Object, now time.Time) {
	if c.Disabled() {
		return
	}
	c.load()
	c.data.Entries[scope] = cacheEntry{FetchedAt: now, Objects: objects}
	c.save()
}

// Invalidate drops one scope. Called when a cached id turns out to be gone.
func (c *Cache) Invalidate(scope string) {
	if c.Disabled() {
		return
	}
	c.load()
	if _, ok := c.data.Entries[scope]; !ok {
		return
	}
	delete(c.data.Entries, scope)
	if c.dropped == nil {
		c.dropped = map[string]bool{}
	}
	c.dropped[scope] = true
	c.save()
}

// Clear removes the cache file entirely.
func (c *Cache) Clear() error {
	if c.Disabled() {
		return nil
	}
	c.loaded = false
	c.data = cacheFile{}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path reports the on-disk location, for `cache clear` to report.
func (c *Cache) Path() string {
	if c.Disabled() {
		return ""
	}
	return c.path
}

// mergeFromDisk folds in scopes another invocation wrote since this one
// loaded, preferring whichever entry is fresher.
func (c *Cache) mergeFromDisk() {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var onDisk cacheFile
	if json.Unmarshal(raw, &onDisk) != nil || onDisk.Version != cacheVersion {
		return
	}
	for scope, entry := range onDisk.Entries {
		if c.dropped[scope] {
			continue // invalidated here on purpose; do not resurrect it
		}
		if mine, ok := c.data.Entries[scope]; !ok || entry.FetchedAt.After(mine.FetchedAt) {
			c.data.Entries[scope] = entry
		}
	}
}

func (c *Cache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.data = cacheFile{Version: cacheVersion, Entries: map[string]cacheEntry{}}

	raw, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var onDisk cacheFile
	// A corrupt or stale-version file is discarded, never repaired. Every
	// entry is reconstructible from one request.
	if json.Unmarshal(raw, &onDisk) != nil || onDisk.Version != cacheVersion {
		return
	}
	if onDisk.Entries != nil {
		c.data.Entries = onDisk.Entries
	}
}

// save merges with whatever is on disk, then writes atomically.
//
// Rename alone makes the file never torn, but it does not make the write
// per-entry: two invocations resolving different scopes would each rewrite the
// whole file from their own snapshot and discard the other's entry. Merging
// keeps both. Within one scope the last writer still wins, which is correct —
// both had just fetched it.
func (c *Cache) save() {
	if c.Disabled() {
		return
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	// Merge and write under one lock, so two invocations resolving different
	// scopes cannot interleave read-modify-write and drop each other's entry.
	_ = lockfile.With(c.path, func() error {
		c.mergeFromDisk()
		c.write(dir)
		return nil
	})
}

func (c *Cache) write(dir string) {
	raw, err := json.Marshal(c.data)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() {
		// Best effort: if the rename succeeded this fails harmlessly.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmpName, c.path)
}
