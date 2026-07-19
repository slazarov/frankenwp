package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/maypok86/otter/v2"
	"go.uber.org/zap"
)

var (
	ErrCacheExpired  = errors.New("cache expired")
	ErrCacheNotFound = errors.New("key not found in cache")

	CachedContentEncoding = []string{
		"none",
		"gzip",
		"br",
		"zstd",
	}
)

const (
	CACHE_DIR = "sidekick-cache"
	// keySep separates the path component of a cache key from the query
	// variant, and the variant from the content-encoding. It is intentionally
	// a token that cannot appear inside a sanitized single path segment so
	// that a prefix purge on "<path>::" is segment-exact.
	keySep = "::"
)

// Store is a two-tier full-page cache: an in-memory Otter cache (adaptive
// W-TinyLFU, weight-bounded) backed by an on-disk store under <loc>/sidekick-cache.
type Store struct {
	loc    string
	ttl    int
	logger *zap.Logger

	memMaxSize  int
	memMaxCount int

	mem *otter.Cache[string, *MemCacheItem]

	// diskMu serializes bulk disk removal (Purge/Flush, write-lock) against
	// individual entry writes/reads (Set / disk load, read-lock) so a
	// RemoveAll never races a WriteFile on the same directory.
	diskMu sync.RWMutex

	// populating dedups concurrent live-miss writers for the same page variant
	// so only one CustomWriter stores the response; followers serve the origin
	// but skip the write. Keyed by dirKey (path::variant).
	populating sync.Map
}

// tryLeadPopulation returns true for the first caller for a dirKey (the leader,
// which will store the response). Concurrent callers get false until the leader
// calls donePopulation.
func (d *Store) tryLeadPopulation(dirKey string) bool {
	_, loaded := d.populating.LoadOrStore(dirKey, struct{}{})
	return !loaded
}

func (d *Store) donePopulation(dirKey string) {
	d.populating.Delete(dirKey)
}

type MemCacheItem struct {
	*CacheMeta
	value []byte
}

// NewStore builds the store and eagerly creates the cache directory, returning
// an error if it is not creatable/writable so Provision can fail fast.
func NewStore(loc string, ttl int, memMaxSize int, memMaxCount int, logger *zap.Logger) (*Store, error) {
	if loc == "" {
		return nil, errors.New("wp_cache: cache location (loc/CACHE_LOC) is empty")
	}
	if err := os.MkdirAll(path.Join(loc, CACHE_DIR), 0o755); err != nil {
		return nil, fmt.Errorf("wp_cache: cannot create cache dir %q: %w", path.Join(loc, CACHE_DIR), err)
	}

	d := &Store{
		loc:         loc,
		ttl:         ttl,
		logger:      logger,
		memMaxSize:  memMaxSize,
		memMaxCount: memMaxCount,
	}

	opts := &otter.Options[string, *MemCacheItem]{
		// Bound the memory tier by total bytes (key + value). Otter uses a
		// single bound; memory_max_count now only seeds the initial capacity.
		MaximumWeight: uint64(memMaxSize),
		Weigher: func(key string, value *MemCacheItem) uint32 {
			if value == nil {
				return uint32(len(key))
			}
			return uint32(len(key) + len(value.value))
		},
	}
	if memMaxCount > 0 {
		opts.InitialCapacity = memMaxCount
	}
	if ttl > 0 {
		// Backstop bound on how long a memory entry may live; the authoritative
		// TTL check is done on read against CacheMeta.Timestamp.
		opts.ExpiryCalculator = otter.ExpiryWriting[string, *MemCacheItem](time.Duration(ttl) * time.Second)
	}

	c, err := otter.New(opts)
	if err != nil {
		return nil, fmt.Errorf("wp_cache: init memory cache: %w", err)
	}
	d.mem = c

	return d, nil
}

// pathKey sanitizes a request path into a single filesystem-safe token.
func (d *Store) pathKey(reqPath string) string {
	return strings.ReplaceAll(reqPath, "/", "+")
}

// dirKey is the on-disk directory name / memory key prefix for a page variant.
func (d *Store) dirKey(reqPath, variant string) string {
	return d.pathKey(reqPath) + keySep + variant
}

// Get returns the cached body + meta for a page variant and encoding, loading
// from disk on a memory miss. Concurrent loads of the same key are coalesced by
// Otter. An expired entry (per CacheMeta.Timestamp) is reported as a miss.
func (d *Store) Get(ctx context.Context, reqPath, variant, ce string) ([]byte, *CacheMeta, error) {
	dirKey := d.dirKey(reqPath, variant)
	otterKey := dirKey + keySep + ce

	item, err := d.mem.Get(ctx, otterKey, otter.LoaderFunc[string, *MemCacheItem](
		func(ctx context.Context, _ string) (*MemCacheItem, error) {
			return d.loadFromDisk(dirKey, ce)
		},
	))
	if err != nil || item == nil {
		return nil, nil, ErrCacheNotFound
	}

	if d.expired(item.CacheMeta) {
		d.mem.Invalidate(otterKey)
		return nil, nil, ErrCacheExpired
	}

	return item.value, item.CacheMeta, nil
}

func (d *Store) expired(meta *CacheMeta) bool {
	return d.ttl > 0 && meta != nil && time.Now().Unix() > meta.Timestamp+int64(d.ttl)
}

