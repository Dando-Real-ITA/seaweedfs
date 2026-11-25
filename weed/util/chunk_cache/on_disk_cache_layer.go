package chunk_cache

import (
	"fmt"
	"path"
	"slices"

	"github.com/seaweedfs/seaweedfs/weed/glog"
)

type OnDiskCacheLayer struct {
	diskCaches    []*ChunkCacheVolume
	expectedCount int
}

func NewOnDiskCacheLayer(dir, namePrefix string, diskSize int64, segmentCount int) *OnDiskCacheLayer {

	volumeCount, volumeSize := int(diskSize/(30000*1024*1024)), int64(30000*1024*1024)
	if volumeCount < segmentCount {
		volumeCount, volumeSize = segmentCount, diskSize/int64(segmentCount)
	}

	c := &OnDiskCacheLayer{expectedCount: volumeCount}
	for i := 0; i < volumeCount; i++ {
		fileName := path.Join(dir, fmt.Sprintf("%s_%d", namePrefix, i))
		diskCache, err := LoadOrCreateChunkCacheVolume(fileName, volumeSize)
		if err != nil {
			glog.Errorf("failed to add cache %s : %v", fileName, err)
		} else {
			c.diskCaches = append(c.diskCaches, diskCache)
		}
	}

	// keep newest cache to the front
	slices.SortFunc(c.diskCaches, func(a, b *ChunkCacheVolume) int {
		return b.lastModTime.Compare(a.lastModTime)
	})
	return c
}

func (c *OnDiskCacheLayer) shutdown() {

	for _, diskCache := range c.diskCaches {
		diskCache.Shutdown()
	}

}
