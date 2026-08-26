package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InTx runs fn inside a transaction, committing if it returns nil and rolling
// back otherwise.
//
// The Querier handed to fn is the transaction, so repositories constructed
// with it write through the same transaction and compose atomically:
//
//	err := db.InTx(ctx, pool, func(q db.Querier) error {
//	    libs := repository.NewLibraryRepository(q)
//	    if err := libs.Create(ctx, &lib); err != nil {
//	        return err
//	    }
//	    return libs.AddPath(ctx, &path)
//	})
//
// The rollback is unconditional in a defer rather than only on the error path:
// a panic inside fn must not leave the transaction open, holding locks until
// the connection is reaped.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(Querier) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	defer func() {
		// Rolling back an already-committed transaction is a no-op that
		// returns pgx.ErrTxClosed, so this is safe on the success path.
		// context.WithoutCancel keeps the rollback working when fn failed
		// because ctx was cancelled.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// compile-time assertions that the types callers pass to repositories really
// do satisfy Querier. A pgx upgrade that changed either signature would
// otherwise surface as a confusing error at every call site.
var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)
