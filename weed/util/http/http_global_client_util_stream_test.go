package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/util"
)

func TestReadUrlAsStreamValidatesDecodedLength(t *testing.T) {
	InitGlobalHttpClient()
	for _, tc := range []struct {
		name string
		body string
		gzip bool
		want string // empty when the read must fail
	}{
		{name: "short", body: "abc"},
		// a needle outlives a mount truncate of its chunk
		{name: "oversized", body: "abcdefghijk", want: "abcdefghij"},
		{name: "gzip short", body: "abc", gzip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.gzip {
					w.Header().Set("Content-Encoding", "gzip")
					gz := gzip.NewWriter(w)
					_, _ = gz.Write([]byte(tc.body))
					_ = gz.Close()
				} else {
					_, _ = io.WriteString(w, tc.body)
				}
			}))
			defer srv.Close()
			var got bytes.Buffer
			retry, err := ReadUrlAsStreamWithError(t.Context(), srv.URL, "", nil, tc.gzip, true, 0, 10, func(p []byte) error {
				_, err := got.Write(p)
				return err
			})
			if tc.want != "" {
				if err != nil || got.String() != tc.want {
					t.Fatalf("got=%q err=%v, want %q", got.String(), err, tc.want)
				}
			} else if !retry || err == nil {
				t.Fatalf("retry=%v err=%v, want retryable length failure", retry, err)
			}
		})
	}
}

func TestReadUrlAsStreamStopsOnDestinationError(t *testing.T) {
	InitGlobalHttpClient()
	peerCancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "abc")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(peerCancelled)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	want := errors.New("destination failed")
	retry, err := ReadUrlAsStreamWithError(ctx, srv.URL, "", nil, false, true, 0, 1000, func([]byte) error { return want })
	if retry || !errors.Is(err, want) {
		t.Fatalf("retry=%v err=%v, want original nonretryable destination error", retry, err)
	}
	select {
	case <-peerCancelled:
	case <-ctx.Done():
		t.Fatal("upstream was drained instead of closed on destination failure")
	}
}

func TestReadUrlAsStreamEncryptedDecodedLength(t *testing.T) {
	InitGlobalHttpClient()
	key := util.GenCipherKey()
	for _, compressed := range []bool{false, true} {
		for _, body := range []string{"short", "0123456789", "too many decoded bytes"} {
			data := []byte(body)
			if compressed {
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				_, _ = gz.Write(data)
				_ = gz.Close()
				data = buf.Bytes()
			}
			encoded, err := util.Encrypt(data, key)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(encoded)
			}))
			var got bytes.Buffer
			retry, err := ReadUrlAsStreamWithError(t.Context(), srv.URL, "", key, compressed, true, 0, 10, func(p []byte) error {
				_, err := got.Write(p)
				return err
			})
			srv.Close()
			if len(body) >= 10 {
				if err != nil || got.String() != body[:10] {
					t.Fatalf("compressed=%v body=%q retry=%v err=%v", compressed, got.String(), retry, err)
				}
			} else if !retry || err == nil {
				t.Fatalf("compressed=%v body=%q retry=%v err=%v: want retryable length error", compressed, body, retry, err)
			}
		}
	}
}
