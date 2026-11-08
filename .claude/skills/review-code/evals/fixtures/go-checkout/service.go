package checkout

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// clock is swapped out in tests to pin timestamps.
var clock = time.Now

// Order is a placed order.
type Order struct {
	ID         int64
	SKU        string
	Qty        int
	Region     string
	TotalCents int64
	TaxCents   int64
	PlacedAt   time.Time
}

// Notifier delivers order confirmations.
type Notifier interface {
	Notify(ctx context.Context, to, subject, body string) error
}

// Catalog looks up sellable items.
type Catalog interface {
	UnitCents(ctx context.Context, sku string) (int64, error)
}

// Service places orders.
type Service struct {
	repo     *OrderRepo
	catalog  Catalog
	notifier Notifier
	rates    map[string]float64
}

// NewService wires the dependencies an order service needs.
func NewService(repo *OrderRepo, catalog Catalog, notifier Notifier) *Service {
	return &Service{repo: repo, catalog: catalog, notifier: notifier}
}

// Init loads the tax rate table. Call it once at startup, before PlaceOrder.
func (s *Service) Init(ctx context.Context) error {
	rates, err := s.repo.LoadTaxRates(ctx)
	if err != nil {
		return err
	}
	s.rates = rates
	return nil
}

// PlaceOrder validates the request, prices it, stores it and emails the
// customer a confirmation.
func (s *Service) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*Order, error) {
	// --- validation ---
	if strings.TrimSpace(req.SKU) == "" {
		return nil, errors.New("sku is required")
	}
	if req.Qty <= 0 {
		return nil, errors.New("qty must be positive")
	}
	if req.Qty > 10000 {
		return nil, errors.New("qty exceeds per-order maximum")
	}
	if strings.TrimSpace(req.Region) == "" {
		return nil, errors.New("region is required")
	}
	if !strings.Contains(req.Email, "@") {
		return nil, errors.New("email is not valid")
	}
	if req.Qty > 500 && req.Region == "EU" {
		return nil, errors.New("bulk EU orders require manual approval")
	}

	// --- pricing ---
	unit, err := s.catalog.UnitCents(ctx, req.SKU)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", req.SKU, err)
	}
	gross := unit * int64(req.Qty)
	var fraction float64
	switch {
	case req.Qty >= 100:
		fraction = 0.15
	case req.Qty >= 50:
		fraction = 0.10
	case req.Qty >= 10:
		fraction = 0.05
	}
	subtotal := gross - int64(float64(gross)*fraction)
	if req.CouponCode != "" {
		if strings.HasPrefix(req.CouponCode, "SAVE") {
			subtotal = subtotal - 500
		}
		if subtotal < 0 {
			subtotal = 0
		}
	}
	rate := s.rates[req.Region]
	tax := int64(float64(subtotal)*rate + 0.5)

	order := &Order{
		SKU:        req.SKU,
		Qty:        req.Qty,
		Region:     req.Region,
		TotalCents: subtotal + tax,
		TaxCents:   tax,
		PlacedAt:   clock(),
	}

	// --- persistence ---
	id, err := s.repo.Insert(ctx, order)
	if err != nil {
		return nil, err
	}
	order.ID = id

	// --- notification ---
	body := fmt.Sprintf("Thanks for your order of %d x %s. Total: %d cents.",
		order.Qty, order.SKU, order.TotalCents)
	if err := s.notifier.Notify(ctx, req.Email, "Order confirmed", body); err != nil {
		return order, nil
	}

	return order, nil
}

// PlaceOrderRequest is the input to PlaceOrder.
type PlaceOrderRequest struct {
	SKU        string
	Qty        int
	Region     string
	Email      string
	CouponCode string
}
