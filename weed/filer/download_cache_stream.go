package filer

import (
	"context"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/glog"
	"github.com/seaweedfs/seaweedfs/weed/pb/filer_pb"
	"github.com/seaweedfs/seaweedfs/weed/wdclient"
)

// prepareCachedStreamContent resolves manifests through the download cache and
// looks up volume locations only for chunks it does not retain.
func prepareCachedStreamContent(ctx context.Context, masterClient wdclient.HasLookupFileIdFunction, jwtFunc VolumeServerJwtFunction, chunks []*filer_pb.FileChunk, offset int64, size int64, downloadMaxBytesPs int64, prefetchAhead int, cache *DownloadCache) (DoStreamContent, error) {
	glog.V(4).InfofCtx(ctx, "prepare to stream content with prefetch=%d for chunks: %d", prefetchAhead, len(chunks))
	resolved, _, err := resolveChunkManifestCached(ctx, masterClient.GetLookupFileIdFunction(), chunks, offset, offset+size, cache.Manifests)
	if err != nil {
		return nil, err
	}
	chunkViews := ViewFromVisibleIntervals(readResolvedChunks(resolved, 0, math.MaxInt64), offset, size)

	fileId2Url := make(map[string][]string)
	for x := chunkViews.Front(); x != nil; x = x.Next {
		chunkView := x.Value
		// Retained chunks are served without volume locations; a fetcher that
		// finds one evicted meanwhile looks it up then.
		if len(chunkView.CipherKey) == 0 && cache.Cache.HasChunk(chunkView.FileId, int64(chunkView.ChunkSize)) {
			continue
		}
		urlStrings, err := lookupChunkUrls(ctx, masterClient, chunkView.FileId)
		if err != nil {
			return nil, err
		}
		fileId2Url[chunkView.FileId] = urlStrings
	}

	return func(writer io.Writer) error {
		return streamChunksSpooled(ctx, writer, chunkViews, fileId2Url, jwtFunc, masterClient, offset, size, downloadMaxBytesPs, prefetchAhead, cache)
	}, nil
}

// resolveChunkManifestCached is ResolveChunkManifest backed by a manifest cache, which may be nil.
func resolveChunkManifestCached(ctx context.Context, lookupFileIdFn wdclient.LookupFileIdFunctionType, chunks []*filer_pb.FileChunk, startOffset, stopOffset int64, cache *ChunkManifestCache) (dataChunks, manifestChunks []*filer_pb.FileChunk, manifestResolveErr error) {
	resolver := newChunkManifestResolver(ctx, lookupFileIdFn, nil, nil)
	resolver.manifestCache = cache
	defer resolver.close()
	return resolver.resolve(chunks, startOffset, stopOffset)
}

func lookupChunkUrls(ctx context.Context, masterClient wdclient.HasLookupFileIdFunction, fileId string) ([]string, error) {
	var urlStrings []string
	var err error
	for _, backoff := range getLookupFileIdBackoffSchedule {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		urlStrings, err = masterClient.GetLookupFileIdFunction()(ctx, fileId)
		if err == nil && len(urlStrings) > 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		glog.V(4).InfofCtx(ctx, "waiting for chunk: %s", fileId)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		glog.V(1).InfofCtx(ctx, "operation LookupFileId %s failed, err: %v", fileId, err)
		return nil, err
	} else if len(urlStrings) == 0 {
		errUrlNotFound := fmt.Errorf("operation LookupFileId %s failed, err: urls not found", fileId)
		glog.ErrorCtx(ctx, errUrlNotFound)
		return nil, errUrlNotFound
	}
	return urlStrings, nil
}

// newChunkSpool reserves staging for cv; fill reports that the whole chunk is
// fetched so it can be retained. c may be nil.
func (c *DownloadCache) newChunkSpool(ctx context.Context, cv *ChunkView) (spool *chunkSpool, fill bool, err error) {
	budget, dir := defaultChunkSpoolBudget, ""
	if c != nil {
		dir = c.SpoolDir
		if c.SpoolBudget != nil {
			budget = c.SpoolBudget
		}
	}
	// Ranges fill the whole chunk so later requests can reuse it.
	fill = c.cacheable(cv) && c.Cache.CanRetain(int64(cv.ChunkSize))
	spoolSize := int64(cv.ViewSize)
	if fill {
		spoolSize = int64(cv.ChunkSize)
	}
	spool, err = budget.newSpool(ctx, spoolSize, dir)
	return spool, fill, err
}

func (c *DownloadCache) cacheable(cv *ChunkView) bool {
	return c != nil && c.Cache != nil && len(cv.CipherKey) == 0
}

// fetchChunkSpool fills result.spool from the cache or, on a miss, from volume
// servers, retaining a filled chunk. c may be nil.
func (c *DownloadCache) fetchChunkSpool(ctx context.Context, result *chunkSpoolResult, fill bool, urls []string, jwtFunc VolumeServerJwtFunction, masterClient wdclient.HasLookupFileIdFunction, invalidator CacheInvalidator) error {
	cv, spool := result.chunkView, result.spool
	if c.cacheable(cv) {
		release, err := c.acquire(ctx, cv.FileId, fill)
		if err != nil {
			return err
		}
		defer release()
		if err := spool.setExpected(int64(cv.ViewSize)); err != nil {
			return err
		}
		hit, err := c.Cache.CopyChunkRange(spool, cv.FileId, int64(cv.ChunkSize), cv.OffsetInChunk, int64(cv.ViewSize))
		if hit && err == nil {
			return nil
		}
	}
	fetchOffset, fetchSize := cv.OffsetInChunk, int64(cv.ViewSize)
	if fill {
		fetchOffset, fetchSize = 0, int64(cv.ChunkSize)
		result.skip = cv.OffsetInChunk
	}
	// A failed local copy must never leave a prefix in the fetch.
	if err := spool.setExpected(fetchSize); err != nil {
		return err
	}
	if len(urls) == 0 {
		var err error
		if urls, err = lookupChunkUrls(ctx, masterClient, cv.FileId); err != nil {
			return err
		}
	}
	_, err := retriedStreamFetchChunkData(ctx, spool, urls, jwtFunc(cv.FileId),
		cv.CipherKey, cv.IsGzipped, fill || cv.IsFullChunk(), fetchOffset, int(fetchSize),
		refreshUrls(ctx, invalidator, masterClient.GetLookupFileIdFunction(), cv.FileId))
	if err != nil || !fill {
		return err
	}
	r, retainErr := spool.reader()
	if retainErr == nil {
		retainErr = c.Cache.SetChunkFromReader(cv.FileId, r, int64(cv.ChunkSize))
	}
	// Retention is best effort. A valid staged download can still be
	// delivered if the cache is full or unavailable.
	if retainErr != nil {
		glog.V(1).InfofCtx(ctx, "retain chunk %s in download cache: %v", cv.FileId, retainErr)
	}
	return nil
}
