package web

import (
	"encoding/json"
	"net/http"
)

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if store == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "not_ready",
			"error":  "registry store not initialized",
		})
		return
	}
	// store != nil only proves the server completed startup; it says nothing
	// about whether the registry's state directory is still mounted and
	// writable right now (readAll/writeAll swallow I/O errors, see
	// pkg/registry). Ping exercises that path on every call so a probe never
	// reports ready while writes are silently failing underneath it.
	if err := store.Ping(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "not_ready",
			"error":  "registry store unwritable: " + err.Error(),
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ready",
	})
}
