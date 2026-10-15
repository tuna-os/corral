package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Characterization tests for the JSON response helpers (#503).

func TestJSONResp(t *testing.T) {
	w := httptest.NewRecorder()
	jsonResp(w, http.StatusAccepted, map[string]any{"task": "t1", "n": 2})
	if w.Code != http.StatusAccepted {
		t.Errorf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := w.Body.String(); got != "{\"n\":2,\"task\":\"t1\"}\n" {
		t.Errorf("body = %q", got)
	}
}

func TestErrResp(t *testing.T) {
	w := httptest.NewRecorder()
	errResp(w, http.StatusConflict, errors.New(`stop "vm" <first>`))
	if w.Code != http.StatusConflict {
		t.Errorf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	// encoding/json escapes HTML characters by default.
	if got := w.Body.String(); got != "{\"error\":\"stop \\\"vm\\\" \\u003cfirst\\u003e\"}\n" {
		t.Errorf("body = %q", got)
	}
}

func TestStatusFor(t *testing.T) {
	base := errors.New("bad name")
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"plain", base, http.StatusInternalServerError},
		{"badRequest", badRequest(base), http.StatusBadRequest},
		{"wrapped badRequest", fmt.Errorf("create: %w", badRequest(base)), http.StatusBadRequest},
		{"custom status", &httpError{status: http.StatusConflict, err: base}, http.StatusConflict},
	} {
		if got := statusFor(tc.err); got != tc.want {
			t.Errorf("%s: statusFor = %d, want %d", tc.name, got, tc.want)
		}
	}

	err := badRequest(base)
	if err.Error() != "bad name" || !errors.Is(err, base) {
		t.Errorf("badRequest must keep the message and unwrap: %v", err)
	}
}
