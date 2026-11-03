package supervisor

import (
	"context"
	"sync"
)

// Limits is a set of counting semaphores keyed by operation kind, shared by
// every repository's controller. Unknown kinds are unbounded.
type Limits struct {
	sems map[string]chan struct{}
	mu   sync.Mutex
	peak map[string]int
	cur  map[string]int
}

// NewLimits creates limits; a size <= 0 leaves the kind unbounded.
func NewLimits(sizes map[string]int) *Limits {
	l := &Limits{sems: map[string]chan struct{}{}, peak: map[string]int{}, cur: map[string]int{}}
	for k, n := range sizes {
		if n > 0 {
			l.sems[k] = make(chan struct{}, n)
		}
	}
	return l
}

// Acquire implements reconciler.Limiter.
func (l *Limits) Acquire(ctx context.Context, kind string) (func(), error) {
	if sem, ok := l.sems[kind]; ok {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	l.mu.Lock()
	l.cur[kind]++
	if l.cur[kind] > l.peak[kind] {
		l.peak[kind] = l.cur[kind]
	}
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.cur[kind]--
			l.mu.Unlock()
			if sem, ok := l.sems[kind]; ok {
				<-sem
			}
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

// Limits exposes the supervisor's limiter (for status and tests).
func (s *Supervisor) Limits() *Limits { return s.limits }
