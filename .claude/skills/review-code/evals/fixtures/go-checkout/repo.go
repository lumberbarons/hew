package checkout

import (
	"context"
	"database/sql"
	"fmt"
)

// OrderRepo persists orders.
type OrderRepo struct {
	db *sql.DB
}

// NewOrderRepo returns a repo backed by db. The caller owns the connection
// lifecycle.
func NewOrderRepo(db *sql.DB) *OrderRepo {
	return &OrderRepo{db: db}
}

// Insert writes o and returns the assigned id.
func (r *OrderRepo) Insert(ctx context.Context, o *Order) (int64, error) {
	row := r.db.QueryRowContext(ctx,
		`INSERT INTO orders (sku, qty, total_cents, tax_cents) VALUES ($1,$2,$3,$4) RETURNING id`,
		o.SKU, o.Qty, o.TotalCents, o.TaxCents)
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, fmt.Errorf("insert order: %w", err)
	}
	return id, nil
}

// LoadTaxRates returns the region -> rate table.
func (r *OrderRepo) LoadTaxRates(ctx context.Context) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT region, rate FROM tax_rates`)
	if err != nil {
		return nil, fmt.Errorf("load tax rates: %w", err)
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var region string
		var rate float64
		if err := rows.Scan(&region, &rate); err != nil {
			return nil, fmt.Errorf("scan tax rate: %w", err)
		}
		out[region] = rate
	}
	return out, rows.Err()
}

// Query runs an arbitrary statement against the orders database and returns the
// raw result set. Used by the reporting and admin handlers.
func (r *OrderRepo) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.db.QueryContext(ctx, query, args...)
}

// Tx exposes the underlying handle so callers can run their own transactions.
func (r *OrderRepo) Tx() *sql.DB {
	return r.db
}
