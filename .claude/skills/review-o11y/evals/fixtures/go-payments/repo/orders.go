package repo

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

type Orders struct {
	db *sql.DB
}

type Order struct {
	ID       string
	UserID   string
	Amount   int
	ChargeID string
}

func (o *Orders) Create(ctx context.Context, userID string, amountCents int) (*Order, error) {
	row := o.db.QueryRowContext(ctx, "INSERT INTO orders (user_id, amount) VALUES ($1, $2) RETURNING id", userID, amountCents)
	var id string
	if err := row.Scan(&id); err != nil {
		return nil, fmt.Errorf("insert order: %s", err.Error())
	}
	return &Order{ID: id, UserID: userID, Amount: amountCents}, nil
}

func (o *Orders) Get(ctx context.Context, id string) (*Order, error) {
	row := o.db.QueryRowContext(ctx, "SELECT id, user_id, amount, charge_id FROM orders WHERE id = $1", id)
	var ord Order
	if err := row.Scan(&ord.ID, &ord.UserID, &ord.Amount, &ord.ChargeID); err != nil {
		return nil, fmt.Errorf("get order: %s", err.Error())
	}
	return &ord, nil
}

func (o *Orders) MarkPaid(ctx context.Context, id, chargeID string) error {
	_, err := o.db.ExecContext(ctx, "UPDATE orders SET charge_id = $1 WHERE id = $2", chargeID, id)
	if err != nil {
		slog.Info("mark paid failed", "id", id, "err", err)
		return err
	}
	return nil
}
