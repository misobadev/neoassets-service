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

// A guest's threads belong to the guest (their address), not the app: the same
// guest on two apps shares them, another guest of the same app has their own,
// and a registered user has their own.
func TestAcquireSubjectKeysGuestThreadsByAddress(t *testing.T) {
	s := &ScrapeService{}
	ctx := context.Background()
	owner := uuid.New()
	app1 := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "a1", ClientOwnerID: owner, ClientKey: "203.0.113.7", Threads: 1}
	app2 := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "a2", ClientOwnerID: owner, ClientKey: "203.0.113.7", Threads: 1}
	otherGuest := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "a1", ClientOwnerID: owner, ClientKey: "198.51.100.9", Threads: 1}
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: owner, Threads: 1}

	release, err := s.AcquireSubject(ctx, app1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireSubject(waitCtx, app2); !errors.Is(err, ErrTooManyConcurrent) {
		t.Fatalf("same guest on a second app: err = %v, want ErrTooManyConcurrent", err)
	}
	r3, err := s.AcquireSubject(ctx, otherGuest)
	if err != nil {
		t.Fatalf("another guest must have their own slots: %v", err)
	}
	r3()
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
	r, err := s.AcquireGlobal(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireGlobal(waitCtx, nil); !errors.Is(err, ErrServerBusy) {
		t.Fatalf("full cap: err = %v, want ErrServerBusy", err)
	}
	r()

	s.SetMaxInFlight(-1)
	for i := 0; i < 50; i++ {
		if _, err := s.AcquireGlobal(ctx, nil); err != nil {
			t.Fatalf("disabled cap refused: %v", err)
		}
	}
}

// All guests together share a small slice of the global cap, so anonymous
// traffic can't take the server from registered users.
func TestAcquireGlobalCapsGuestsTogether(t *testing.T) {
	s := &ScrapeService{}
	s.SetMaxInFlight(10)
	s.SetGuestMaxInFlight(1)
	ctx := context.Background()
	guestA := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientKey: "203.0.113.7", Threads: 1}
	guestB := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientKey: "198.51.100.9", Threads: 1}
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 1}

	release, err := s.AcquireGlobal(ctx, guestA)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireGlobal(waitCtx, guestB); !errors.Is(err, ErrServerBusy) {
		t.Fatalf("second guest while the guest share is full: err = %v, want ErrServerBusy", err)
	}
	r2, err := s.AcquireGlobal(ctx, user)
	if err != nil {
		t.Fatalf("a user must not wait for the guest share: %v", err)
	}
	r2()
	release()

	// Releasing frees both the guest slot and the global one.
	r3, err := s.AcquireGlobal(ctx, guestB)
	if err != nil {
		t.Fatalf("guest after release: %v", err)
	}
	r3()
	if n := len(s.globalSlots.entries); n != 0 {
		t.Fatalf("global slots still held: %d", n)
	}
}

// Open threads are capped per kind (all guests together, all users together)
// and counted in the live status; releasing frees the slot and the count.
func TestOpenThreadCapsAndLiveStatus(t *testing.T) {
	s := &ScrapeService{}
	s.SetOpenThreadCaps(1, 2)
	ctx := context.Background()
	guestA := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientKey: "203.0.113.7", Threads: 1}
	guestB := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientKey: "198.51.100.9", Threads: 1}
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 4}

	relGuest, err := s.AcquireSubject(ctx, guestA)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireSubject(waitCtx, guestB); !errors.Is(err, ErrServerBusy) {
		t.Fatalf("guest open threads full: err = %v, want ErrServerBusy", err)
	}
	relUser, err := s.AcquireSubject(ctx, user)
	if err != nil {
		t.Fatalf("users have their own open-thread cap: %v", err)
	}

	st := s.Status()
	if st.OpenThreads.Guests != 1 || st.OpenThreads.Users != 1 || st.OpenThreads.GuestsMax != 1 || st.OpenThreads.UsersMax != 2 {
		t.Fatalf("open threads = %+v", st.OpenThreads)
	}
	if st.RequestsPerMinute != 2 || st.Scrapers.Guests != 1 || st.Scrapers.Users != 1 {
		t.Fatalf("status = %+v", st)
	}

	relGuest()
	relUser()
	if g, u := s.live.openGuests.Load(), s.live.openUsers.Load(); g != 0 || u != 0 {
		t.Fatalf("open after release: guests %d users %d", g, u)
	}
	if _, err := s.AcquireSubject(ctx, guestB); err != nil {
		t.Fatalf("guest after release: %v", err)
	}
}
