package filer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/glog"
	"github.com/seaweedfs/seaweedfs/weed/util"
	util_http "github.com/seaweedfs/seaweedfs/weed/util/http"
)

// fetchChunkDataWithRetry retries upstream failures across replicas and, with
// backoff, the same volume. Buffered destinations discard partial attempts;
// live streams skip the prefix already published. Refresh locations at most
// once per call.
func fetchChunkDataWithRetry(ctx context.Context, writer io.Writer, urlStrings []string, jwt string, cipherKey []byte, isGzipped bool, isFullChunk bool, offset int64, size int, refreshUrls util_http.RefreshUrlsFunc) (written int64, err error) {

	var shouldRetry bool
	var totalWritten int
	var refreshed bool
	buffer, buffered := writer.(*bytes.Buffer)
	var initialBufferSize int
	if buffered {
		initialBufferSize = buffer.Len()
	}

	for waitTime := time.Second; ; waitTime += waitTime / 2 {
		// Check for context cancellation before starting retry loop
		select {
		case <-ctx.Done():
			return int64(totalWritten), ctx.Err()
		default:
		}

		var failed bool
		var retrySameVolume bool
		for _, urlString := range util_http.ReachableFirst(urlStrings) {
			// Check for context cancellation before each volume server request
			select {
			case <-ctx.Done():
				return int64(totalWritten), ctx.Err()
			default:
			}

			// A spool has not published any data yet. Discard failed attempts
			// instead of mixing their partial bytes with the next response.
			if spool, ok := writer.(*chunkSpool); ok {
				if resetErr := spool.reset(); resetErr != nil {
					return int64(totalWritten), &chunkWriteError{resetErr}
				}
				totalWritten = 0
			} else if buffered {
				// Manifest buffers must not retain an invalid partial prefix
				// when retrying against a repaired or relocated source.
				buffer.Truncate(initialBufferSize)
				totalWritten = 0
			}
			var localProcessed int
			var writeErr error
			shouldRetry, err = util_http.ReadUrlAsStreamWithError(ctx, util_http.AppendQueryParameter(urlString, "readDeleted", "true"), jwt, cipherKey, isGzipped, isFullChunk, offset, size, func(data []byte) error {
				// Check for context cancellation during data processing
				select {
				case <-ctx.Done():
					writeErr = ctx.Err()
					return writeErr
				default:
				}

				if totalWritten > localProcessed {
					toBeSkipped := totalWritten - localProcessed
					if len(data) <= toBeSkipped {
						localProcessed += len(data)
						return nil // skip if already processed
					}
					data = data[toBeSkipped:]
					localProcessed += toBeSkipped
				}
				var writtenCount int
				writtenCount, writeErr = writer.Write(data)
				localProcessed += writtenCount
				totalWritten += writtenCount
				if writeErr == nil && writtenCount != len(data) {
					writeErr = io.ErrShortWrite
				}
				return writeErr
			})
			if writeErr != nil {
				return int64(totalWritten), &chunkWriteError{writeErr}
			}
			if err == nil {
				if failed && refreshUrls != nil && !refreshed {
					refreshUrls()
				}
				return int64(totalWritten), nil
			}
			if !shouldRetry {
				return int64(totalWritten), err
			}
			failed = true
			// A missing chunk can exist on another replica/location, but
			// repeatedly asking an unchanged 404 source is not transient I/O.
			retrySameVolume = retrySameVolume || !errors.Is(err, util_http.ErrNotFound)
			glog.V(0).InfofCtx(ctx, "read %s failed, err: %v", urlString, err)
		}
		if len(urlStrings) == 0 {
			err = fmt.Errorf("no locations for chunk")
			break
		}
		if shouldRetry && refreshUrls != nil && !refreshed {
			refreshed = true
			if fresh := refreshUrls(); len(fresh) > 0 && !util_http.SameUrls(urlStrings, fresh) {
				urlStrings = fresh
				continue
			}
		}
		// Exhausting the replica list is not exhaustion of the retry budget:
		// transient failures must also retry when there is only one volume.
		if err != nil && shouldRetry && retrySameVolume && waitTime < util.RetryWaitTime {
			glog.V(0).InfofCtx(ctx, "retry reading in %v", waitTime)
			// Sleep with proper context cancellation and timer cleanup
			timer := time.NewTimer(waitTime)
			select {
			case <-ctx.Done():
				timer.Stop()
				return int64(totalWritten), ctx.Err()
			case <-timer.C:
				// Continue with retry
			}
		} else {
			break
		}
	}

	return int64(totalWritten), err

}

// chunkWriteError distinguishes a broken destination from a retryable upstream
// failure. Re-reading a replica cannot repair a disconnected client or full disk.
type chunkWriteError struct{ error }

func (e *chunkWriteError) Unwrap() error { return e.error }

func isChunkWriteError(err error) bool {
	var writeErr *chunkWriteError
	return errors.As(err, &writeErr)
}
