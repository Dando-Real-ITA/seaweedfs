package chunk_cache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/seaweedfs/seaweedfs/weed/glog"
	"github.com/seaweedfs/seaweedfs/weed/storage/needle"
	"github.com/seaweedfs/seaweedfs/weed/storage/types"
)

// SetChunkFromReader inserts a complete decoded chunk without allocating the
// whole chunk in RAM. The copy runs without the cache lock into a reserved
// range; the entry becomes visible only once complete.
func (c *TieredChunkCache) SetChunkFromReader(fileId string, r io.Reader, size int64) error {
	if c == nil {
		return nil
	}
	fid, err := needle.ParseFileIdFromString(fileId)
	if err != nil {
		return err
	}
	v, start, padded, err := c.reserve(fid, size)
	if err != nil || v == nil {
		return err
	}
	header := make([]byte, cacheHeaderSize)
	binary.BigEndian.PutUint32(header[:4], uint32(fid.VolumeId))
	types.CookieToBytes(header[4:], fid.Cookie)
	err = v.writeEntryAt(start, io.MultiReader(bytes.NewReader(header), r), size+cacheHeaderSize, padded)
	// Release before publishing so recycling never waits on a lock holder.
	v.release()
	return c.publish(v, fid.Key, start, size+cacheHeaderSize, padded, err)
}

// reserve returns a pinned segment and range for a new entry, or nil if the
// chunk is already retained.
func (c *TieredChunkCache) reserve(fid *needle.FileId, size int64) (*ChunkCacheVolume, int64, int64, error) {
	c.Lock()
	defer c.Unlock()
	layer, paddedSize, err := c.retainLayer(size)
	if err != nil {
		return nil, 0, 0, err
	}
	// Do not let a stale, differently-sized entry prevent repair after a miss.
	if _, _, _, ok := c.findEntry(fid, size, 0); ok {
		return nil, 0, 0, nil
	}
	if layer.diskCaches[0].fileSize+paddedSize > layer.diskCaches[0].sizeLimit {
		if err := layer.rotate(); err != nil {
			return nil, 0, 0, err
		}
	}
	v := layer.diskCaches[0]
	start := v.fileSize
	v.fileSize += paddedSize
	v.acquire()
	return v, start, paddedSize, nil
}

func (c *TieredChunkCache) publish(v *ChunkCacheVolume, key types.NeedleId, start, entrySize, padded int64, err error) error {
	c.Lock()
	defer c.Unlock()
	if err == nil && v.retired {
		err = fmt.Errorf("cache segment %s recycled during insert", v.fileName)
	}
	if err == nil {
		err = v.nm.Put(key, types.ToOffset(start), types.Size(entrySize))
	}
	if err != nil && !v.retired && v.fileSize == start+padded {
		v.fileSize = start
		_ = v.DataBackend.Truncate(start)
	}
	return err
}

// CanRetain reports whether SetChunkFromReader could store a chunk of this size.
func (c *TieredChunkCache) CanRetain(size int64) bool {
	if c == nil {
		return false
	}
	c.RLock()
	defer c.RUnlock()
	_, _, err := c.retainLayer(size)
	return err == nil
}

// retainLayer requires the caller to hold the cache lock.
func (c *TieredChunkCache) retainLayer(size int64) (*OnDiskCacheLayer, int64, error) {
	if c.closed {
		return nil, 0, fmt.Errorf("chunk cache closed")
	}
	if size < 0 || size > (1<<31-1)-cacheHeaderSize {
		return nil, 0, fmt.Errorf("chunk size %d cannot be indexed by the disk cache", size)
	}
	level := 2
	if uint64(size) <= c.onDiskCacheSizeLimit0 {
		level = 0
	} else if uint64(size) <= c.onDiskCacheSizeLimit1 {
		level = 1
	}
	layer := c.diskCaches[level]
	if len(layer.diskCaches) == 0 {
		return nil, 0, fmt.Errorf("chunk cache disk layer %d unavailable", level)
	}
	entrySize := size + cacheHeaderSize
	paddedSize := (entrySize + int64(types.NeedlePaddingSize) - 1) / int64(types.NeedlePaddingSize) * int64(types.NeedlePaddingSize)
	if paddedSize > layer.diskCaches[0].sizeLimit {
		return nil, 0, fmt.Errorf("chunk size %d exceeds cache segment capacity %d", size, layer.diskCaches[0].sizeLimit)
	}
	return layer, paddedSize, nil
}

// readDiskChunkAt reads a range of a validated disk entry with the segment
// pinned rather than the cache locked.
func (c *TieredChunkCache) readDiskChunkAt(data []byte, fid *needle.FileId, offset uint64) (int, error) {
	minSize := offset + uint64(len(data))
	minLevel := 2
	if minSize <= c.onDiskCacheSizeLimit0 {
		minLevel = 0
	} else if minSize <= c.onDiskCacheSizeLimit1 {
		minLevel = 1
	}
	v, start, entrySize, ok := c.acquireEntry(fid, -1, minLevel)
	if !ok {
		return 0, nil
	}
	defer v.release()
	if minSize > uint64(entrySize-cacheHeaderSize) {
		return 0, nil
	}
	readOffset := start + cacheHeaderSize + int64(offset)
	if n, err := v.DataBackend.ReadAt(data, readOffset); n != len(data) {
		glog.V(4).Infof("read %s from %s.dat at %d: %d bytes, %v", fid, v.fileName, readOffset, n, err)
		return 0, nil
	}
	v.dropReadCache(readOffset, int64(len(data)))
	return len(data), nil
}

