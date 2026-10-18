// Package health: the 1-second DB probe, /health/db and /whoami.
package health

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"labapp-go/internal/db"
)

const historySize = 50

type Outage struct {
	Target     string    `json:"target"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Millis     int64     `json:"millis"`
	FirstError string    `json:"firstError"`
}

type path struct {
	name       string
	check      func(ctx context.Context) error
	up         bool
	downSince  time.Time
	firstError string
}

// Probe: every PROBE_INTERVAL, one tiny write through the writer pool and one
// read through the reader pool, WITHOUT retry. Every up->down / down->up change
// is logged, exported as metrics and kept in a history of the last 50 outages.
//
//	WARN DOWN target=writer since=13:02:11.204 error="2013: invalid connection"
//	WARN UP   target=writer at=13:02:17.530 downMs=6326
type Probe struct {
	interval, timeout time.Duration
	log               *slog.Logger

	mu      sync.Mutex
	writer  *path
	reader  *path
	history []Outage

	up        *prometheus.GaugeVec     // app_db_probe_up{target}
	lastStart *prometheus.GaugeVec     // app_db_outage_last_start_seconds{target}
	lastEnd   *prometheus.GaugeVec     // app_db_outage_last_end_seconds{target}
	lastDur   *prometheus.GaugeVec     // app_db_outage_last_duration_seconds{target}
	outages   *prometheus.HistogramVec // app_db_outage_seconds{target}  (_count = number of outages)
}

func NewProbe(pools *db.Pools, reg prometheus.Registerer, interval, timeout time.Duration) *Probe {
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, []string{"target"})
	}
	p := &Probe{
		interval: interval,
		timeout:  timeout,
		log:      slog.With("logger", "probe"),
		writer: &path{name: "writer", up: true, check: func(ctx context.Context) error {
			_, err := pools.Writer.ExecContext(ctx, `
				INSERT INTO heartbeat (id, beat_at, node) VALUES (1, NOW(3), @@hostname) AS n
				ON DUPLICATE KEY UPDATE beat_at = n.beat_at, node = n.node`)
			return err
		}},
		reader: &path{name: "reader", up: true, check: func(ctx context.Context) error {
			var one int
			return pools.Reader.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		}},
		up:        gauge("app_db_probe_up", "1 = path healthy, 0 = failing"),
		lastStart: gauge("app_db_outage_last_start_seconds", "Unix time the last outage started"),
		lastEnd:   gauge("app_db_outage_last_end_seconds", "Unix time the last outage ended"),
		lastDur:   gauge("app_db_outage_last_duration_seconds", "Length of the last finished outage"),
		outages: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "app_db_outage_seconds",
			Help:    "Finished outages and their length",
			Buckets: []float64{1, 2, 5, 10, 15, 20, 30, 60, 120, 300},
		}, []string{"target"}),
	}
	reg.MustRegister(p.up, p.lastStart, p.lastEnd, p.lastDur, p.outages)
	for _, t := range []string{"writer", "reader"} {
		p.up.WithLabelValues(t).Set(1)
		p.lastStart.WithLabelValues(t)
		p.lastEnd.WithLabelValues(t)
		p.lastDur.WithLabelValues(t)
		p.outages.WithLabelValues(t)
	}
	return p
}

// Run probes both paths, each in its own goroutine, until ctx is cancelled.
// Started by main only after InitSchema (the heartbeat table must exist).
func (p *Probe) Run(ctx context.Context) {
	p.log.Info("probe started")
	var wg sync.WaitGroup
	for _, pa := range []*path{p.writer, p.reader} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				p.checkOnce(ctx, pa)
				select {
				case <-ctx.Done():
					return
				case <-time.After(p.interval):
				}
			}
		}()
	}
	wg.Wait()
}

func (p *Probe) checkOnce(ctx context.Context, pa *path) {
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	err := pa.check(cctx)
	cancel()
	if ctx.Err() != nil {
		return // shutting down: not an outage
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case err != nil && pa.up:
		pa.up, pa.downSince, pa.firstError = false, started, db.Describe(err)
		p.up.WithLabelValues(pa.name).Set(0)
		p.lastStart.WithLabelValues(pa.name).Set(float64(started.UnixMilli()) / 1000)
		p.log.Warn("DOWN", "target", pa.name, "since", started.Format("15:04:05.000"), "error", pa.firstError)
	case err == nil && !pa.up:
		now := time.Now()
		d := now.Sub(pa.downSince)
		pa.up = true
		p.up.WithLabelValues(pa.name).Set(1)
		p.lastEnd.WithLabelValues(pa.name).Set(float64(now.UnixMilli()) / 1000)
		p.lastDur.WithLabelValues(pa.name).Set(d.Seconds())
		p.outages.WithLabelValues(pa.name).Observe(d.Seconds())
		p.history = append([]Outage{{pa.name, pa.downSince, now, d.Milliseconds(), pa.firstError}}, p.history...)
		if len(p.history) > historySize {
			p.history = p.history[:historySize]
		}
		p.log.Warn("UP", "target", pa.name, "at", now.Format("15:04:05.000"), "downMs", d.Milliseconds())
	}
}

type PathStatus struct {
	Up         bool       `json:"up"`
	DownSince  *time.Time `json:"downSince,omitempty"`
	DownForMs  int64      `json:"downForMs,omitempty"`
	FirstError string     `json:"firstError,omitempty"`
}

type Snapshot struct {
	Writer  PathStatus `json:"writer"`
	Reader  PathStatus `json:"reader"`
	Outages []Outage   `json:"outages"`
}

func (p *Probe) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := func(pa *path) PathStatus {
		if pa.up {
			return PathStatus{Up: true}
		}
		since := pa.downSince
		return PathStatus{DownSince: &since, DownForMs: time.Since(since).Milliseconds(), FirstError: pa.firstError}
	}
	return Snapshot{
		Writer:  status(p.writer),
		Reader:  status(p.reader),
		Outages: append([]Outage{}, p.history...),
	}
}

// checkPool is used by /whoami: which node does each pool reach right now?
func checkPool(ctx context.Context, pool *sql.DB, name string, readOnly bool) (map[string]any, error) {
	var node string
	var ro int
	err := db.InTx(ctx, pool, readOnly, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT @@hostname, @@super_read_only").Scan(&node, &ro)
	})
	return map[string]any{"pool": name, "node": node, "read_only": ro}, err
}
