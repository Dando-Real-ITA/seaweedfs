package http

import (
	"context"
	"fmt"
	"io"
)

// ReadUrlAsStreamWithError stops reading immediately if the destination fails.
// A positive size is the expected decoded length, not the HTTP wire length.
// Length failures are retryable; callback failures are not.
func ReadUrlAsStreamWithError(ctx context.Context, fileUrl, jwt string, cipherKey []byte, isContentGzipped bool, isFullChunk bool, offset int64, size int, fn func(data []byte) error) (retryable bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var received int64
	var lengthFailure bool
	var failErr error
	retryable, err = ReadUrlAsStream(ctx, fileUrl, jwt, cipherKey, isContentGzipped, isFullChunk, offset, size, func(data []byte) {
		if failErr != nil {
			return
		}
		if size > 0 && received+int64(len(data)) > int64(size) {
			lengthFailure = true
			failErr = fmt.Errorf("%s: chunk exceeds expected size %d", fileUrl, size)
		} else {
			received += int64(len(data))
			failErr = fn(data)
		}
		if failErr != nil {
			// ReadUrlAsStream checks the context before every read.
			cancel()
		}
	})
	if failErr != nil {
		return lengthFailure, failErr
	}
	if err == nil && size > 0 && received != int64(size) {
		return true, fmt.Errorf("%s: received %d of %d bytes: %w", fileUrl, received, size, io.ErrUnexpectedEOF)
	}
	return retryable, err
}
