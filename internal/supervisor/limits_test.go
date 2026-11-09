package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/reconciler"
)

func TestLimitsQueueAndResize(t *testing.T) {
	l := NewLimits(map[string]int{"agent": 1})
	r1, _ := l.Acquire(reconciler.WithWaiter(context.Background(), reconciler.Waiter{Repo: "a", Task: "t1"}), "agent")
	got := make(chan func(), 1)
	go func() {
		r, _ := l.Acquire(reconciler.WithWaiter(context.Background(), reconciler.Waiter{Repo: "b", Task: "t2"}), "agent")
		got <- r
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(l.State().Waiting) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := l.State()
	if len(st.Waiting) != 1 || st.Waiting[0].Task != "t2" || len(st.Holders) != 1 || st.Holders[0].Repo != "a" {
		t.Fatalf("state %+v", st)
	}
	l.Resize("agent", 2) // a bigger limit admits the waiter now
	select {
	case r2 := <-got:
		r2()
	case <-time.After(2 * time.Second):
		t.Fatal("resize did not admit the waiter")
	}
	r1()
	ctx, cancel := context.WithCancel(context.Background())
	l.Resize("agent", 0) // unbounded
	if _, err := l.Acquire(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestLimitsServeUrgentFirst(t *testing.T) {
	l := NewLimits(map[string]int{"gate": 1})
	ctx := context.Background()
	hold, err := l.Acquire(ctx, "gate")
	if err != nil {
		t.Fatal(err)
	}
	order := make(chan string, 2)
	wait := func(name string, urgent bool) {
		release, err := l.Acquire(reconciler.WithWaiter(ctx, reconciler.Waiter{Task: api.TaskID(name), Urgent: urgent}), "gate")
		if err != nil {
			t.Error(err)
			return
		}
		order <- name
		time.Sleep(10 * time.Millisecond)
		release()
	}
	go wait("branch-check", false)
	for len(l.State().Waiting) < 1 {
		time.Sleep(time.Millisecond)
	}
	go wait("merge-check", true)
	for len(l.State().Waiting) < 2 {
		time.Sleep(time.Millisecond)
	}
	hold()
	if first, second := <-order, <-order; first != "merge-check" || second != "branch-check" {
		t.Fatalf("served %s then %s; the merge check must go first", first, second)
	}
}
