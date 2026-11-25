package weed_server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/seaweedfs/seaweedfs/weed/pb"
	"github.com/seaweedfs/seaweedfs/weed/util/version"
)

func TestFilerDownloadCacheFoldersOwnershipAndReopen(t *testing.T) {
	option := &FilerOption{
		Host: pb.ServerAddress("localhost:8888"),
		DownloadCacheOption: DownloadCacheOption{
			DownloadCacheDir: t.TempDir(), DownloadCacheCapacityMB: 32, DownloadSpoolDir: t.TempDir(),
		},
	}
	c, closeCache, err := newFilerDownloadCache(option)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCache()
	if c.SpoolDir != option.DownloadSpoolDir || c.Cache == nil {
		t.Fatal("download cache options not applied")
	}
	if _, _, err := newFilerDownloadCache(option); err == nil {
		t.Fatal("second cache owner accepted")
	}
	const fid = "1,0000000100000001"
	if err := c.Cache.SetChunkFromReader(fid, bytes.NewBufferString("cached"), 6); err != nil {
		t.Fatal(err)
	}
	closeCache()
	c, closeAgain, err := newFilerDownloadCache(option)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAgain()
	var got bytes.Buffer
	if hit, err := c.Cache.CopyChunkRange(&got, fid, 6, 0, 6); !hit || err != nil || got.String() != "cached" {
		t.Fatalf("reopen hit=%v bytes=%q err=%v", hit, got.String(), err)
	}
	closeAgain()

	// Another build's segments may use a different on-disk format.
	stamps, err := filepath.Glob(filepath.Join(option.DownloadCacheDir, "*", "version"))
	if err != nil || len(stamps) != 1 {
		t.Fatalf("version stamps %v: %v", stamps, err)
	}
	if err := os.WriteFile(stamps[0], []byte("other build"), 0600); err != nil {
		t.Fatal(err)
	}
	c, closeUpgraded, err := newFilerDownloadCache(option)
	if err != nil {
		t.Fatal(err)
	}
	defer closeUpgraded()
	if c.Cache.HasChunk(fid, 6) {
		t.Fatal("chunk from another build survived")
	}
	if b, err := os.ReadFile(stamps[0]); err != nil || string(b) != version.Version() {
		t.Fatalf("version stamp %q: %v", b, err)
	}
}

func TestFilerDownloadCacheDisabledAndInvalidFolders(t *testing.T) {
	c, closeCache, err := newFilerDownloadCache(&FilerOption{})
	if err != nil || c.Cache != nil || c.SpoolDir != "" {
		t.Fatalf("default cache=%v err=%v", c, err)
	}
	closeCache()
	dir := t.TempDir()
	c, closeCache, err = newFilerDownloadCache(&FilerOption{DownloadCacheOption: DownloadCacheOption{DownloadCacheDir: dir}})
	if err != nil || c.Cache != nil || c.SpoolDir == "" {
		t.Fatalf("staging-only cache=%v err=%v", c, err)
	}
	closeCache()
	file, err := os.CreateTemp(t.TempDir(), "not-directory")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	for _, option := range []DownloadCacheOption{{DownloadCacheDir: file.Name()}, {DownloadSpoolDir: file.Name()}, {DownloadCacheCapacityMB: -1}, {DownloadStagingDiskMB: -1}, {DownloadStagingMemoryThresholdMB: 2, DownloadStagingMemoryMB: 1}} {
		if _, closeCache, err := newFilerDownloadCache(&FilerOption{DownloadCacheOption: option}); err == nil {
			closeCache()
			t.Fatalf("invalid option accepted: %+v", option)
		}
	}
}
