// Package orders: the orders table, its service, HTTP handlers and the load loop.
package orders

import (
	"context"
	"database/sql"
	"log/slog"

	"labapp-go/internal/db"
)

const ordersDDL = `
CREATE TABLE IF NOT EXISTS orders (
  id          BIGINT AUTO_INCREMENT PRIMARY KEY,
  request_id  CHAR(36)     NOT NULL,
  item        VARCHAR(64)  NOT NULL,
  qty         INT          NOT NULL,
  written_on  VARCHAR(64)  NOT NULL,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_request_id (request_id),
  KEY ix_created_at (created_at)
)`

const heartbeatDDL = `
CREATE TABLE IF NOT EXISTS heartbeat (
  id       TINYINT PRIMARY KEY,
  beat_at  TIMESTAMP(3) NOT NULL,
  node     VARCHAR(64)  NOT NULL
)`

// InitSchema creates both tables on the primary (through the writer pool, with retry).
// The probe and the load loop start only after this returned nil.
func InitSchema(ctx context.Context, writer *sql.DB, retry *db.Retry) error {
	for _, t := range []struct{ name, ddl string }{{"orders", ordersDDL}, {"heartbeat", heartbeatDDL}} {
		_, err := retry.Run(ctx, db.Write, "schema", func(ctx context.Context) error {
			_, err := writer.ExecContext(ctx, t.ddl)
			return err
		})
		if err != nil {
			return err
		}
		slog.Info("table ready", "table", t.name)
	}
	return nil
}
