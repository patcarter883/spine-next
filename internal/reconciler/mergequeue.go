package reconciler

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/storage"
)

// mergeQueue lets one task at a time merge into each base ref, in the order
// they became ready. The head checks the merge result against the tip it
// will land on, so no other integration can invalidate that check and each
// task is gated on merge once.
type mergeQueue struct {
	mu    sync.Mutex
	order map[string][]api.TaskID
}

// mergeTurn adds t to its base's queue and reports whether it is at the head,
// and how many tasks are ahead of it. Heads that are no longer merging are
// dropped first.
func (c *Controller) mergeTurn(ctx context.Context, t *api.Task) (bool, int, error) {
	q := &c.merges
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.order == nil {
		q.order = map[string][]api.TaskID{}
	}
	ref := t.Spec.BaseRef
	if !slices.Contains(q.order[ref], t.ID) {
		q.order[ref] = append(q.order[ref], t.ID)
	}
	for len(q.order[ref]) > 0 && q.order[ref][0] != t.ID {
		head, err := c.Store.GetTask(ctx, q.order[ref][0])
		if errors.Is(err, storage.ErrNotFound) || (err == nil && (!merging(&head) || head.Spec.BaseRef != ref)) {
			q.order[ref] = q.order[ref][1:]
			continue
		}
		if err != nil {
			return false, 0, err
		}
		break
	}
	i := slices.Index(q.order[ref], t.ID)
	return i == 0, i, nil
}

// mergeDone removes t from its base's queue.
func (c *Controller) mergeDone(t *api.Task) {
	q := &c.merges
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.order == nil {
		return
	}
	ref := t.Spec.BaseRef
	q.order[ref] = slices.DeleteFunc(q.order[ref], func(id api.TaskID) bool { return id == t.ID })
}

// merging reports whether t is waiting to merge or checking its merge.
func merging(t *api.Task) bool {
	s := t.Status
	switch s.Phase {
	case api.PhaseVerified:
		return true
	case api.PhaseVerifying:
		if s.Op != nil {
			return s.Op.Kind == "gate" && s.Op.OnMerge
		}
		return s.LastGate != nil && s.LastGate.Passed
	}
	return false
}
