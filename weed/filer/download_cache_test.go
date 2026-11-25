package filer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
	"github.com/seaweedfs/seaweedfs/weed/util/chunk_cache"
	util_http "github.com/seaweedfs/seaweedfs/weed/util/http"
)

func TestDownloadCacheReuseRangesAndConcurrentFetch(t *testing.T) {
	util_http.InitGlobalHttpClient()
	const fid = "1,0000000100000001"
	// Exercise on-disk staging both during the upstream fetch and cache hits.
	payload := bytes.Repeat([]byte("chunk"), int(chunkSpoolMemoryThreshold)/5+1)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	master := &testMasterClient{urls: map[string][]string{fid: {srv.URL + "/" + fid}}}
	chunks := []*filer_pb.FileChunk{{FileId: fid, Size: uint64(len(payload))}}
	cache := chunk_cache.NewTieredChunkCache(16, t.TempDir(), 128, 1024*1024)
	defer cache.Shutdown()
	options := &DownloadCache{Cache: cache, SpoolDir: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := PrepareStreamContentWithPrefetch(t.Context(), master, noopJwt, chunks, 0, int64(len(payload)), 0, 4, options)
			if err != nil {
				t.Error(err)
				return
			}
			var got bytes.Buffer
			if err := stream(&got); err != nil || !bytes.Equal(got.Bytes(), payload) {
				t.Errorf("download bytes=%d err=%v", got.Len(), err)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("concurrent downloads fetched volume %d times", requests.Load())
	}
	stream, err := PrepareStreamContentWithPrefetch(t.Context(), master, noopJwt, chunks, 19, 100, 0, 4, options)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := stream(&got); err != nil || !bytes.Equal(got.Bytes(), payload[19:119]) || requests.Load() != 1 {
		t.Fatalf("cached range bytes=%d requests=%d err=%v", got.Len(), requests.Load(), err)
	}
	if files, err := os.ReadDir(options.SpoolDir); err != nil || len(files) != 0 {
		t.Fatalf("staging files leaked: %v %v", files, err)
	}
}

func TestDownloadCacheRangeFillsWholeChunkAndSkipsLookupOnHit(t *testing.T) {
	util_http.InitGlobalHttpClient()
	const fid = "1,0000000100000001"
	payload := []byte("0123456789")
	var requests, rangeRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Range") != "" {
			rangeRequests.Add(1)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	cache := chunk_cache.NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	options := &DownloadCache{Cache: cache}
	chunks := []*filer_pb.FileChunk{{FileId: fid, Size: 10}}
	read := func(master *testMasterClient, offset, size int64) string {
		t.Helper()
		stream, err := PrepareStreamContentWithPrefetch(t.Context(), master, noopJwt, chunks, offset, size, 0, 4, options)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := stream(&got); err != nil {
			t.Fatal(err)
		}
		return got.String()
	}
	if got := read(&testMasterClient{urls: map[string][]string{fid: {srv.URL + "/" + fid}}}, 3, 4); got != "3456" {
		t.Fatalf("range=%q", got)
	}
	if !cache.HasChunk(fid, 10) || rangeRequests.Load() != 0 {
		t.Fatalf("range did not fill whole chunk: cached=%v rangeRequests=%d", cache.HasChunk(fid, 10), rangeRequests.Load())
	}
	// Volume locations are unknown: a retained chunk must not need them.
	if got := read(&testMasterClient{}, 7, 3); got != "789" || requests.Load() != 1 {
		t.Fatalf("cached range=%q requests=%d", got, requests.Load())
	}
}

func TestDownloadCacheMixedHitsAndMissInOneRange(t *testing.T) {
	util_http.InitGlobalHttpClient()
	fids := []string{"1,0000000100000001", "1,0000000200000001", "1,0000000300000001"}
	payloads := [][]byte{[]byte("abcdefghij"), []byte("klmnopqrst"), []byte("uvwxyz0123")}
	var mu sync.Mutex
	requests := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		mu.Lock()
		requests[path]++
		mu.Unlock()
		for i, fid := range fids {
			if fid == path {
				w.Header().Set("Content-Length", fmt.Sprint(len(payloads[i])))
				_, _ = w.Write(payloads[i])
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cache := chunk_cache.NewTieredChunkCache(16, t.TempDir(), 64, 1024*1024)
	defer cache.Shutdown()
	for _, i := range []int{0, 2} {
		if err := cache.SetChunkFromReader(fids[i], bytes.NewReader(payloads[i]), 10); err != nil {
			t.Fatal(err)
		}
	}
	master := &testMasterClient{urls: map[string][]string{}}
	chunks := make([]*filer_pb.FileChunk, len(fids))
	for i, fid := range fids {
		master.urls[fid] = []string{srv.URL + "/" + fid}
		chunks[i] = &filer_pb.FileChunk{FileId: fid, Offset: int64(i * 10), Size: 10, ModifiedTsNs: int64(i)}
	}
	stream, err := PrepareStreamContentWithPrefetch(t.Context(), master, noopJwt, chunks, 3, 21, 0, 4, &DownloadCache{Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := stream(&got); err != nil || got.String() != "defghijklmnopqrstuvwx" {
		t.Fatalf("mixed range=%q err=%v", got.String(), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[fids[1]] != 1 {
		t.Fatalf("volume requests=%v, want only %s once", requests, fids[1])
	}
	if !cache.HasChunk(fids[1], 10) {
		t.Fatal("fetched middle chunk was not retained")
	}
}

func TestDownloadCacheEvictedAfterPrepareLooksUpLazily(t *testing.T) {
	util_http.InitGlobalHttpClient()
	const fid = "1,0000000100000001"
	payload := []byte("0123456789")
	srv := createTestServer(map[string][]byte{fid: payload})
	defer srv.Close()
	cache := chunk_cache.NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	if err := cache.SetChunkFromReader(fid, bytes.NewReader(payload), 10); err != nil {
		t.Fatal(err)
	}
	master := &testMasterClient{urls: map[string][]string{fid: {srv.URL + "/" + fid}}}
	options := &DownloadCache{Cache: cache}
	stream, err := PrepareStreamContentWithPrefetch(t.Context(), master, noopJwt, []*filer_pb.FileChunk{{FileId: fid, Size: 10}}, 0, 10, 0, 4, options)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate eviction between prepare and fetch.
	options.Cache = chunk_cache.NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer options.Cache.Shutdown()
	var got bytes.Buffer
	if err := stream(&got); err != nil || got.String() != string(payload) {
		t.Fatalf("lazy lookup bytes=%q err=%v", got.String(), err)
	}
}

func TestDownloadCacheReusesManifests(t *testing.T) {
	util_http.InitGlobalHttpClient()
	const dataFid, manifestFid = "1,0100000001", "1,0200000001"
	payload := []byte("0123456789")
	var manifestBytes []byte
	manifest, err := mergeIntoManifest(func(r io.Reader, _ string, _ int64, _ int64, _ uint64) (*filer_pb.FileChunk, error) {
		var readErr error
		manifestBytes, readErr = io.ReadAll(r)
		return &filer_pb.FileChunk{FileId: manifestFid}, readErr
	}, []*filer_pb.FileChunk{{FileId: dataFid, Size: 10}})
	if err != nil {
		t.Fatal(err)
	}
	var manifestReads atomic.Int32
	data := map[string][]byte{dataFid: payload, manifestFid: manifestBytes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == manifestFid {
			manifestReads.Add(1)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data[path])))
		_, _ = w.Write(data[path])
	}))
	defer srv.Close()
	master := &testMasterClient{urls: map[string][]string{dataFid: {srv.URL + "/" + dataFid}, manifestFid: {srv.URL + "/" + manifestFid}}}
	cache := chunk_cache.NewTieredChunkCache(16, t.TempDir(), 32, 1024*1024)
	defer cache.Shutdown()
	options := &DownloadCache{Cache: cache, Manifests: NewChunkManifestCache(MaxMountChunkManifestCacheEntries, MaxMountChunkManifestCacheBytes)}
	for i := 0; i < 2; i++ {
		stream, err := PrepareStreamContentWithPrefetch(t.Context(), master, noopJwt, []*filer_pb.FileChunk{manifest}, 0, 10, 0, 4, options)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := stream(&got); err != nil || got.String() != string(payload) {
			t.Fatalf("manifest download bytes=%q err=%v", got.String(), err)
		}
	}
	if manifestReads.Load() != 1 {
		t.Fatalf("manifest fetched %d times", manifestReads.Load())
	}
}

func TestChunkSpoolUsesConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := defaultChunkSpoolBudget.newSpool(t.Context(), chunkSpoolMemoryThreshold+1, dir)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), int(chunkSpoolMemoryThreshold)+1)
	if _, err := s.Write(payload); err != nil {
		s.close()
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		s.close()
		t.Fatalf("staging files=%d err=%v", len(files), err)
	}
	s.close()
	files, err = os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("staging files leaked: %v %v", files, err)
	}
}

