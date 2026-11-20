package handlers

import (
	"context"
	"net/http"
)

// DetachWrites keeps a write request running when the client goes away.
// Writes mix database statements and object-store copies, so cancelling one
// part-way (a closed tab, a dropped connection) could leave a review or an
// upload half applied. Reads stay cancellable, and writes keep the request
// deadline set by the timeout middleware.
func DetachWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithoutCancel(r.Context())
		if deadline, ok := r.Context().Deadline(); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
