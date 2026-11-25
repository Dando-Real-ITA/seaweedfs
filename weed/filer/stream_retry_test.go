package filer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	util_http "github.com/seaweedfs/seaweedfs/weed/util/http"
)

func TestChunkFetchRetriesSingleVolume(t *testing.T) {
	util_http.InitGlobalHttpClient()
	for _, failure := range []string{"short", "truncated", "server error", "oversized"} {
		for _, spooled := range []bool{false, true} {
			if failure == "oversized" && !spooled {
				continue
			}
			name := failure
			if spooled {
				name += "/spooled"
			}
			t.Run(name, func(t *testing.T) {
				var attempts atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if attempts.Add(1) == 1 {
						switch failure {
						case "server error":
							http.Error(w, "temporary failure", http.StatusServiceUnavailable)
						case "oversized":
							_, _ = io.WriteString(w, "bad response longer than ten bytes")
						default:
							if failure == "truncated" {
								w.Header().Set("Content-Length", "10")
							}
							prefix := "012"
							if spooled {
								prefix = "BAD" // must be discarded, not mixed into the retry
							}
							_, _ = io.WriteString(w, prefix)
						}
						return
					}
					_, _ = io.WriteString(w, "0123456789")
				}))
				defer srv.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
				defer cancel()
				var output bytes.Buffer
				// Hide the buffer's reset capability to exercise the live-stream
				// prefix-skipping path as well as transactional spool retries.
				var dst io.Writer = struct{ io.Writer }{&output}
				var spool *chunkSpool
				if spooled {
					var err error
					spool, err = defaultChunkSpoolBudget.newSpool(ctx, 10, "")
					if err != nil {
						t.Fatal(err)
					}
					defer spool.close()
					dst = spool
				}
				n, err := retriedStreamFetchChunkData(ctx, dst, []string{srv.URL}, "", nil, false, true, 0, 10, nil)
				if err != nil || n != 10 || attempts.Load() != 2 {
					t.Fatalf("written=%d attempts=%d err=%v", n, attempts.Load(), err)
				}
				if spooled {
					r, err := spool.reader()
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(&output, r)
				}
				if output.String() != "0123456789" {
					t.Fatalf("duplicate or failed-attempt bytes: %q", output.String())
				}
			})
		}
	}
}

func TestChunkFetchCancellationDuringBackoff(t *testing.T) {
	util_http.InitGlobalHttpClient()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "temporary", http.StatusServiceUnavailable)
		cancel()
	}))
	defer srv.Close()
	_, err := retriedStreamFetchChunkData(ctx, io.Discard, []string{srv.URL}, "", nil, false, true, 0, 10, nil)
	if !errors.Is(err, context.Canceled) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestChunkFetchDestinationFailureDoesNotRetry(t *testing.T) {
	util_http.InitGlobalHttpClient()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer srv.Close()
	_, err := retriedStreamFetchChunkData(t.Context(), &failOnWrite{}, []string{srv.URL}, "", nil, false, true, 0, 10, nil)
	if !isChunkWriteError(err) || attempts.Load() != 1 {
		t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
	}
}

func TestChunkFetchRetriesReplicaBeforeBackoff(t *testing.T) {
	util_http.InitGlobalHttpClient()
	var first, second atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		_, _ = io.WriteString(w, "012")
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer good.Close()
	var output bytes.Buffer
	n, err := retriedStreamFetchChunkData(t.Context(), &output, []string{bad.URL, good.URL}, "", nil, false, true, 0, 10, nil)
	if err != nil || n != 10 || output.String() != "0123456789" || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("written=%d body=%q requests=%d/%d err=%v", n, output.String(), first.Load(), second.Load(), err)
	}
}
