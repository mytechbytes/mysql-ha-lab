package db

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"labapp-go/internal/config"
)

// Pools: Writer -> router 6446 (always the PRIMARY), Reader -> router 6447 (a SECONDARY).
type Pools struct {
	Writer *sql.DB
	Reader *sql.DB
}

// Open builds both pools. It does NOT connect: like Hikari with
// initialization-fail-timeout=-1, the app starts even while the cluster is down.
func Open(cfg config.Config) (*Pools, error) {
	w, err := open(cfg, cfg.WriterPort, "go-writer")
	if err != nil {
		return nil, err
	}
	r, err := open(cfg, cfg.ReaderPort, "go-reader")
	if err != nil {
		w.Close()
		return nil, err
	}
	return &Pools{Writer: w, Reader: r}, nil
}

func (p *Pools) Close() error {
	return errors.Join(p.Writer.Close(), p.Reader.Close())
}

func open(cfg config.Config, port int, name string) (*sql.DB, error) {
	c := mysql.NewConfig() // a Config struct, so the password never needs URL-escaping
	c.User = cfg.DBUser
	c.Passwd = cfg.DBPassword
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(cfg.DBHost, strconv.Itoa(port))
	c.DBName = cfg.DBName
	c.Timeout = 3 * time.Second       // TCP connect + handshake  (Hikari connectTimeout)
	c.ReadTimeout = 15 * time.Second  // one network read           (socketTimeout)
	c.WriteTimeout = 15 * time.Second // one network write
	c.ParseTime = true                // TIMESTAMP -> time.Time
	c.InterpolateParams = true        // send "?" values inline: 1 round trip, no server-side PREPARE
	// Visible in performance_schema.session_connect_attrs.
	c.ConnectionAttributes = "program_name:" + config.AppName + ",pool:" + name

	connector, err := mysql.NewConnector(c)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(cfg.PoolSize)       // maximum-pool-size
	db.SetMaxIdleConns(2)                  // minimum-idle (roughly)
	db.SetConnMaxLifetime(5 * time.Minute) // max-lifetime
	db.SetConnMaxIdleTime(1 * time.Minute) // idle connections are closed, so few go stale
	return db, nil
}

// InTx runs fn inside ONE transaction: commit when fn returns nil, rollback otherwise.
// readOnly=true sends START TRANSACTION READ ONLY.
func InTx(ctx context.Context, db *sql.DB, readOnly bool, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