// CopyChunkRange copies a cache hit to dst without holding the cache lock; the
// segment stays readable even if recycled meanwhile. A missing or
// differently-sized entry is a cache miss.
func (c *TieredChunkCache) CopyChunkRange(dst io.Writer, fileId string, chunkSize, offset, size int64) (bool, error) {
	if c == nil {
		return false, nil
	}
	if offset < 0 || size < 0 || offset > chunkSize || size > chunkSize-offset {
		return false, fmt.Errorf("invalid cached chunk range")
	}
	fid, err := needle.ParseFileIdFromString(fileId)
	if err != nil {
		return false, nil
	}
	v, start, _, ok := c.acquireEntry(fid, chunkSize, 0)
	if !ok {
		return false, nil
	}
	defer v.release()
	n, err := io.CopyN(dst, io.NewSectionReader(v.DataBackend, start+cacheHeaderSize+offset, size), size)
	if err != nil {
		return false, err
	}
	return n == size, nil
}

// HasChunk reports whether a complete chunk of chunkSize is retained.
func (c *TieredChunkCache) HasChunk(fileId string, chunkSize int64) bool {
	if c == nil {
		return false
	}
	fid, err := needle.ParseFileIdFromString(fileId)
	if err != nil {
		return false
	}
	c.RLock()
	defer c.RUnlock()
	if c.closed {
		return false
	}
	_, _, _, ok := c.findEntry(fid, chunkSize, 0)
	return ok
}

// acquireEntry pins the entry's segment so the caller can read it unlocked.
func (c *TieredChunkCache) acquireEntry(fid *needle.FileId, chunkSize int64, minLevel int) (*ChunkCacheVolume, int64, int64, bool) {
	c.RLock()
	defer c.RUnlock()
	if c.closed {
		return nil, 0, 0, false
	}
	v, start, entrySize, ok := c.findEntry(fid, chunkSize, minLevel)
	if ok {
		v.acquire()
	}
	return v, start, entrySize, ok
}

// findEntry requires the caller to hold the cache lock. A negative chunkSize
// matches any size.
func (c *TieredChunkCache) findEntry(fid *needle.FileId, chunkSize int64, minLevel int) (*ChunkCacheVolume, int64, int64, bool) {
	header := make([]byte, cacheHeaderSize)
	for _, layer := range c.diskCaches[minLevel:] {
		for _, v := range layer.diskCaches {
			nv, ok := v.nm.Get(fid.Key)
			if !ok || int64(nv.Size) < cacheHeaderSize || chunkSize >= 0 && int64(nv.Size) != chunkSize+cacheHeaderSize {
				continue
			}
			start := nv.Offset.ToActualOffset()
			if _, err := v.DataBackend.ReadAt(header, start); err != nil {
				continue
			}
			volume, cookie := parseCacheHeader(header)
			if volume == fid.VolumeId && cookie == fid.Cookie {
				return v, start, int64(nv.Size), true
			}
		}
	}
	return nil, 0, 0, false
}

// DiskCacheAvailable allows server startup to reject partially initialized
// caches rather than silently accepting an unwritable or already-open directory.
func (c *TieredChunkCache) DiskCacheAvailable() bool {
	if c == nil {
		return false
	}
	c.RLock()
	defer c.RUnlock()
	if c.closed {
		return false
	}
	for _, layer := range c.diskCaches {
		if len(layer.diskCaches) != layer.expectedCount {
			return false
		}
	}
	return true
}

// rotate recycles the oldest segment as the newest; the caller holds the cache write lock.
func (c *OnDiskCacheLayer) rotate() error {
	last := len(c.diskCaches) - 1
	old := c.diskCaches[last]
	t, err := old.Reset()
	if err != nil {
		// The old segment is already retired and must not be indexed again.
		c.diskCaches = c.diskCaches[:last]
		return fmt.Errorf("reset cache file %s: %w", old.fileName, err)
	}
	copy(c.diskCaches[1:], c.diskCaches[:last])
	c.diskCaches[0] = t
	return nil
}

func (v *ChunkCacheVolume) acquire() {
	v.refs.Add(1)
}

func (v *ChunkCacheVolume) release() {
	if v.refs.Add(-1) == 0 {
		v.DataBackend.Close()
		close(v.closed)
	}
}

// writeEntryAt fills a range reserved under the cache lock without holding it.
func (v *ChunkCacheVolume) writeEntryAt(start int64, r io.Reader, size, padded int64) error {
	buf := make([]byte, 256*1024)
	for offset := int64(0); offset < size; {
		want := len(buf)
		if int64(want) > size-offset {
			want = int(size - offset)
		}
		n, err := io.ReadFull(r, buf[:want])
		if err != nil {
			return err
		}
		written, err := v.DataBackend.WriteAt(buf[:n], start+offset)
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
		offset += int64(n)
	}
	var extra [1]byte
	if n, err := r.Read(extra[:]); n != 0 || err != io.EOF {
		return fmt.Errorf("cache entry exceeds expected size %d or did not finish: %v", size, err)
	}
	if padding := padded - size; padding > 0 {
		if n, err := v.DataBackend.WriteAt(v.smallBuffer[:padding], start+size); err != nil {
			return err
		} else if int64(n) != padding {
			return io.ErrShortWrite
		}
	}
	return nil
}
