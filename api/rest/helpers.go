package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeProblem writes an RFC 7807 Problem Details JSON response.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"type":   fmt.Sprintf("https://sage.dev/errors/%d", status),
		"title":  title,
		"status": status,
		"detail": detail,
	})
}

// decodeJSON reads and unmarshals the request body as JSON.
func decodeJSON(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return fmt.Errorf("empty request body")
	}
	// Replace body so downstream handlers can re-read it.
	r.Body = io.NopCloser(bytes.NewReader(body))
	return json.Unmarshal(body, v)
}
