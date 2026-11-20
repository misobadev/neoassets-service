package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type body struct {
	Comment string `json:"comment"`
}

func TestDecode(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		ok       bool
	}{
		{"valid", `{"comment":"hi"}`, true},
		{"empty", ``, false},
		{"malformed", `{"comment":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var b body
			ok := Decode(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.in)), &b)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), MsgInvalidBody)) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestDecodeOptionalAcceptsAnEmptyBodyOnly(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"empty", ``, "", true},
		{"valid", `{"comment":"hi"}`, "hi", true},
		{"malformed", `not json`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var b body
			ok := DecodeOptional(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.in)), &b)
			if ok != tc.ok || b.Comment != tc.want {
				t.Fatalf("ok=%v comment=%q, want ok=%v comment=%q", ok, b.Comment, tc.ok, tc.want)
			}
			if !ok && rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func TestPathUUID(t *testing.T) {
	req := func(id string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	}
	rec := httptest.NewRecorder()
	if id, ok := PathUUID(rec, req("00000000-0000-4000-8000-000000000001"), "id", "invalid x id"); !ok || id.String() != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("valid id: %v %v", id, ok)
	}
	rec = httptest.NewRecorder()
	if _, ok := PathUUID(rec, req("nope"), "id", "invalid x id"); ok || rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid x id") {
		t.Fatalf("invalid id: ok=%v %d %s", ok, rec.Code, rec.Body)
	}
}
