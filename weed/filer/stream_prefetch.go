package filer

import (
	"context"
	"io"

	"github.com/seaweedfs/seaweedfs/weed/wdclient"
)

// streamChunksPrefetched streams chunks with concurrent prefetch through
// bounded spools; see streamChunksSpooled.
func streamChunksPrefetched(
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
) error {
	return streamChunksSpooled(ctx, writer, chunkViews, fileId2Url, jwtFunc, masterClient, offset, size, downloadMaxBytesPs, prefetchAhead, nil)
}