func TestDownloadCacheGateCancellation(t *testing.T) {
	c := &DownloadCache{}
	release, err := c.acquire(t.Context(), "fid", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.acquire(ctx, "fid", true); err != context.Canceled {
		t.Fatalf("gate cancellation: %v", err)
	}
}

func TestDownloadCacheFillGates(t *testing.T) {
	c := &DownloadCache{}
	releaseA, err := c.acquire(t.Context(), "a", true)
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated file IDs and non-filling readers with no fill in flight never wait.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, lead := range []bool{true, false} {
		release, err := c.acquire(ctx, "b", lead)
		if err != nil {
			t.Fatalf("unrelated lead=%v blocked: %v", lead, err)
		}
		release()
	}
	// Partial readers wait for an in-flight fill, then a filler takes over.
	acquired := make(chan bool, 2)
	for _, lead := range []bool{false, true} {
		go func() {
			release, err := c.acquire(t.Context(), "a", lead)
			if err == nil {
				release()
			}
			acquired <- err == nil
		}()
	}
	select {
	case <-acquired:
		t.Fatal("waiter did not wait for in-flight fill")
	case <-time.After(50 * time.Millisecond):
	}
	releaseA()
	for i := 0; i < 2; i++ {
		if !<-acquired {
			t.Fatal("waiter failed after fill completed")
		}
	}
	if len(c.fills) != 0 {
		t.Fatalf("fill bookkeeping leaked: %d", len(c.fills))
	}
}