func (d *Store) loadFromDisk(dirKey, ce string) (*MemCacheItem, error) {
	d.diskMu.RLock()
	defer d.diskMu.RUnlock()

	base := path.Join(d.loc, CACHE_DIR, dirKey)

	meta := &CacheMeta{}
	if err := meta.LoadFromFile(path.Join(base, ".meta."+ce)); err != nil {
		// Backward-compat: caches written before per-encoding meta used a
		// single shared ".meta". Fall back once; new writes are per-encoding.
		if err2 := meta.LoadFromFile(path.Join(base, ".meta")); err2 != nil {
			return nil, ErrCacheNotFound
		}
	}

	value, err := os.ReadFile(path.Join(base, "."+ce))
	if err != nil {
		return nil, ErrCacheNotFound
	}

	if d.expired(meta) {
		return nil, ErrCacheExpired
	}

	meta.contentEncoding = ce
	return &MemCacheItem{CacheMeta: meta, value: value}, nil
}

// Peek returns the current cached meta for a variant/encoding without loading
// from disk or affecting recency; used to carry validators across recache.
func (d *Store) Peek(reqPath, variant, ce string) (*CacheMeta, bool) {
	otterKey := d.dirKey(reqPath, variant) + keySep + ce
	item, ok := d.mem.GetIfPresent(otterKey)
	if !ok || item == nil {
		return nil, false
	}
	return item.CacheMeta, true
}

// Set stores a page variant/encoding into memory and disk. Body is written
// before meta, both atomically (temp file + rename), so a reader never pairs a
// fresh meta with a short/missing body.
func (d *Store) Set(reqPath, variant string, meta *CacheMeta, value []byte) error {
	dirKey := d.dirKey(reqPath, variant)
	ce := meta.contentEncoding
	otterKey := dirKey + keySep + ce

	d.mem.Set(otterKey, &MemCacheItem{CacheMeta: meta, value: value})

	d.diskMu.RLock()
	defer d.diskMu.RUnlock()

	base := path.Join(d.loc, CACHE_DIR, dirKey)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}

	var errs []error
	if err := writeFileAtomic(path.Join(base, "."+ce), value, 0o644); err != nil {
		errs = append(errs, fmt.Errorf("write body: %w", err))
	}
	if err := meta.WriteToFileAtomic(path.Join(base, ".meta."+ce)); err != nil {
		errs = append(errs, fmt.Errorf("write meta: %w", err))
	}
	return errors.Join(errs...)
}

// Purge removes a single page (all query variants + encodings) from memory and
// disk. The match is segment-exact: purging "/post" never removes "/post-2".
func (d *Store) Purge(reqPath string) {
	prefix := d.pathKey(reqPath) + keySep

	for k := range d.mem.All() {
		if strings.HasPrefix(k, prefix) {
			d.mem.Invalidate(k)
		}
	}

	d.diskMu.Lock()
	defer d.diskMu.Unlock()

	base := path.Join(d.loc, CACHE_DIR)
	files, err := os.ReadDir(base)
	if err != nil {
		d.logger.Error("wp_cache: purge readdir", zap.Error(err))
		return
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), prefix) {
			continue
		}
		fp := path.Join(base, f.Name())
		if err := os.RemoveAll(fp); err != nil {
			d.logger.Error("wp_cache: purge remove", zap.String("fp", fp), zap.Error(err))
		}
	}
}

// Flush empties the entire cache (memory + disk).
func (d *Store) Flush() error {
	d.mem.InvalidateAll()

	d.diskMu.Lock()
	defer d.diskMu.Unlock()

	base := path.Join(d.loc, CACHE_DIR)
	files, err := os.ReadDir(base)
	if err != nil {
		d.logger.Error("wp_cache: flush readdir", zap.Error(err))
		return err
	}
	var errs []error
	for _, f := range files {
		fp := path.Join(base, f.Name())
		if err := os.RemoveAll(fp); err != nil {
			errs = append(errs, err)
			d.logger.Error("wp_cache: flush remove", zap.String("fp", fp), zap.Error(err))
		}
	}
	return errors.Join(errs...)
}

// List enumerates the cache contents (used by the authenticated purge GET).
func (d *Store) List() map[string][]string {
	list := map[string][]string{
		"mem":  {},
		"disk": {},
	}

	for k := range d.mem.All() {
		list["mem"] = append(list["mem"], k)
	}

	base := path.Join(d.loc, CACHE_DIR)
	if files, err := os.ReadDir(base); err == nil {
		for _, file := range files {
			if !file.IsDir() {
				continue
			}
			for _, name := range CachedContentEncoding {
				if _, err := os.Stat(path.Join(base, file.Name(), "."+name)); err == nil {
					list["disk"] = append(list["disk"], file.Name()+keySep+name)
				}
			}
		}
	}

	list["debug"] = []string{
		fmt.Sprintf("max_weight=%d", d.memMaxSize),
		fmt.Sprintf("weighted_size=%d", d.mem.WeightedSize()),
		fmt.Sprintf("count=%d", d.mem.EstimatedSize()),
	}

	return list
}

// writeFileAtomic writes data to fp via a temp file in the same directory
// followed by an atomic rename.
func writeFileAtomic(fp string, data []byte, perm os.FileMode) error {
	dir := path.Dir(fp)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, fp)
}
