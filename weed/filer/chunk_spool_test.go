package filer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestChunkSpoolSpillResetAndCleanup(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	payload := bytes.Repeat([]byte("x"), int(chunkSpoolMemoryThreshold)+1)
	s, err := defaultChunkSpoolBudget.newSpool(t.Context(), int64(len(payload)), "")
	if err != nil {
		t.Fatal(err)
	}
	// Close even if an assertion fails, without releasing reservations twice.
	defer func() {
		if s != nil {
			s.close()
		}
	}()
	if _, err := s.Write(payload[:chunkSpoolMemoryThreshold]); err != nil {
		t.Fatal(err)
	}
	if s.file == nil || s.memoryBudget != 0 || cap(s.buffer) != 0 {
		t.Fatal("large spool used memory instead of disk")
	}
	if _, err := s.Write(payload[chunkSpoolMemoryThreshold:]); err != nil {
		t.Fatal(err)
	}
	name := s.file.Name()
	if err := s.reset(); err != nil {
		t.Fatal(err)
	}
	payload[0] = 'y'
	if _, err := s.Write(payload); err != nil {
		t.Fatal(err)
	}
	r, err := s.reader()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("reset spool mismatch: len=%d err=%v", len(got), err)
	}
	s.close()
	s = nil
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file not removed: %v", err)
	}
	if !defaultChunkSpoolBudget.memory.TryAcquire(chunkSpoolMemoryLimit) {
		t.Fatal("memory reservation leaked")
	}
	defaultChunkSpoolBudget.memory.Release(chunkSpoolMemoryLimit)
	if !defaultChunkSpoolBudget.disk.TryAcquire(chunkSpoolDiskLimit) {
		t.Fatal("disk reservation leaked")
	}
	defaultChunkSpoolBudget.disk.Release(chunkSpoolDiskLimit)
}

func TestChunkSpoolBudgetWaitCancels(t *testing.T) {
	if err := defaultChunkSpoolBudget.disk.Acquire(t.Context(), chunkSpoolDiskLimit); err != nil {
		t.Fatal(err)
	}
	defer defaultChunkSpoolBudget.disk.Release(chunkSpoolDiskLimit)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := defaultChunkSpoolBudget.newSpool(ctx, chunkSpoolMemoryThreshold+1, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("budget wait: %v", err)
	}
	if !defaultChunkSpoolBudget.memory.TryAcquire(chunkSpoolMemoryLimit) {
		t.Fatal("memory leaked while waiting for disk")
	}
	defaultChunkSpoolBudget.memory.Release(chunkSpoolMemoryLimit)
}

func TestChunkSpoolMemoryNotQueuedBehindDiskWaiters(t *testing.T) {
	if err := defaultChunkSpoolBudget.disk.Acquire(t.Context(), chunkSpoolDiskLimit); err != nil {
		t.Fatal(err)
	}
	defer defaultChunkSpoolBudget.disk.Release(chunkSpoolDiskLimit)
	waitCtx, cancelWait := context.WithCancel(t.Context())
	defer cancelWait()
	go func() { _, _ = defaultChunkSpoolBudget.newSpool(waitCtx, chunkSpoolMemoryThreshold+1, "") }()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	s, err := defaultChunkSpoolBudget.newSpool(ctx, 10, "")
	if err != nil {
		t.Fatalf("memory spool waited for disk budget: %v", err)
	}
	s.close()
}

func TestChunkSpoolConfiguredBudget(t *testing.T) {
	b := NewChunkSpoolBudget(32, 8, 16)
	s, err := b.newSpool(t.Context(), 9, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.diskBudget != 9 {
		t.Fatalf("threshold above memory limit was not capped: %+v", s)
	}
	s.close()
	if _, err := b.newSpool(t.Context(), 17, ""); err == nil {
		t.Fatal("chunk above disk limit accepted")
	}
	if !b.memory.TryAcquire(8) || !b.disk.TryAcquire(16) {
		t.Fatal("configured reservations leaked")
	}
}

func TestChunkSpoolRejectsInvalidLengths(t *testing.T) {
	s, err := defaultChunkSpoolBudget.newSpool(t.Context(), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	_, _ = s.Write([]byte("short"))
	if _, err := s.reader(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short spool accepted: %v", err)
	}
	if _, err := s.Write([]byte("too long")); err == nil {
		t.Fatal("oversized spool accepted")
	}
}
