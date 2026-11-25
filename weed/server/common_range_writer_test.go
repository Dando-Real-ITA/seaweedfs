package weed_server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seaweedfs/seaweedfs/weed/filer"
)

type flushFailResponseWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *flushFailResponseWriter) Write(p []byte) (int, error) {
	w.writes++
	return 0, errors.New("response socket failed")
}

func TestProcessRangeRequestFinalFlushFailureAborts(t *testing.T) {
	for _, rangeHeader := range []string{"", "bytes=0-9", "bytes=0-1,8-9"} {
		t.Run(rangeHeader, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/test.bin", nil)
			r.Header.Set("Range", rangeHeader)
			w := &flushFailResponseWriter{ResponseRecorder: httptest.NewRecorder()}
			defer func() {
				if got := recover(); got != http.ErrAbortHandler {
					t.Fatalf("panic=%v, want http.ErrAbortHandler", got)
				}
				if w.writes != 1 {
					t.Fatalf("flush retried failed socket %d times", w.writes)
				}
			}()
			_ = ProcessRangeRequest(r, w, 10, "application/octet-stream", func(offset, size int64) (filer.DoStreamContent, error) {
				return func(dst io.Writer) error {
					_, err := dst.Write(bytes.Repeat([]byte("x"), int(size)))
					return err
				}, nil
			})
		})
	}
}
