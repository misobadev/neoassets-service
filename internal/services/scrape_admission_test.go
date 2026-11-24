package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/pkg/auth"
)

// A key never has more than limit holders at once, however many ask.
func TestSlotPoolCapsConcurrency(t *testing.T) {
	var p slotPool
	var inFlight, peak, served atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := p.acquire(context.Background(), "user:a", 4, time.Second)
			if !ok {
				return
			}
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
			served.Add(1)
			release()
		}()
	}
	wg.Wait()
	if peak.Load() > 4 {
		t.Fatalf("peak %d in flight, want at most 4", peak.Load())
	}
	if served.Load() != 40 {
		t.Fatalf("served %d of 40 within the wait", served.Load())
	}
	if len(p.entries) != 0 {
		t.Fatalf("%d idle entries kept, want 0", len(p.entries))
	}
}

// A request waits for a slot up to the wait, then gives up; other keys are
// independent, and a released slot is reused.
func TestSlotPoolWaitsThenRefuses(t *testing.T) {
	var p slotPool
	ctx := context.Background()
	hold, ok := p.acquire(ctx, "k", 1, 0)
	if !ok {
		t.Fatal("first acquire refused")
	}
	start := time.Now()
	if _, ok := p.acquire(ctx, "k", 1, 30*time.Millisecond); ok {
		t.Fatal("a full key admitted a second holder")
	}
	if time.Since(start) < 25*time.Millisecond {
		t.Fatal("refused without waiting")
	}
	if other, ok := p.acquire(ctx, "other", 1, 0); !ok {
		t.Fatal("another key was blocked")
	} else {
		other()
	}

	go func() { time.Sleep(10 * time.Millisecond); hold() }()
	next, ok := p.acquire(ctx, "k", 1, time.Second)
	if !ok {
		t.Fatal("a released slot was not handed to the waiter")
	}
	next()
	next() // release is idempotent
	if len(p.entries) != 0 {
		t.Fatalf("%d entries left", len(p.entries))
	}
}

// A level-up raises the limit for new requests right away.
func TestSlotPoolFollowsLimitChanges(t *testing.T) {
	var p slotPool
	ctx := context.Background()
	a, _ := p.acquire(ctx, "k", 1, 0)
	if _, ok := p.acquire(ctx, "k", 1, 0); ok {
		t.Fatal("limit 1 admitted two")
	}
	b, ok := p.acquire(ctx, "k", 2, 0)
	if !ok {
		t.Fatal("a raised limit was not applied")
	}
	a()
	b()
	if len(p.entries) != 0 {
		t.Fatalf("%d entries left", len(p.entries))
	}
}

// A guest's threads belong to the developer app's owner, so two apps of the
// same owner share them; a registered user has their own.
func TestAcquireSubjectSharesGuestThreadsPerOwner(t *testing.T) {
	s := &ScrapeService{}
	ctx := context.Background()
	owner := uuid.New()
	app1 := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "a1", ClientOwnerID: owner, Threads: 1}
	app2 := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "a2", ClientOwnerID: owner, Threads: 1}
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: owner, Threads: 1}

	release, err := s.AcquireSubject(ctx, app1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireSubject(waitCtx, app2); !errors.Is(err, ErrTooManyConcurrent) {
		t.Fatalf("second app of the same owner: err = %v, want ErrTooManyConcurrent", err)
	}
	r2, err := s.AcquireSubject(ctx, user)
	if err != nil {
		t.Fatalf("a registered user shares the guest slots: %v", err)
	}
	r2()
}

func TestAcquireGlobalCapAndDisable(t *testing.T) {
	s := &ScrapeService{}
	s.SetMaxInFlight(0)
	if s.maxInFlight != DefaultScrapeMaxInFlight {
		t.Fatalf("default cap = %d", s.maxInFlight)
	}
	s.SetMaxInFlight(1)
	ctx := context.Background()
	r, err := s.AcquireGlobal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireGlobal(waitCtx); !errors.Is(err, ErrServerBusy) {
		t.Fatalf("full cap: err = %v, want ErrServerBusy", err)
	}
	r()

	s.SetMaxInFlight(-1)
	for i := 0; i < 50; i++ {
		if _, err := s.AcquireGlobal(ctx); err != nil {
			t.Fatalf("disabled cap refused: %v", err)
		}
	}
}
