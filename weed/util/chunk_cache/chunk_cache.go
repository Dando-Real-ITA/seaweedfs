package chunk_cache

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"

	"github.com/seaweedfs/seaweedfs/weed/glog"
	"github.com/seaweedfs/seaweedfs/weed/storage/needle"
	"github.com/seaweedfs/seaweedfs/weed/storage/types"
)

var ErrorOutOfBounds = errors.New("attempt to read out of bounds")

const cacheHeaderSize = 8 // 4 bytes volumeId + 4 bytes cookie

// parseCacheHeader extracts volume ID and cookie from the 8-byte cache header
func parseCacheHeader(header []byte) (needle.VolumeId, types.Cookie) {
	volumeId := needle.VolumeId(binary.BigEndian.Uint32(header[0:4]))
	cookie := types.BytesToCookie(header[4:8])
	return volumeId, cookie
}

type ChunkCache interface {
	ReadChunkAt(data []byte, fileId string, offset uint64) (n int, err error)
	SetChunk(fileId string, data []byte)
	IsInCache(fileId string, lockNeeded bool) (answer bool)
	GetMaxFilePartSizeInCache() (answer uint64)
}

// a global cache for recently accessed file chunks
type TieredChunkCache struct {
	memCache   *ChunkCacheInMemory
	diskCaches []*OnDiskCacheLayer
	sync.RWMutex
	onDiskCacheSizeLimit0  uint64
	onDiskCacheSizeLimit1  uint64
	onDiskCacheSizeLimit2  uint64
	maxFilePartSizeInCache uint64
	closed                 bool
}

var _ ChunkCache = &TieredChunkCache{}

func NewTieredChunkCache(maxEntries int64, dir string, diskSizeInUnit int64, unitSize int64) *TieredChunkCache {

	c := &TieredChunkCache{
		memCache: NewChunkCacheInMemory(maxEntries),
	}
	c.diskCaches = make([]*OnDiskCacheLayer, 3)
	c.onDiskCacheSizeLimit0 = uint64(unitSize)
	c.onDiskCacheSizeLimit1 = 4 * c.onDiskCacheSizeLimit0
	c.onDiskCacheSizeLimit2 = 2 * c.onDiskCacheSizeLimit1
	c.diskCaches[0] = NewOnDiskCacheLayer(dir, "c0_2", diskSizeInUnit*unitSize/8, 2)
	c.diskCaches[1] = NewOnDiskCacheLayer(dir, "c1_3", diskSizeInUnit*unitSize/4+diskSizeInUnit*unitSize/8, 3)
	c.diskCaches[2] = NewOnDiskCacheLayer(dir, "c2_2", diskSizeInUnit*unitSize/2, 2)
	c.maxFilePartSizeInCache = uint64(unitSize*diskSizeInUnit) / 4

	return c
}

func (c *TieredChunkCache) GetMaxFilePartSizeInCache() (answer uint64) {
	if c == nil {
		return 0
	}
	return c.maxFilePartSizeInCache
}

func (c *TieredChunkCache) IsInCache(fileId string, lockNeeded bool) (answer bool) {
	if c == nil {
		return false
	}

	if lockNeeded {
		c.RLock()
		defer c.RUnlock()
	}
	if c.closed {
		return false
	}

	item := c.memCache.cache.Get(fileId)
	if item != nil {
		glog.V(4).Infof("fileId %s is in memcache", fileId)
		return true
	}

	fid, err := needle.ParseFileIdFromString(fileId)
	if err != nil {
		glog.V(4).Infof("failed to parse file id %s", fileId)
		return false
	}

	// Check disk cache with volume ID and cookie validation
	for i, diskCacheLayer := range c.diskCaches {
		for k, v := range diskCacheLayer.diskCaches {
			if nv, ok := v.nm.Get(fid.Key); ok {
				// Read cache header to check volume ID and cookie
				headerBytes := make([]byte, cacheHeaderSize)
				if readN, readErr := v.DataBackend.ReadAt(headerBytes, nv.Offset.ToActualOffset()); readErr == nil && readN == cacheHeaderSize {
					// Parse volume ID and cookie from header
					storedVolumeId, storedCookie := parseCacheHeader(headerBytes)

					if storedVolumeId == fid.VolumeId && storedCookie == fid.Cookie {
						glog.V(4).Infof("fileId %s is in diskCaches[%d].volume[%d]", fileId, i, k)
						return true
					}
					glog.V(4).Infof("fileId %s header mismatch in diskCaches[%d].volume[%d]: stored volume %d cookie %x, expected volume %d cookie %x",
						fileId, i, k, storedVolumeId, storedCookie, fid.VolumeId, fid.Cookie)
				}
			}
		}
	}
	return false
}

func (c *TieredChunkCache) ReadChunkAt(data []byte, fileId string, offset uint64) (n int, err error) {
	if c == nil {
		return 0, nil
	}

	minSize := offset + uint64(len(data))
	if minSize <= c.onDiskCacheSizeLimit0 {
		n, err = c.memCache.readChunkAt(data, fileId, offset)
		if err != nil {
			glog.Errorf("failed to read from memcache: %s", err)
		}
		if n == int(len(data)) {
			return n, nil
		}
	}

	fid, err := needle.ParseFileIdFromString(fileId)
	if err != nil {
		glog.Errorf("failed to parse file id %s", fileId)
		return 0, nil
	}

	// Try disk caches with volume ID and cookie validation
	return c.readDiskChunkAt(data, fid, offset)

}

func (c *TieredChunkCache) SetChunk(fileId string, data []byte) {
	if c == nil {
		return
	}

	glog.V(4).Infof("SetChunk %s size %d\n", fileId, len(data))
	if len(data) <= int(c.onDiskCacheSizeLimit0) {
		c.memCache.SetChunk(fileId, data)
	}
	if err := c.SetChunkFromReader(fileId, bytes.NewReader(data), int64(len(data))); err != nil {
		glog.V(0).Infof("cache write %s size %d: %v", fileId, len(data), err)
	}
}

func (c *TieredChunkCache) Shutdown() {
	if c == nil {
		return
	}
	c.Lock()
	defer c.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for _, diskCache := range c.diskCaches {
		diskCache.shutdown()
	}
}

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}
