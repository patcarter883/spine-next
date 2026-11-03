package supervisor

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/reconciler"
)

// Limits is a set of counting semaphores keyed by operation kind, shared by
// every repository's controller. Unknown kinds (and kinds with a size <= 0)
// are unbounded. Sizes can change while running (Resize): a smaller limit
// takes effect as running operations finish.
type Limits struct {
	mu      sync.Mutex
	max     map[string]int
	cur     map[string]int
	peak    map[string]int
	holders map[uint64]reconciler.Waiter
	waiting map[uint64]reconciler.Waiter
	next    uint64
	wake    chan struct{} // closed and replaced whenever a slot frees
}

// NewLimits creates limits.
func NewLimits(sizes map[string]int) *Limits {
	l := &Limits{max: map[string]int{}, cur: map[string]int{}, peak: map[string]int{},
		holders: map[uint64]reconciler.Waiter{}, waiting: map[uint64]reconciler.Waiter{}, wake: make(chan struct{})}
	for k, n := range sizes {
		l.max[k] = n
	}
	return l
}

func (l *Limits) broadcast() {
	close(l.wake)
	l.wake = make(chan struct{})
}

// Resize changes the size of a kind.
func (l *Limits) Resize(kind string, n int) {
	l.mu.Lock()
	l.max[kind] = n
	l.broadcast()
	l.mu.Unlock()
}

// Acquire implements reconciler.Limiter.
func (l *Limits) Acquire(ctx context.Context, kind string) (func(), error) {
	w, _ := reconciler.WaiterFrom(ctx)
	w.Kind = kind
	w.Since = time.Now().UTC()
	l.mu.Lock()
	l.next++
	id := l.next
	queued := false
	for {
		if n := l.max[kind]; n <= 0 || l.cur[kind] < n {
			break
		}
		if !queued {
			l.waiting[id] = w
			queued = true
		}
		wake := l.wake
		l.mu.Unlock()
		select {
		case <-wake:
			l.mu.Lock()
		case <-ctx.Done():
			l.mu.Lock()
			delete(l.waiting, id)
			l.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	delete(l.waiting, id)
	l.cur[kind]++
	if l.cur[kind] > l.peak[kind] {
		l.peak[kind] = l.cur[kind]
	}
	w.Since = time.Now().UTC()
	l.holders[id] = w
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.cur[kind]--
			delete(l.holders, id)
			l.broadcast()
			l.mu.Unlock()
		})
	}, nil
}

// Usage reports current and peak holders per kind.
func (l *Limits) Usage() (cur, peak map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, peak = map[string]int{}, map[string]int{}
	for k, v := range l.cur {
		cur[k] = v
	}
	for k, v := range l.peak {
		peak[k] = v
	}
	return cur, peak
}

// SlotState is a snapshot for the dashboard.
type SlotState struct {
	Max     map[string]int      `json:"max"`
	InUse   map[string]int      `json:"in_use"`
	Peak    map[string]int      `json:"peak"`
	Holders []reconciler.Waiter `json:"holders"`
	Waiting []reconciler.Waiter `json:"waiting"`
}

// State reports limits, holders and the queue (oldest first).
func (l *Limits) State() SlotState {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := SlotState{Max: map[string]int{}, InUse: map[string]int{}, Peak: map[string]int{}, Holders: []reconciler.Waiter{}, Waiting: []reconciler.Waiter{}}
	for k, v := range l.max {
		s.Max[k] = v
	}
	for k, v := range l.cur {
		s.InUse[k] = v
	}
	for k, v := range l.peak {
		s.Peak[k] = v
	}
	for _, w := range l.holders {
		s.Holders = append(s.Holders, w)
	}
	for _, w := range l.waiting {
		s.Waiting = append(s.Waiting, w)
	}
	sort.Slice(s.Holders, func(i, j int) bool { return s.Holders[i].Since.Before(s.Holders[j].Since) })
	sort.Slice(s.Waiting, func(i, j int) bool { return s.Waiting[i].Since.Before(s.Waiting[j].Since) })
	return s
}

// Limits exposes the supervisor's limiter (for status and tests).
func (s *Supervisor) Limits() *Limits { return s.limits }

type reconcilerWaiter = reconciler.Waiter
