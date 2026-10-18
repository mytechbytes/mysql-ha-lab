package orders

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"labapp-go/internal/db"
	"labapp-go/internal/web"
)

// Load: every LOAD_DELAY, CREATE one order on the primary (with retry), then
// READ it back from a secondary. A miss on the secondary is a stale read.
type Load struct {
	svc   *Service
	delay time.Duration
	log   *slog.Logger

	running                                     atomic.Bool
	seq, ok, retried, failed, dedup, stale, max atomic.Int64

	staleReads prometheus.Counter // app_db_stale_reads_total
}

func NewLoad(svc *Service, reg prometheus.Registerer, enabled bool, delay time.Duration) *Load {
	l := &Load{
		svc:   svc,
		delay: delay,
		log:   slog.With("logger", "load"),
		staleReads: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "app_db_stale_reads_total",
			Help: "Reads on a secondary that did not see our own write yet",
		}),
	}
	reg.MustRegister(l.staleReads)
	l.running.Store(enabled)
	return l
}

// Run loops until ctx is cancelled. Started by main only after InitSchema.
// Like Spring's fixedDelay: the pause starts after a tick has finished.
func (l *Load) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.delay):
		}
		if l.running.Load() {
			l.tick(ctx)
		}
	}
}

func (l *Load) tick(ctx context.Context) {
	n := l.seq.Add(1)

	// 1. CREATE on the primary (retried inside Service.Create)
	t0 := time.Now()
	w, err := l.svc.Create(ctx, "load", 1, "")
	if err != nil {
		l.failed.Add(1)
		l.log.Info("FAIL", "n", n, "op", "create", "error", db.Describe(err))
		return
	}
	ms := time.Since(t0).Milliseconds()
	for { // atomic max
		cur := l.max.Load()
		if ms <= cur || l.max.CompareAndSwap(cur, ms) {
			break
		}
	}
	if w.Attempts > 1 {
		l.retried.Add(1)
	} else {
		l.ok.Add(1)
	}
	if w.Deduplicated {
		l.dedup.Add(1)
	}

	// 2. READ it back from a secondary: did replication keep up?
	read := ""
	r, err := l.svc.Find(ctx, w.Order.ID, false)
	switch {
	case err != nil:
		read = "FAIL " + db.Describe(err)
	case r.Value != nil:
		read = r.Value.ReadFrom
	default:
		l.stale.Add(1)
		l.staleReads.Inc()
		read = "STALE -> fresh ?"
		if f, err := l.svc.Find(ctx, w.Order.ID, true); err == nil && f.Value != nil {
			read = "STALE -> fresh " + f.Value.ReadFrom
		}
	}

	l.log.Info("OK", "n", n, "id", w.Order.ID, "node", w.Order.WrittenOn,
		"attempts", w.Attempts, "ms", ms, "dedup", w.Deduplicated, "read", read)
}

func (l *Load) Status() map[string]any {
	return map[string]any{
		"running": l.running.Load(), "iterations": l.seq.Load(),
		"ok": l.ok.Load(), "retried": l.retried.Load(), "failed": l.failed.Load(),
		"dedup": l.dedup.Load(), "staleReads": l.stale.Load(), "maxCreateMs": l.max.Load(),
	}
}

func (l *Load) reset() {
	for _, a := range []*atomic.Int64{&l.seq, &l.ok, &l.retried, &l.failed, &l.dedup, &l.stale, &l.max} {
		a.Store(0)
	}
}

// RegisterLoad adds /load/start|stop|reset (POST) and /load/status (GET).
func RegisterLoad(mux *http.ServeMux, l *Load) {
	reply := func(w http.ResponseWriter) { web.JSON(w, http.StatusOK, l.Status()) }
	mux.HandleFunc("POST /load/start", func(w http.ResponseWriter, r *http.Request) { l.running.Store(true); reply(w) })
	mux.HandleFunc("POST /load/stop", func(w http.ResponseWriter, r *http.Request) { l.running.Store(false); reply(w) })
	mux.HandleFunc("POST /load/reset", func(w http.ResponseWriter, r *http.Request) { l.reset(); reply(w) })
	mux.HandleFunc("GET /load/status", func(w http.ResponseWriter, r *http.Request) { reply(w) })
}
