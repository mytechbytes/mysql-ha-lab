package health

import (
	"context"
	"net/http"
	"time"

	"labapp-go/internal/db"
	"labapp-go/internal/web"
)

// Register adds:
//
//	GET /health/db  -> 200 when both paths are up, 503 otherwise (+ outage history)
//	GET /whoami     -> node and super_read_only seen through each pool (no retry)
func Register(mux *http.ServeMux, pools *db.Pools, probe *Probe) {
	mux.HandleFunc("GET /health/db", func(w http.ResponseWriter, r *http.Request) {
		s := probe.Snapshot()
		status := http.StatusOK
		if !s.Writer.Up || !s.Reader.Up {
			status = http.StatusServiceUnavailable
		}
		web.JSON(w, status, s)
	})

	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		writer, err := checkPool(ctx, pools.Writer, "writer", false)
		if err != nil {
			web.DBError(w, err)
			return
		}
		reader, err := checkPool(ctx, pools.Reader, "reader", true)
		if err != nil {
			web.DBError(w, err)
			return
		}
		web.JSON(w, http.StatusOK, map[string]any{"writer": writer, "reader": reader})
	})
}
