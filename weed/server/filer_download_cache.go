package weed_server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
	"github.com/seaweedfs/seaweedfs/weed/filer"
	"github.com/seaweedfs/seaweedfs/weed/util"
	"github.com/seaweedfs/seaweedfs/weed/util/chunk_cache"
	"github.com/seaweedfs/seaweedfs/weed/util/version"
)

// DownloadCacheOption configures HTTP download staging and chunk retention.
type DownloadCacheOption struct {
	DownloadCacheDir        string
	DownloadCacheCapacityMB int64
	DownloadSpoolDir        string
	// Staging budgets are shared by all downloads of this filer.
	DownloadStagingMemoryThresholdMB int64
	DownloadStagingMemoryMB          int64
	DownloadStagingDiskMB            int64
}

// The filer owns its cache instance. Never open a mount's live cache files:
// segment rotation and indexes require a single process owner.
func newFilerDownloadCache(option *FilerOption) (*filer.DownloadCache, func(), error) {
	if option.DownloadCacheCapacityMB < 0 || option.DownloadCacheCapacityMB > (1<<43) {
		return nil, nil, fmt.Errorf("invalid download cache capacity %d MB", option.DownloadCacheCapacityMB)
	}
	for _, mb := range []int64{option.DownloadStagingMemoryThresholdMB, option.DownloadStagingMemoryMB, option.DownloadStagingDiskMB} {
		if mb < 0 || mb > (1<<43) {
			return nil, nil, fmt.Errorf("invalid download staging size %d MB", mb)
		}
	}
	if option.DownloadStagingMemoryThresholdMB > option.DownloadStagingMemoryMB {
		return nil, nil, fmt.Errorf("download staging memory threshold %d MB exceeds staging memory %d MB", option.DownloadStagingMemoryThresholdMB, option.DownloadStagingMemoryMB)
	}
	cache := &filer.DownloadCache{
		SpoolDir:    option.DownloadSpoolDir,
		SpoolBudget: filer.NewChunkSpoolBudget(option.DownloadStagingMemoryThresholdMB<<20, option.DownloadStagingMemoryMB<<20, option.DownloadStagingDiskMB<<20),
	}
	var lock *flock.Flock
	cleanup := func() {
		if cache.Cache != nil {
			cache.Cache.Shutdown()
		}
		if lock != nil {
			_ = lock.Unlock()
		}
	}
	var once sync.Once
	closeCache := func() { once.Do(cleanup) }
	if option.DownloadCacheDir != "" {
		// Scope by filer identity, not pid or version, so retained chunks survive restarts.
		dir := filepath.Join(option.DownloadCacheDir, util.Md5String([]byte("filer-download" + string(option.Host)))[0:8])
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, nil, fmt.Errorf("create download cache directory: %w", err)
		}
		lock = flock.New(filepath.Join(dir, "cache.lock"))
		locked, err := lock.TryLock()
		if err != nil || !locked {
			return nil, nil, fmt.Errorf("download cache directory %s is unavailable or in use: %v", dir, err)
		}
		if err := resetCacheOnVersionChange(dir); err != nil {
			closeCache()
			return nil, nil, fmt.Errorf("reset download cache in %s: %w", dir, err)
		}
		if cache.SpoolDir == "" {
			cache.SpoolDir = filepath.Join(dir, "staging")
		}
		if option.DownloadCacheCapacityMB > 0 {
			cache.Cache = chunk_cache.NewTieredChunkCache(0, dir, option.DownloadCacheCapacityMB, 1024*1024)
			if !cache.Cache.DiskCacheAvailable() {
				closeCache()
				return nil, nil, fmt.Errorf("cannot initialize download chunk cache in %s", dir)
			}
			cache.Manifests = filer.NewChunkManifestCache(filer.MaxMountChunkManifestCacheEntries, filer.MaxMountChunkManifestCacheBytes)
		}
	}
	if cache.SpoolDir != "" {
		if err := os.MkdirAll(cache.SpoolDir, 0700); err != nil {
			closeCache()
			return nil, nil, fmt.Errorf("create download staging directory: %w", err)
		}
		// Fail at startup rather than on the first large response.
		f, err := os.CreateTemp(cache.SpoolDir, ".write-test-*")
		if err != nil {
			closeCache()
			return nil, nil, fmt.Errorf("download staging directory is not writable: %w", err)
		}
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	return cache, closeCache, nil
}

// resetCacheOnVersionChange drops segments written by another build, whose on-disk format may differ.
func resetCacheOnVersionChange(dir string) error {
	stamp := filepath.Join(dir, "version")
	if b, err := os.ReadFile(stamp); err == nil && string(b) == version.Version() {
		return nil
	}
	for _, pattern := range []string{"c0_2_*", "c1_3_*", "c2_2_*"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return err
		}
		for _, m := range matches {
			if err := os.RemoveAll(m); err != nil {
				return err
			}
		}
	}
	return os.WriteFile(stamp, []byte(version.Version()), 0600)
}
