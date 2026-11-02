package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spine.db")
	s, err := Open(context.Background(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func TestOpenIsWALAndExclusive(t *testing.T) {
	ctx := context.Background()
	s, p := open(t)
	if mode, err := s.JournalMode(ctx); err != nil || mode != "wal" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	if _, err := Open(ctx, p, Options{LockWait: 100 * time.Millisecond}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: %v, want ErrLocked", err)
	}
	s.Close()
	s2, err := Open(ctx, p, Options{})
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	s2.Close()
}

func TestNotFoundIsDistinct(t *testing.T) {
	s, _ := open(t)
	if _, err := s.GetLease(context.Background(), "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestLeaseOpSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	l := Lease{TaskID: "t1", Attempt: 1, Path: "/x/t1-a1", Branch: "spine/t1/1", BaseOID: "abc", State: LeaseRequested, OwnerPID: 1}
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatal(err)
	}
	l.State, l.AttemptID = LeaseReady, "id"
	if err := s.PutLease(ctx, l); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLease(ctx, "t1", 1)
	if err != nil || got != l {
		t.Fatalf("got %+v err %v", got, err)
	}
	if ready, _ := s.ListLeases(ctx, LeaseReady); len(ready) != 1 {
		t.Fatalf("ready=%v", ready)
	}

	seq, err := s.NextSnapshotSeq(ctx, "t1")
	if err != nil || seq != 1 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
	op, err := s.BeginOp(ctx, "t1", "snapshot", map[string]any{"seq": seq, "ref": "refs/spine/snap/t1/1"})
	if err != nil {
		t.Fatal(err)
	}
	// A pending intent reserves its seq.
	if next, _ := s.NextSnapshotSeq(ctx, "t1"); next != 2 {
		t.Fatalf("next=%d", next)
	}
	if err := s.CompleteSnapshot(ctx, op, Snapshot{TaskID: "t1", Seq: 1, Ref: "refs/spine/snap/t1/1", CommitOID: "c", TreeOID: "tr", Label: "x"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PendingOps(ctx); len(p) != 0 {
		t.Fatalf("pending=%v", p)
	}
	if sn, _ := s.ListSnapshots(ctx, "t1"); len(sn) != 1 || sn[0].CommitOID != "c" {
		t.Fatalf("snaps=%v", sn)
	}
}
