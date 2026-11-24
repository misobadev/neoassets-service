package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTTLValueCachesUntilCleared(t *testing.T) {
	var c ttlValue[int]
	calls := 0
	load := func(context.Context) (int, error) { calls++; return calls, nil }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, _ := c.get(ctx, time.Minute, load); v != 1 {
			t.Fatalf("get %d = %d, want the first load", i, v)
		}
	}
	c.clear()
	if v, _ := c.get(ctx, time.Minute, load); v != 2 || calls != 2 {
		t.Fatalf("after clear got %d with %d loads, want a fresh load", v, calls)
	}
	// A zero ttl never caches.
	c.get(ctx, 0, load)
	c.get(ctx, 0, load)
	if calls != 4 {
		t.Fatalf("ttl 0: %d loads, want 4", calls)
	}
}

func TestTTLValueExpiresAndSkipsErrors(t *testing.T) {
	var c ttlValue[int]
	ctx := context.Background()
	fail := errors.New("db down")
	if _, err := c.get(ctx, time.Minute, func(context.Context) (int, error) { return 0, fail }); !errors.Is(err, fail) {
		t.Fatalf("err = %v, want the load error", err)
	}
	if v, _ := c.get(ctx, time.Millisecond, func(context.Context) (int, error) { return 7, nil }); v != 7 {
		t.Fatalf("a failed load was cached: got %d", v)
	}
	time.Sleep(5 * time.Millisecond)
	if v, _ := c.get(ctx, time.Millisecond, func(context.Context) (int, error) { return 8, nil }); v != 8 {
		t.Fatalf("an expired value was served: got %d", v)
	}
}

// A burst on an empty cache runs the query once, not once per request.
func TestTTLValueLoadsOnceUnderConcurrency(t *testing.T) {
	var c ttlValue[int]
	var mu sync.Mutex
	calls := 0
	load := func(context.Context) (int, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return 1, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.get(context.Background(), time.Minute, load) }()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("%d loads for 20 concurrent gets, want 1", calls)
	}
}
