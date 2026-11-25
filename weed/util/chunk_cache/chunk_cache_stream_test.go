package chunk_cache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/storage/needle"
)

// startBlockedInsert begins a 6 MiB insert that stalls after its first MiB.
func startBlockedInsert(t *testing.T, cache *TieredChunkCache, fid string, payload []byte) (unblock func(), done <-chan error) {
	t.Helper()
	pr, pw := io.Pipe()
	result := make(chan error, 1)
	go func() { result <- cache.SetChunkFromReader(fid, pr, int64(len(payload))) }()
	if _, err := pw.Write(payload[:1<<20]); err != nil {
		t.Fatal(err)
	}
	return func() {
		_, _ = pw.Write(payload[1<<20:])
		_ = pw.Close()
	}, result
}

func within(t *testing.T, what string, f func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() { f(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked behind an in-flight insert", what)
	}
}

func TestStreamingChunkCacheInsertDoesNotBlockOthers(t *testing.T) {
	cache := NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	const cached, slow, other = "1,0000000100000001", "1,0000000200000001", "1,0000000300000001"
	// Above the memory tier, so reads must hit disk.
	cachedData := bytes.Repeat([]byte("c"), 2<<20)
	cache.SetChunk(cached, cachedData)
	slowData := bytes.Repeat([]byte("s"), 6<<20)
	unblock, done := startBlockedInsert(t, cache, slow, slowData)

	within(t, "ReadChunkAt", func() {
		got := make([]byte, len(cachedData))
		if n, _ := cache.ReadChunkAt(got, cached, 0); n != len(got) || !bytes.Equal(got, cachedData) {
			t.Errorf("ReadChunkAt n=%d", n)
		}
	})
	within(t, "CopyChunkRange", func() {
		if hit, err := cache.CopyChunkRange(io.Discard, cached, int64(len(cachedData)), 0, int64(len(cachedData))); !hit || err != nil {
			t.Errorf("CopyChunkRange hit=%v err=%v", hit, err)
		}
	})
	within(t, "another insert", func() { cache.SetChunk(other, []byte("other")) })
	if cache.HasChunk(slow, int64(len(slowData))) {
		t.Fatal("incomplete entry became visible")
	}

	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if hit, err := cache.CopyChunkRange(&got, slow, int64(len(slowData)), 0, int64(len(slowData))); !hit || err != nil || !bytes.Equal(got.Bytes(), slowData) {
		t.Fatalf("slow entry hit=%v err=%v", hit, err)
	}
}

func TestStreamingChunkCacheRecycleKeepsPinnedReadsAndDropsStaleInserts(t *testing.T) {
	// The largest tier has two 8 MiB segments; each 6 MiB chunk fills one.
	cache := NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	const pinned, slow = "1,0000000100000001", "1,0000000200000001"
	chunk := func(b byte) []byte { return bytes.Repeat([]byte{b}, 6<<20) }
	if err := cache.SetChunkFromReader(pinned, bytes.NewReader(chunk('p')), 6<<20); err != nil {
		t.Fatal(err)
	}
	fid, _ := needle.ParseFileIdFromString(pinned)
	v, start, _, ok := cache.acquireEntry(fid, 6<<20, 0)
	if !ok {
		t.Fatal("pinned entry missing")
	}
	unblock, done := startBlockedInsert(t, cache, slow, chunk('s'))
	// Two more inserts recycle both the pinned and the in-flight segments.
	for i, f := range []string{"1,0000000300000001", "1,0000000400000001"} {
		if err := cache.SetChunkFromReader(f, bytes.NewReader(chunk(byte('a'+i))), 6<<20); err != nil {
			t.Fatal(err)
		}
	}
	if cache.HasChunk(pinned, 6<<20) {
		t.Fatal("recycled entry still indexed")
	}
	got := make([]byte, 6<<20)
	if _, err := v.DataBackend.ReadAt(got, start+cacheHeaderSize); err != nil || !bytes.Equal(got, chunk('p')) {
		t.Fatalf("pinned read changed after recycling: %v", err)
	}
	v.release()

	unblock()
	if err := <-done; err == nil {
		t.Fatal("insert into a recycled segment was published")
	}
	if cache.HasChunk(slow, 6<<20) {
		t.Fatal("stale insert became visible")
	}
}

func TestStreamingChunkCacheCompatibilityAndPersistence(t *testing.T) {
	dir := t.TempDir()
	cache := NewTieredChunkCache(16, dir, 32, 1024*1024)
	const fid = "1,0000000100000001"
	payload := bytes.Repeat([]byte("streamed chunk"), 10000)
	if err := cache.SetChunkFromReader(fid, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	// Mount's existing read API sees entries written through streaming insertion.
	got := make([]byte, len(payload))
	if n, err := cache.ReadChunkAt(got, fid, 0); n != len(payload) || err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("legacy read n=%d err=%v", n, err)
	}
	cache.Shutdown()
	cache = NewTieredChunkCache(16, dir, 32, 1024*1024)
	defer cache.Shutdown()
	var part bytes.Buffer
	hit, err := cache.CopyChunkRange(&part, fid, int64(len(payload)), 17, 100)
	if !hit || err != nil || !bytes.Equal(part.Bytes(), payload[17:117]) {
		t.Fatalf("persisted range hit=%v err=%v", hit, err)
	}
	for _, otherFID := range []string{"2,0000000100000001", "1,0000000100000002"} {
		if hit, err := cache.CopyChunkRange(io.Discard, otherFID, int64(len(payload)), 0, 100); hit || err != nil {
			t.Fatalf("cross-volume/cookie cache hit for %s: %v %v", otherFID, hit, err)
		}
	}
	if hit, err := cache.CopyChunkRange(io.Discard, fid, int64(len(payload))+1, 0, 100); hit || err != nil {
		t.Fatalf("wrong-length entry accepted: %v %v", hit, err)
	}
	// Streaming reads can also consume entries from mount's legacy writer.
	const legacyFID = "1,0000000200000001"
	cache.SetChunk(legacyFID, payload)
	part.Reset()
	if hit, err := cache.CopyChunkRange(&part, legacyFID, int64(len(payload)), 0, int64(len(payload))); !hit || err != nil || !bytes.Equal(part.Bytes(), payload) {
		t.Fatalf("legacy entry hit=%v err=%v", hit, err)
	}
}

func TestStreamingChunkCacheDoesNotPublishFailedEntries(t *testing.T) {
	cache := NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	const fid = "1,0000000100000001"
	for _, data := range []string{"short", "much too long"} {
		if err := cache.SetChunkFromReader(fid, bytes.NewBufferString(data), 10); err == nil {
			t.Fatalf("invalid entry %q accepted", data)
		}
		if cache.IsInCache(fid, true) {
			t.Fatal("incomplete entry became visible")
		}
	}
	if err := cache.SetChunkFromReader(fid, bytes.NewBufferString("0123456789"), 10); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if hit, err := cache.CopyChunkRange(&got, fid, 10, 0, 10); !hit || err != nil || got.String() != "0123456789" {
		t.Fatalf("replacement hit=%v bytes=%q err=%v", hit, got.String(), err)
	}
}

func TestStreamingChunkCacheEviction(t *testing.T) {
	// Largest tier has two 8 MiB segments. Three 6 MiB chunks recycle
	// the oldest segment, using mount's existing FIFO eviction policy.
	cache := NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	payload := bytes.Repeat([]byte("x"), 6*1024*1024)
	if !cache.CanRetain(int64(len(payload))) || cache.CanRetain(9*1024*1024) {
		t.Fatal("CanRetain does not match segment capacity")
	}
	fids := []string{"1,0000000100000001", "1,0000000200000001", "1,0000000300000001"}
	for _, fid := range fids {
		if err := cache.SetChunkFromReader(fid, bytes.NewReader(payload), int64(len(payload))); err != nil {
			t.Fatal(err)
		}
	}
	if hit, err := cache.CopyChunkRange(io.Discard, fids[0], int64(len(payload)), 0, 1); hit || err != nil {
		t.Fatalf("oldest entry not evicted: %v %v", hit, err)
	}
	if hit, err := cache.CopyChunkRange(io.Discard, fids[2], int64(len(payload)), 0, int64(len(payload))); !hit || err != nil {
		t.Fatalf("latest entry missing: %v %v", hit, err)
	}
}

func TestStreamingChunkCacheRealignsPartialTailAfterRestart(t *testing.T) {
	dir := t.TempDir()
	cache := NewTieredChunkCache(16, dir, 32, 1024*1024)
	const first, second = "1,0000000100000001", "1,0000000200000001"
	if err := cache.SetChunkFromReader(first, bytes.NewBufferString("first"), 5); err != nil {
		t.Fatal(err)
	}
	cache.Shutdown()
	files, err := filepath.Glob(filepath.Join(dir, "*.dat"))
	if err != nil || len(files) == 0 {
		t.Fatalf("cache files %v: %v", files, err)
	}
	for _, name := range files {
		if info, err := os.Stat(name); err == nil && info.Size() > 0 {
			f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write([]byte("tail"))
			f.Close()
		}
	}
	cache = NewTieredChunkCache(16, dir, 32, 1024*1024)
	defer cache.Shutdown()
	if err := cache.SetChunkFromReader(second, bytes.NewBufferString("second"), 6); err != nil {
		t.Fatal(err)
	}
	for fid, want := range map[string]string{first: "first", second: "second"} {
		var got bytes.Buffer
		if hit, err := cache.CopyChunkRange(&got, fid, int64(len(want)), 0, int64(len(want))); !hit || err != nil || got.String() != want {
			t.Fatalf("%s hit=%v bytes=%q err=%v", fid, hit, got.String(), err)
		}
	}
}

func TestDiskCacheAvailableUsesLayerSegmentCount(t *testing.T) {
	dir := t.TempDir()
	cache := NewTieredChunkCache(16, dir, 32, 1024*1024)
	defer cache.Shutdown()
	if !cache.DiskCacheAvailable() {
		t.Fatal("default cache unavailable")
	}
	// Large capacities split a layer into more than its minimum segment count.
	layer := cache.diskCaches[2]
	layer.expectedCount = 3
	if cache.DiskCacheAvailable() {
		t.Fatal("missing segment not detected")
	}
	extra, err := LoadOrCreateChunkCacheVolume(filepath.Join(dir, "c2_2_2"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	layer.diskCaches = append(layer.diskCaches, extra)
	if !cache.DiskCacheAvailable() {
		t.Fatal("cache with extra segments rejected")
	}
}

func TestStreamingChunkCacheRepairsWrongLengthEntry(t *testing.T) {
	cache := NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	const fid = "1,0000000100000001"
	cache.SetChunk(fid, []byte("short"))
	if hit, err := cache.CopyChunkRange(io.Discard, fid, 10, 0, 10); hit || err != nil {
		t.Fatalf("wrong-length entry accepted: %v %v", hit, err)
	}
	if err := cache.SetChunkFromReader(fid, bytes.NewBufferString("0123456789"), 10); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if hit, err := cache.CopyChunkRange(&got, fid, 10, 0, 10); !hit || err != nil || got.String() != "0123456789" {
		t.Fatalf("repaired entry hit=%v bytes=%q err=%v", hit, got.String(), err)
	}
}
