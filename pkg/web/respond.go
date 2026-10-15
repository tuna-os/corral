package web

import (
	"encoding/json"
	"errors"
	"net/http"
)

func jsonResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errResp(w http.ResponseWriter, code int, err error) {
	jsonResp(w, code, map[string]string{"error": err.Error()})
}

// httpError lets a function that doesn't touch http.ResponseWriter (e.g. the
// createBootc/createWindows/createGeneric strategy functions) still signal
// which status a validation failure should map to. Plain errors default to
// 500 — see statusFor.
type httpError struct {
	status int
	err    error
}

func (e *httpError) Error() string { return e.err.Error() }
func (e *httpError) Unwrap() error { return e.err }

func badRequest(err error) error { return &httpError{status: http.StatusBadRequest, err: err} }

// statusFor returns the HTTP status a strategy-function error maps to.
func statusFor(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.status
	}
	return http.StatusInternalServerError
}
