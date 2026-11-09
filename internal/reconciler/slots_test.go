package reconciler

import (
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

func TestResumedTaskGetsTheNextSlot(t *testing.T) {
	c := &Controller{slots: map[api.TaskID]bool{}}
	c.Config.Parallel = 1
	if !c.acquireNewSlot("busy") {
		t.Fatal("a free slot")
	}
	if c.acquireResumeSlot("hinted") {
		t.Fatal("no slot is free yet")
	}
	c.releaseSlot("busy")
	if c.acquireNewSlot("pending") {
		t.Fatal("a task that has not started took the slot a resumed task is waiting for")
	}
	if !c.acquireResumeSlot("hinted") {
		t.Fatal("the resumed task gets the freed slot")
	}
	c.releaseSlot("hinted")
	if !c.acquireNewSlot("pending") {
		t.Fatal("with no resumed task waiting, new tasks take slots again")
	}

	c.releaseSlot("pending")
	c.acquireNewSlot("busy")
	c.acquireResumeSlot("cancelled")
	c.releaseSlot("busy")
	c.resuming["cancelled"] = time.Now().Add(-time.Minute)
	if !c.acquireNewSlot("pending") {
		t.Fatal("a resumed task that stopped asking must not hold new tasks back")
	}
}
