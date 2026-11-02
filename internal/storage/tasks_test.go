package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
)

func TestTaskLifecycleOwnershipAndCAS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec := api.Spec{Intent: "do it", BaseRef: "refs/heads/main"}
	tk, err := s.CreateTask(ctx, "t1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, "t1", spec); !errors.Is(err, ErrExists) {
		t.Fatalf("dup: %v", err)
	}
	if _, err := s.CreateTask(ctx, "t2", api.Spec{Intent: "x", BaseRef: "main"}); err == nil {
		t.Fatal("short base ref accepted")
	}
	// Controller may change phase; the gate may write GatePassed.
	tk, err = s.UpdateStatus(ctx, "t1", tk.ResourceVersion, api.SourceController, func(st *api.Status) error {
		st.Phase = api.PhaseProvisioned
		return nil
	})
	if err != nil || tk.ResourceVersion != 2 {
		t.Fatalf("%+v %v", tk, err)
	}
	if _, err := s.UpdateStatus(ctx, "t1", 1, api.SourceController, func(*api.Status) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale rv: %v", err)
	}
	// The agent (or a supervisor) may not claim the gate passed.
	_, err = s.UpdateStatus(ctx, "t1", 0, api.SourceAgent, func(st *api.Status) error {
		st.SetCondition(api.CondGatePassed, true, "Claimed", "", api.SourceAgent)
		return nil
	})
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner: %v", err)
	}
	// Integrated phase needs the Integrated condition.
	_, err = s.UpdateStatus(ctx, "t1", 0, api.SourceController, func(st *api.Status) error {
		st.Phase = api.PhaseIntegrated
		return nil
	})
	if !errors.Is(err, ErrPhaseEvidence) {
		t.Fatalf("evidence: %v", err)
	}
	if _, err := s.UpdateStatus(ctx, "t1", 0, api.SourceGate, func(st *api.Status) error {
		st.SetCondition(api.CondVerified, true, "AllGatesPassed", "", api.SourceGate)
		st.Phase = api.PhaseVerified
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.ListEvents(ctx, "t1", 0, 0)
	if err != nil || len(evs) != 3 || evs[0].Kind != "submitted" || evs[2].Kind != "transition" {
		t.Fatalf("events %v %v", evs, err)
	}
	// Read-only open works while the writer lock is held.
	if !IsLocked(path) {
		t.Fatal("lock not visible")
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ro.GetTask(ctx, "t1")
	if err != nil || got.Status.Phase != api.PhaseVerified {
		t.Fatalf("ro: %+v %v", got, err)
	}
	if _, err := ro.CreateTask(ctx, "t9", spec); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}
