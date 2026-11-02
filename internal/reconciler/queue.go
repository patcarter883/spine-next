package reconciler

import (
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

// queue is a small equivalent of client-go's rate-limited workqueue (RFC
// §4.4): a key is never handed to two workers at once, a key re-added while
// in flight is coalesced and handed out again after Done, AddAfter delays,
// and AddRateLimited backs off exponentially per key until Forget.
type queue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	order      []api.TaskID
	dirty      map[api.TaskID]bool
	processing map[api.TaskID]bool
	failures   map[api.TaskID]int
	timers     map[api.TaskID]*time.Timer
	shutdown   bool
	base, max  time.Duration
}

func newQueue() *queue {
	q := &queue{dirty: map[api.TaskID]bool{}, processing: map[api.TaskID]bool{}, failures: map[api.TaskID]int{},
		timers: map[api.TaskID]*time.Timer{}, base: 500 * time.Millisecond, max: 2 * time.Minute}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) Add(id api.TaskID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shutdown || q.dirty[id] {
		return
	}
	q.dirty[id] = true
	if !q.processing[id] {
		q.order = append(q.order, id)
		q.cond.Signal()
	}
}

// AddAfter adds id after d. An earlier pending AddAfter for id is kept if it
// fires sooner.
func (q *queue) AddAfter(id api.TaskID, d time.Duration) {
	if d <= 0 {
		q.Add(id)
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shutdown {
		return
	}
	if t, ok := q.timers[id]; ok {
		t.Stop()
	}
	q.timers[id] = time.AfterFunc(d, func() {
		q.mu.Lock()
		delete(q.timers, id)
		q.mu.Unlock()
		q.Add(id)
	})
}

func (q *queue) AddRateLimited(id api.TaskID) {
	q.mu.Lock()
	n := q.failures[id]
	q.failures[id] = n + 1
	q.mu.Unlock()
	d := q.base << min(n, 16)
	if d > q.max || d <= 0 {
		d = q.max
	}
	q.AddAfter(id, d)
}

func (q *queue) Forget(id api.TaskID) {
	q.mu.Lock()
	delete(q.failures, id)
	q.mu.Unlock()
}

// Get blocks for the next key; ok is false after ShutDown.
func (q *queue) Get() (api.TaskID, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.order) == 0 && !q.shutdown {
		q.cond.Wait()
	}
	if len(q.order) == 0 {
		return "", false
	}
	id := q.order[0]
	q.order = q.order[1:]
	q.processing[id] = true
	delete(q.dirty, id)
	return id, true
}

func (q *queue) Done(id api.TaskID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.processing, id)
	if q.dirty[id] {
		q.order = append(q.order, id)
		q.cond.Signal()
	}
}

func (q *queue) ShutDown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shutdown = true
	for _, t := range q.timers {
		t.Stop()
	}
	q.cond.Broadcast()
}
