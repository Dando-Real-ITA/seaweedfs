package filer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatedPrefetchWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gatedPrefetchWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

func TestPrefetchSlowConsumerKeepsFourSpoolWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	const chunkSize = 2 << 20
	var requests atomic.Int32
	completed := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Length", fmt.Sprint(chunkSize))
		_, _ = w.Write(make([]byte, chunkSize))
		completed <- struct{}{}
	}))
	defer srv.Close()
	chunks, master, _, cleanup := makeChunksAndServer(t, 8, chunkSize)
	cleanup()
	for fid := range master.urls {
		master.urls[fid] = []string{srv.URL + "/" + fid}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream, err := PrepareStreamContentWithPrefetch(ctx, master, noopJwt, chunks, 0, 8*chunkSize, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	dst := &gatedPrefetchWriter{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- stream(dst) }()
	// Always release the simulated client, including on assertion failure.
	defer func() {
		close(dst.release)
		cancel()
		<-done
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 0 {
			t.Errorf("temporary files leaked after cancellation: %v err=%v", files, err)
		}
	}()
	select {
	case <-dst.started:
	case <-ctx.Done():
		t.Fatal("first spool never reached consumer")
	}
	for i := 0; i < 4; i++ {
		select {
		case <-completed:
		case <-ctx.Done():
			t.Fatal("prefetch stopped reading upstream while client was blocked")
		}
	}
	select {
	case <-completed:
		t.Fatal("prefetch exceeded its outstanding four-chunk window")
	case <-time.After(30 * time.Millisecond):
	}
	if requests.Load() != 4 {
		t.Fatalf("requested %d chunks while first was unconsumed", requests.Load())
	}
}

func TestPrefetchDestinationFailureCleansSpools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	chunks, master, _, cleanup := makeChunksAndServer(t, 6, 2<<20)
	defer cleanup()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := PrepareStreamContentWithPrefetch(ctx, master, noopJwt, chunks, 0, 6*(2<<20), 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream(&failOnWrite{}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected destination error, got %v", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files leaked: %v err=%v", files, err)
	}
	if !defaultChunkSpoolBudget.disk.TryAcquire(chunkSpoolDiskLimit) {
		t.Fatal("disk reservations leaked")
	}
	defaultChunkSpoolBudget.disk.Release(chunkSpoolDiskLimit)
}
