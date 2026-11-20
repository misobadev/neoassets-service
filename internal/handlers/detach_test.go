package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDetachWrites(t *testing.T) {
	var gotErr error
	var hasDeadline bool
	h := DetachWrites(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotErr = r.Context().Err()
		_, hasDeadline = r.Context().Deadline()
	}))

	serve := func(method string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		req := httptest.NewRequest(method, "/", nil).WithContext(ctx)
		cancel() // the client went away
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	serve(http.MethodGet)
	if gotErr == nil {
		t.Fatal("a read should see the cancellation")
	}
	serve(http.MethodPost)
	if gotErr != nil {
		t.Fatalf("a write should not be cancelled, got %v", gotErr)
	}
	if !hasDeadline {
		t.Fatal("a write should keep the request deadline")
	}
}
