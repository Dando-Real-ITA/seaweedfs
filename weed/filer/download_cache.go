package filer

import (
	"context"
	"sync"

	"github.com/seaweedfs/seaweedfs/weed/util/chunk_cache"
)

// DownloadCache reuses the same tiered disk-cache implementation as mount.
// Partial/failed downloads remain private staging data; only complete decoded
// chunks are inserted under their immutable file ID. Encrypted chunks are not
// retained so decrypted plaintext is never written to the cache at rest.
type DownloadCache struct {
	Cache       *chunk_cache.TieredChunkCache
	Manifests   *ChunkManifestCache
	SpoolDir    string
	SpoolBudget *ChunkSpoolBudget
	mu          sync.Mutex
	fills       map[string]chan struct{}
}

// acquire waits for any in-flight fill of fid, then, if lead is set, makes the
// caller the only filler until release. Unrelated file IDs never wait on each
// other, and a cancelled leader hands over to a waiter.
func (c *DownloadCache) acquire(ctx context.Context, fid string, lead bool) (func(), error) {
	for {
		c.mu.Lock()
		done, busy := c.fills[fid]
		if !busy {
			if !lead {
				c.mu.Unlock()
				return func() {}, nil
			}
			if c.fills == nil {
				c.fills = make(map[string]chan struct{})
			}
			done = make(chan struct{})
			c.fills[fid] = done
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				delete(c.fills, fid)
				c.mu.Unlock()
				close(done)
			}, nil
		}
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
