package filer

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/stats"
	"github.com/seaweedfs/seaweedfs/weed/util"
	"github.com/seaweedfs/seaweedfs/weed/util/mem"
	"github.com/seaweedfs/seaweedfs/weed/wdclient"
)

type chunkSpoolResult struct {
	chunkView *ChunkView
	spool     *chunkSpool
	skip      int64 // view offset within the spool when a whole chunk was filled
	fetchErr  error
	done      chan struct{}
}

// streamChunksSpooled fetches complete, validated chunks before publishing
// bytes to the response. Outstanding slots include completed but unconsumed
// spools, so slow clients cannot turn a four-chunk window into whole-file
// caching. cache may be nil.
func streamChunksSpooled(
	ctx context.Context,
	writer io.Writer,
	chunkViews *IntervalList[*ChunkView],
	fileId2Url map[string][]string,
	jwtFunc VolumeServerJwtFunction,
	masterClient wdclient.HasLookupFileIdFunction,
	offset int64,
	size int64,
	downloadMaxBytesPs int64,
	prefetchAhead int,
	cache *DownloadCache,
) error {
	localCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	invalidator, _ := masterClient.(CacheInvalidator)
	results := make(chan *chunkSpoolResult, prefetchAhead)
	slots := make(chan struct{}, prefetchAhead)
	producerDone := make(chan struct{})

	go func() {
		defer close(producerDone)
		defer close(results)
		for x := chunkViews.Front(); x != nil; x = x.Next {
			select {
			case slots <- struct{}{}:
			case <-localCtx.Done():
				return
			}
			cv := x.Value
			// Reserve in file order before launching: a later chunk must not
			// hold budget that the first chunk needs in order to run.
			spool, fill, err := cache.newChunkSpool(localCtx, cv)
			result := &chunkSpoolResult{chunkView: cv, spool: spool, done: make(chan struct{})}
			if err != nil {
				result.fetchErr = err
				close(result.done)
			} else {
				go func() {
					defer close(result.done)
					result.fetchErr = cache.fetchChunkSpool(localCtx, result, fill, fileId2Url[cv.FileId], jwtFunc, masterClient, invalidator)
				}()
			}
			select {
			case results <- result:
			case <-localCtx.Done():
				<-result.done
				if spool != nil {
					spool.close()
				}
				<-slots
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// Cancel before joining fetchers, including when the HTTP handler panics.
	defer func() {
		cancel()
		for result := range results {
			<-result.done
			if result.spool != nil {
				result.spool.close()
			}
			<-slots
		}
		<-producerDone
	}()

	copyBuf := mem.Allocate(256 * 1024)
	defer mem.Free(copyBuf)
	throttler := util.NewWriteThrottler(downloadMaxBytesPs)
	remaining := size
	for result := range results {
		err := func() error {
			defer func() {
				<-result.done
				if result.spool != nil {
					result.spool.close()
				}
				<-slots
			}()
			start := time.Now()
			select {
			case <-result.done:
			case <-localCtx.Done():
				return localCtx.Err()
			}
			if result.fetchErr != nil {
				stats.FilerHandlerCounter.WithLabelValues("chunkDownloadError").Inc()
				return fmt.Errorf("read chunk: %w", result.fetchErr)
			}
			reader, err := result.spool.section(result.skip, int64(result.chunkView.ViewSize))
			if err != nil {
				return err
			}
			cv := result.chunkView
			if offset < cv.ViewOffset {
				gap := cv.ViewOffset - offset
				if err := writeZero(writer, gap); err != nil {
					return err
				}
				remaining -= gap
				offset = cv.ViewOffset
			}
			n, err := io.CopyBuffer(writer, reader, copyBuf)
			if err != nil {
				return fmt.Errorf("write chunk: %w", err)
			}
			if n != int64(cv.ViewSize) {
				return fmt.Errorf("read spool: received %d of %d bytes: %w", n, cv.ViewSize, io.ErrUnexpectedEOF)
			}
			offset += n
			remaining -= n
			stats.FilerRequestHistogram.WithLabelValues("chunkDownload").Observe(time.Since(start).Seconds())
			stats.FilerHandlerCounter.WithLabelValues("chunkDownload").Inc()
			throttler.MaybeSlowdown(n)
			return nil
		}()
		if err != nil {
			return err
		}
	}
	if err := localCtx.Err(); err != nil {
		return err
	}
	if remaining > 0 {
		return writeZero(writer, remaining)
	}
	return nil
}
