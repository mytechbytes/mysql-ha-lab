package orders

import (
	"fmt"
	"net/http"
	"strconv"

	"labapp-go/internal/web"
)

// Register adds the /orders endpoints (Go 1.22+ method + path patterns).
//
//	curl -X POST 'localhost:8091/orders?item=pen&qty=2' [-H 'Idempotency-Key: <uuid>']
//	curl localhost:8091/orders/1[?fresh=true]
//	curl 'localhost:8091/orders?limit=5'
//	curl -X PUT 'localhost:8091/orders/1?qty=5'
//	curl -X DELETE localhost:8091/orders/1
func Register(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		item := q.Get("item")
		if item == "" {
			item = "widget"
		}
		qty, ok := intParam(w, q.Get("qty"), 1)
		if !ok {
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > 36 {
			web.BadRequest(w, "Idempotency-Key must be at most 36 characters")
			return
		}
		res, err := s.Create(r.Context(), item, qty, key)
		if err != nil {
			web.DBError(w, err)
			return
		}
		status := http.StatusCreated
		if res.Deduplicated {
			status = http.StatusOK
		}
		web.JSON(w, status, res)
	})

	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		fresh := r.URL.Query().Get("fresh") == "true"
		res, err := s.Find(r.Context(), id, fresh)
		if err != nil {
			web.DBError(w, err)
			return
		}
		if res.Value == nil {
			notFound(w, id, res.Attempts)
			return
		}
		web.JSON(w, http.StatusOK, res.Value)
	})

	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		limit, ok := intParam(w, r.URL.Query().Get("limit"), 10)
		if !ok {
			return
		}
		res, err := s.Recent(r.Context(), limit)
		if err != nil {
			web.DBError(w, err)
			return
		}
		web.JSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("PUT /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		qs := r.URL.Query().Get("qty")
		if qs == "" {
			web.BadRequest(w, "qty is required")
			return
		}
		qty, ok := intParam(w, qs, 0)
		if !ok {
			return
		}
		res, err := s.UpdateQty(r.Context(), id, qty)
		if err != nil {
			web.DBError(w, err)
			return
		}
		if res.Value == nil {
			notFound(w, id, res.Attempts)
			return
		}
		web.JSON(w, http.StatusOK, res.Value)
	})

	mux.HandleFunc("DELETE /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idParam(w, r)
		if !ok {
			return
		}
		res, err := s.Delete(r.Context(), id)
		if err != nil {
			web.DBError(w, err)
			return
		}
		if !res.Value {
			notFound(w, id, res.Attempts)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		web.BadRequest(w, "id must be a number")
		return 0, false
	}
	return id, true
}

func intParam(w http.ResponseWriter, v string, def int) (int, bool) {
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		web.BadRequest(w, fmt.Sprintf("%q is not a number", v))
		return 0, false
	}
	return n, true
}

func notFound(w http.ResponseWriter, id int64, attempts int) {
	web.JSON(w, http.StatusNotFound, map[string]any{
		"error":    fmt.Sprintf("order %d not found", id),
		"attempts": attempts,
	})
}
