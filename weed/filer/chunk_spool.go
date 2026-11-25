package filer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/sync/semaphore"
)

const (
	chunkSpoolMemoryThreshold int64 = 16 << 20
	chunkSpoolMemoryLimit     int64 = 1 << 30
	chunkSpoolDiskLimit       int64 = 16 << 30
)

// ChunkSpoolBudget bounds staging shared by every download that uses it, not
// per download. Reserve before opening HTTP responses so budget pressure never
// idles an upstream connection. Chunks above the memory threshold are staged
// on disk from the first byte.
type ChunkSpoolBudget struct {
	memoryThreshold int64
	diskLimit       int64
	memory          *semaphore.Weighted
	disk            *semaphore.Weighted
}

// NewChunkSpoolBudget takes sizes in bytes; the threshold is capped at memoryLimit.
func NewChunkSpoolBudget(memoryThreshold, memoryLimit, diskLimit int64) *ChunkSpoolBudget {
	return &ChunkSpoolBudget{
		memoryThreshold: min(memoryThreshold, memoryLimit),
		diskLimit:       diskLimit,
		memory:          semaphore.NewWeighted(memoryLimit),
		disk:            semaphore.NewWeighted(diskLimit),
	}
}

var defaultChunkSpoolBudget = NewChunkSpoolBudget(chunkSpoolMemoryThreshold, chunkSpoolMemoryLimit, chunkSpoolDiskLimit)

// A spool is owned by a fetcher until done is closed, then by the consumer.
// Its slot and reservations stay held until consumption or cancellation.
type chunkSpool struct {
	buffer       []byte
	file         *os.File
	size         int64
	expected     int64
	capacity     int64
	memoryBudget int64
	diskBudget   int64
	dir          string
	budget       *ChunkSpoolBudget
}

func (b *ChunkSpoolBudget) newSpool(ctx context.Context, size int64, dir string) (*chunkSpool, error) {
	if size < 0 || (size > b.memoryThreshold && size > b.diskLimit) {
		return nil, fmt.Errorf("chunk size %d exceeds prefetch spool limit %d", size, b.diskLimit)
	}
	s := &chunkSpool{expected: size, capacity: size, dir: dir, budget: b}
	if size > b.memoryThreshold {
		s.diskBudget = size
	} else {
		s.memoryBudget = size
	}
	// A zero-weight Acquire still queues behind waiters for the other budget.
	if s.memoryBudget > 0 {
		if err := b.memory.Acquire(ctx, s.memoryBudget); err != nil {
			return nil, err
		}
	}
	if s.diskBudget > 0 {
		if err := b.disk.Acquire(ctx, s.diskBudget); err != nil {
			return nil, err
		}
	}
	s.buffer = make([]byte, 0, int(s.memoryBudget))
	return s, nil
}

func (s *chunkSpool) Write(p []byte) (int, error) {
	if int64(len(p)) > s.expected-s.size {
		return 0, fmt.Errorf("prefetch spool exceeds expected chunk size %d", s.expected)
	}
	if s.diskBudget == 0 {
		s.buffer = append(s.buffer, p...)
		s.size += int64(len(p))
		return len(p), nil
	}
	if s.file == nil {
		f, err := os.CreateTemp(s.dir, "seaweedfs-prefetch-*")
		if err != nil {
			return 0, err
		}
		s.file = f
	}
	n, err := s.file.Write(p)
	s.size += int64(n)
	return n, err
}

func (s *chunkSpool) reset() error {
	s.size = 0
	s.buffer = s.buffer[:0]
	if s.file != nil {
		if err := s.file.Truncate(0); err != nil {
			return err
		}
		_, err := s.file.Seek(0, io.SeekStart)
		return err
	}
	return nil
}

func (s *chunkSpool) reader() (io.Reader, error) {
	return s.section(0, s.expected)
}

// section returns part of a complete spool, such as a range within a filled chunk.
func (s *chunkSpool) section(offset, n int64) (io.Reader, error) {
	if s.size != s.expected {
		return nil, fmt.Errorf("prefetch received %d of %d bytes: %w", s.size, s.expected, io.ErrUnexpectedEOF)
	}
	if offset < 0 || n < 0 || n > s.size-offset {
		return nil, fmt.Errorf("spool section %d+%d outside %d bytes", offset, n, s.size)
	}
	if s.file != nil {
		if _, err := s.file.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		return io.LimitReader(s.file, n), nil
	}
	return bytes.NewReader(s.buffer[offset : offset+n]), nil
}

// setExpected changes what an empty spool will hold, within its reservation.
func (s *chunkSpool) setExpected(n int64) error {
	if n < 0 || n > s.capacity {
		return fmt.Errorf("spool size %d exceeds reservation %d", n, s.capacity)
	}
	if err := s.reset(); err != nil {
		return err
	}
	s.expected = n
	return nil
}

func (s *chunkSpool) close() {
	if s.file != nil {
		name := s.file.Name()
		s.file.Close()
		os.Remove(name)
	}
	s.buffer = nil
	if s.memoryBudget > 0 {
		s.budget.memory.Release(s.memoryBudget)
	}
	if s.diskBudget > 0 {
		s.budget.disk.Release(s.diskBudget)
	}
}
