package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
)

type Kind string

const (
	Read  Kind = "read"
	Write Kind = "write"
)

// Server errors seen during failover / switchover: retry the WHOLE transaction.
//
//	1205 lock wait timeout, 1213 deadlock, 1290 --super-read-only (old primary),
//	1836 read-only mode, 3100/3101 GR rejected / rolled back the transaction
//
// (2002/2003/2006/2013 are client-side codes; the Go driver reports them as
// network errors, handled in Classify.)
var retryableCodes = map[uint16]bool{1205: true, 1213: true, 1290: true, 1836: true, 3100: true, 3101: true}

// Configuration errors: retrying cannot help, fail at once.
//
//	1044/1045 access denied, 1049 unknown database, 1064 SQL syntax,
//	1142/1143 command/column denied, 1146 table missing
var permanentCodes = map[uint16]bool{1044: true, 1045: true, 1049: true, 1064: true, 1142: true, 1143: true, 1146: true}

type Retry struct {
	maxAttempts    int
	attemptTimeout time.Duration
	log            *slog.Logger

	ops           *prometheus.CounterVec   // app_db_ops_total{kind,outcome}
	attemptErrors *prometheus.CounterVec   // app_db_attempt_errors_total{kind,error}
	opSeconds     *prometheus.HistogramVec // app_db_op_seconds{kind}  (incl. retries)
	opMax         *MaxGauge                // app_db_op_seconds_max{kind}
}

func NewRetry(reg prometheus.Registerer, maxAttempts int, attemptTimeout time.Duration) *Retry {
	r := &Retry{
		maxAttempts:    maxAttempts,
		attemptTimeout: attemptTimeout,
		log:            slog.With("logger", "retry"),
		ops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "app_db_ops_total",
			Help: "DB operations by final outcome: ok (1st attempt), retried (ok after retry), failed",
		}, []string{"kind", "outcome"}),
		attemptErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "app_db_attempt_errors_total",
			Help: "Failed attempts by error code (one operation can fail several attempts)",
		}, []string{"kind", "error"}),
		opSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "app_db_op_seconds",
			Help:    "DB operation incl. retries",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60},
		}, []string{"kind"}),
		opMax: NewMaxGauge("app_db_op_seconds_max", "Slowest DB operation in the last 1-2 minutes", "kind",
			string(Read), string(Write)),
	}
	reg.MustRegister(r.ops, r.attemptErrors, r.opSeconds, r.opMax)

	// Create every series at 0 up front: a series that first appears with value 1
	// is invisible to increase()/rate(), so the first retry would never show.
	for _, k := range []Kind{Read, Write} {
		for _, o := range []string{"ok", "retried", "failed"} {
			r.ops.WithLabelValues(string(k), o)
		}
		for _, e := range []string{"1290", "2003", "2013", "timeout"} {
			r.attemptErrors.WithLabelValues(string(k), e)
		}
		r.opSeconds.WithLabelValues(string(k))
	}
	return r
}

// Run executes fn - ONE complete transaction - and retries it as a whole on
// failover errors, with back-off 200, 400, 800, 1600, 2000, 2000 ... ms.
// Each attempt gets its own deadline (TX_TIMEOUT). Returns the attempts used.
func (r *Retry) Run(ctx context.Context, kind Kind, op string, fn func(ctx context.Context) error) (int, error) {
	start := time.Now()
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
		err := fn(actx)
		cancel()
		if err == nil {
			outcome := "ok"
			if attempt > 1 {
				outcome = "retried"
			}
			r.finish(kind, outcome, start)
			return attempt, nil
		}

		tag, retryable := Classify(err)
		r.attemptErrors.WithLabelValues(string(kind), tag).Inc()
		if !retryable || attempt >= r.maxAttempts || ctx.Err() != nil {
			r.finish(kind, "failed", start)
			r.log.Error("FAILED", "op", op, "attempts", attempt, "error", Describe(err))
			return attempt, err
		}

		sleep := min(time.Duration(200<<(attempt-1))*time.Millisecond, 2*time.Second)
		r.log.Warn("attempt failed, retrying", "op", op, "attempt", attempt, "error", Describe(err), "retryIn", sleep)
		select {
		case <-time.After(sleep):
		case <-ctx.Done(): // shutdown
			r.finish(kind, "failed", start)
			return attempt, err
		}
	}
}

func (r *Retry) finish(kind Kind, outcome string, start time.Time) {
	secs := time.Since(start).Seconds()
	r.ops.WithLabelValues(string(kind), outcome).Inc()
	r.opSeconds.WithLabelValues(string(kind)).Observe(secs)
	r.opMax.Observe(string(kind), secs)
}

// Classify returns a short, low-cardinality label for the error and whether a
// retry can help.
//
//	"1290", "1045", ...  MySQL server error number
//	"2003"               could not connect (router / port refused, DNS, dial timeout)
//	"2013"               connection lost during a query (node killed, router dropped it)
//	"timeout"            the attempt deadline expired (pool wait or a slow query)
func Classify(err error) (tag string, retryable bool) {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		tag = strconv.Itoa(int(me.Number))
		if permanentCodes[me.Number] {
			return tag, false
		}
		return tag, retryableCodes[me.Number] || strings.HasPrefix(sqlState(me), "08")
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "canceled", false
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, sql.ErrTxDone):
		// ErrTxDone: the deadline expired mid-transaction and database/sql rolled it back
		return "timeout", true
	case errors.Is(err, driver.ErrBadConn), errors.Is(err, mysql.ErrInvalidConn),
		errors.Is(err, sql.ErrConnDone), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "2013", true
	}

	var oe *net.OpError
	if errors.As(err, &oe) {
		if oe.Op == "dial" {
			return "2003", true
		}
		return "2013", true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return "2013", true
	}
	return "other", false
}

// Describe gives "ERROR 1290 (HY000): ..." for logs and API responses.
func Describe(err error) string {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return "ERROR " + strconv.Itoa(int(me.Number)) + " (" + sqlState(me) + "): " + shorten(me.Message)
	}
	tag, _ := Classify(err)
	return tag + ": " + shorten(err.Error())
}

// IsDuplicateKey reports MySQL error 1062 (UNIQUE / PRIMARY KEY violation).
func IsDuplicateKey(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

func sqlState(me *mysql.MySQLError) string {
	return strings.TrimRight(string(me.SQLState[:]), "\x00")
}

func shorten(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}
