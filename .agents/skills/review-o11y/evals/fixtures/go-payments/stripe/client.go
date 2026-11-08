package stripe

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Client struct {
	apiKey string
}

type Charge struct {
	ID string
}

func (c *Client) Charge(ctx context.Context, cardNo string, amountCents int) (*Charge, error) {
	return c.withRetry(ctx, func() (*Charge, error) {
		return c.doCharge(ctx, cardNo, amountCents)
	})
}

func (c *Client) Refund(ctx context.Context, chargeID string) (*Charge, error) {
	return c.withRetry(ctx, func() (*Charge, error) {
		return c.doRefund(ctx, chargeID)
	})
}

func (c *Client) withRetry(ctx context.Context, fn func() (*Charge, error)) (*Charge, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		charge, err := fn()
		if err == nil {
			return charge, nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	slog.Error("stripe call failed after retries", "err", lastErr)
	return nil, fmt.Errorf("stripe call failed: %s", lastErr.Error())
}

func (c *Client) doCharge(ctx context.Context, cardNo string, amountCents int) (*Charge, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *Client) doRefund(ctx context.Context, chargeID string) (*Charge, error) {
	return nil, fmt.Errorf("not implemented")
}
