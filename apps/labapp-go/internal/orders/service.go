package orders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"labapp-go/internal/db"
)

type Order struct {
	ID        int64     `json:"id"`
	RequestID string    `json:"requestId"`
	Item      string    `json:"item"`
	Qty       int       `json:"qty"`
	WrittenOn string    `json:"writtenOn"` // @@hostname of the primary that did the INSERT
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ReadFrom  string    `json:"readFrom"` // @@hostname of the node that answered the SELECT
}

type WriteResult struct {
	Order        Order `json:"order"`
	Attempts     int   `json:"attempts"`
	Deduplicated bool  `json:"deduplicated"`
}

type ReadResult[T any] struct {
	Value    T   `json:"value"`
	Attempts int `json:"attempts"`
}

const columns = "SELECT id, request_id, item, qty, written_on, created_at, updated_at, @@hostname AS read_from FROM orders "

type Service struct {
	pools *db.Pools
	retry *db.Retry
}

func NewService(pools *db.Pools, retry *db.Retry) *Service {
	return &Service{pools: pools, retry: retry}
}

// Create is idempotent: the request_id is UNIQUE, so when a retry repeats an
// INSERT that had in fact committed (the connection died before the OK came
// back), the second INSERT hits 1062 and we return the existing row.
func (s *Service) Create(ctx context.Context, item string, qty int, requestID string) (WriteResult, error) {
	if requestID == "" {
		requestID = newUUID()
	}
	var res WriteResult
	attempts, err := s.retry.Run(ctx, db.Write, "create", func(ctx context.Context) error {
		res.Deduplicated = false
		return db.InTx(ctx, s.pools.Writer, false, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"INSERT INTO orders (request_id, item, qty, written_on) VALUES (?, ?, ?, @@hostname)",
				requestID, item, qty)
			switch {
			case db.IsDuplicateKey(err):
				res.Deduplicated = true // a duplicate key rolls back the statement, not the transaction
			case err != nil:
				return err
			}
			res.Order, err = scanOrder(tx.QueryRowContext(ctx, columns+"WHERE request_id = ?", requestID))
			return err
		})
	})
	res.Attempts = attempts
	return res, err
}

// Find reads one order. fresh=false -> reader pool (a secondary, may lag);
// fresh=true -> writer pool (the primary, always sees the latest commit).
func (s *Service) Find(ctx context.Context, id int64, fresh bool) (ReadResult[*Order], error) {
	pool, op := s.pools.Reader, "read"
	if fresh {
		pool, op = s.pools.Writer, "read-fresh"
	}
	var res ReadResult[*Order]
	attempts, err := s.retry.Run(ctx, db.Read, op, func(ctx context.Context) error {
		return db.InTx(ctx, pool, !fresh, func(tx *sql.Tx) error {
			var err error
			res.Value, err = findByID(ctx, tx, id)
			return err
		})
	})
	res.Attempts = attempts
	return res, err
}

func (s *Service) Recent(ctx context.Context, limit int) (ReadResult[[]Order], error) {
	n := min(max(limit, 1), 100)
	var res ReadResult[[]Order]
	attempts, err := s.retry.Run(ctx, db.Read, "list", func(ctx context.Context) error {
		return db.InTx(ctx, s.pools.Reader, true, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, columns+"ORDER BY id DESC LIMIT ?", n)
			if err != nil {
				return err
			}
			defer rows.Close()
			list := []Order{} // [] instead of null in JSON
			for rows.Next() {
				o, err := scanOrder(rows)
				if err != nil {
					return err
				}
				list = append(list, o)
			}
			res.Value = list
			return rows.Err()
		})
	})
	res.Attempts = attempts
	return res, err
}

func (s *Service) UpdateQty(ctx context.Context, id int64, qty int) (ReadResult[*Order], error) {
	var res ReadResult[*Order]
	attempts, err := s.retry.Run(ctx, db.Write, "update", func(ctx context.Context) error {
		return db.InTx(ctx, s.pools.Writer, false, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "UPDATE orders SET qty = ? WHERE id = ?", qty, id); err != nil {
				return err
			}
			var err error
			res.Value, err = findByID(ctx, tx, id)
			return err
		})
	})
	res.Attempts = attempts
	return res, err
}

func (s *Service) Delete(ctx context.Context, id int64) (ReadResult[bool], error) {
	var res ReadResult[bool]
	attempts, err := s.retry.Run(ctx, db.Write, "delete", func(ctx context.Context) error {
		return db.InTx(ctx, s.pools.Writer, false, func(tx *sql.Tx) error {
			r, err := tx.ExecContext(ctx, "DELETE FROM orders WHERE id = ?", id)
			if err != nil {
				return err
			}
			n, err := r.RowsAffected()
			res.Value = n > 0
			return err
		})
	})
	res.Attempts = attempts
	return res, err
}

// findByID returns nil (not an error) when the row does not exist: "not found"
// is a normal answer and must not count as a failed operation.
func findByID(ctx context.Context, tx *sql.Tx, id int64) (*Order, error) {
	o, err := scanOrder(tx.QueryRowContext(ctx, columns+"WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanOrder(r scanner) (Order, error) {
	var o Order
	err := r.Scan(&o.ID, &o.RequestID, &o.Item, &o.Qty, &o.WrittenOn, &o.CreatedAt, &o.UpdatedAt, &o.ReadFrom)
	return o, err
}

// newUUID returns a random version-4 UUID (36 chars, fits request_id CHAR(36)).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
