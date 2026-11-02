package tools

import (
	"context"
	"encoding/json"
	"sync"
)

// Cache stores tool results by content-addressed key.
type Cache interface {
	Get(ctx context.Context, key CacheKey) (Result, bool, error)
	Put(ctx context.Context, key CacheKey, r Result) error
}

// MemCache is an in-process Cache (tests, one-shot CLI runs).
type MemCache struct {
	mu sync.Mutex
	m  map[CacheKey][]byte
}

func NewMemCache() *MemCache { return &MemCache{m: map[CacheKey][]byte{}} }

func (c *MemCache) Get(_ context.Context, k CacheKey) (Result, bool, error) {
	c.mu.Lock()
	b, ok := c.m[k]
	c.mu.Unlock()
	if !ok {
		return Result{}, false, nil
	}
	var r Result
	err := json.Unmarshal(b, &r)
	return r, err == nil, err
}

func (c *MemCache) Put(_ context.Context, k CacheKey, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.m[k] = b
	c.mu.Unlock()
	return nil
}

// Len reports the number of entries.
func (c *MemCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// BlobStore is the persistence a StoreCache needs (implemented by
// internal/storage.Store).
type BlobStore interface {
	GetToolResult(ctx context.Context, key []byte) ([]byte, bool, error)
	PutToolResult(ctx context.Context, key []byte, tool string, result []byte) error
}

// StoreCache persists results in the Spine database, so cached verdicts
// survive restarts and are shared by every task on the repository.
type StoreCache struct{ S BlobStore }

func (c StoreCache) Get(ctx context.Context, k CacheKey) (Result, bool, error) {
	b, ok, err := c.S.GetToolResult(ctx, k[:])
	if err != nil || !ok {
		return Result{}, false, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, false, nil // unreadable entry: treat as a miss, Put overwrites it
	}
	return r, true, nil
}

func (c StoreCache) Put(ctx context.Context, k CacheKey, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return c.S.PutToolResult(ctx, k[:], r.Tool, b)
}
