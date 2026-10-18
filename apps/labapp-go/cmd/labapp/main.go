// labapp-go: the Go twin of labapp-springboot. Same endpoints, same tables,
// same app_db_* metrics, so both apps can be compared on one Grafana dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"labapp-go/internal/config"
	"labapp-go/internal/db"
	"labapp-go/internal/health"
	"labapp-go/internal/orders"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("labapp-go stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pools, err := db.Open(cfg)
	if err != nil {
		return err
	}
	defer pools.Close()

	// Every metric gets application="labapp-go" (Spring: management.metrics.tags.application).
	registry := prometheus.NewRegistry()
	reg := prometheus.WrapRegistererWith(prometheus.Labels{"application": config.AppName}, registry)
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewDBStatsCollector(pools.Writer, "go-writer"), // go_sql_*{db_name="go-writer"}
		collectors.NewDBStatsCollector(pools.Reader, "go-reader"),
	)

	retry := db.NewRetry(reg, cfg.MaxAttempts, cfg.TxTimeout)
	svc := orders.NewService(pools, retry)
	load := orders.NewLoad(svc, reg, cfg.LoadEnabled, cfg.LoadDelay)
	probe := health.NewProbe(pools, reg, cfg.ProbeInterval, cfg.ProbeTimeout)

	mux := http.NewServeMux()
	orders.Register(mux, svc)
	orders.RegisterLoad(mux, load)
	health.Register(mux, pools, probe)
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr, "db", cfg.DBHost, "writerPort", cfg.WriterPort, "readerPort", cfg.ReaderPort)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
			stop()
		}
	}()

	// Tables first; only then the probe and the load loop (no fake outage at startup).
	if err := orders.InitSchema(ctx, pools.Writer, retry); err != nil {
		return errors.Join(errors.New("schema init failed: "+db.Describe(err)), shutdown(srv))
	}
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){probe.Run, load.Run} {
		wg.Add(1)
		go func() { defer wg.Done(); loop(ctx) }()
	}
	slog.Info("labapp-go ready", "load", cfg.LoadEnabled)

	<-ctx.Done()
	slog.Info("shutting down")
	err = shutdown(srv)
	wg.Wait()
	select {
	case e := <-httpErr:
		return errors.Join(e, err)
	default:
		return err
	}
}

func shutdown(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
