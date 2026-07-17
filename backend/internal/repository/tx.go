package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// dbtx is the subset of *pgxpool.Pool and pgx.Tx used by repositories that
// support running inside a caller-managed transaction (via WithTx), so a
// batch of related writes can share a single commit instead of paying one
// round trip (and one WAL fsync) per statement.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
