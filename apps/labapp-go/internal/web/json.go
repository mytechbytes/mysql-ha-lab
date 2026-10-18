// Package web holds small HTTP helpers shared by all handlers.
package web

import (
	"encoding/json"
	"net/http"

	"labapp-go/internal/db"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DBError: a database error that survived all retries (or was not retryable)
// becomes 503 with the MySQL error text, like DbErrorHandler in the Spring app.
func DBError(w http.ResponseWriter, err error) {
	JSON(w, http.StatusServiceUnavailable, map[string]string{"error": db.Describe(err)})
}

func BadRequest(w http.ResponseWriter, msg string) {
	JSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}
