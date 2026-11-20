// Package apperr defines the errors services return to describe what went
// wrong in terms of the request: invalid input, a missing resource, a state
// conflict, a forbidden action, failed authentication, or a dependency that is
// unavailable. Their message is safe to show to the client.
//
// Handlers still choose the HTTP status they always used for each endpoint;
// Kind lets that mapping move to one place when the API adopts 404/409/403.
package apperr

import "errors"

// Kind classifies an application error.
type Kind int

const (
	// KindInvalid is a request that fails validation.
	KindInvalid Kind = iota + 1
	// KindNotFound is a missing (or not visible) resource.
	KindNotFound
	// KindConflict is a request that clashes with the current state (a
	// duplicate, a submission that is no longer pending).
	KindConflict
	// KindForbidden is an action the caller is not allowed to take.
	KindForbidden
	// KindUnauthorized is a failed authentication or signature check.
	KindUnauthorized
	// KindUnavailable is a dependency that is not configured or not responding.
	KindUnavailable
)

var kindNames = map[Kind]string{
	KindInvalid:      "invalid",
	KindNotFound:     "not_found",
	KindConflict:     "conflict",
	KindForbidden:    "forbidden",
	KindUnauthorized: "unauthorized",
	KindUnavailable:  "unavailable",
}

func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return "unknown"
}

// Error is an application error with a client-safe message.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Is matches another *Error with the same kind and message, so sentinel
// errors built with these constructors work with errors.Is.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind && t.Msg == e.Msg
}

// Invalid returns a KindInvalid error.
func Invalid(msg string) error { return &Error{Kind: KindInvalid, Msg: msg} }

// NotFound returns a KindNotFound error.
func NotFound(msg string) error { return &Error{Kind: KindNotFound, Msg: msg} }

// Conflict returns a KindConflict error.
func Conflict(msg string) error { return &Error{Kind: KindConflict, Msg: msg} }

// Forbidden returns a KindForbidden error.
func Forbidden(msg string) error { return &Error{Kind: KindForbidden, Msg: msg} }

// Unauthorized returns a KindUnauthorized error.
func Unauthorized(msg string) error { return &Error{Kind: KindUnauthorized, Msg: msg} }

// Unavailable returns a KindUnavailable error.
func Unavailable(msg string) error { return &Error{Kind: KindUnavailable, Msg: msg} }

// KindOf returns the kind of the first *Error in err's chain, or 0.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}
