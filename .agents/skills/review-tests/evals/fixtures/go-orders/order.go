package orders

import (
	"errors"
	"strings"
)

var ErrEmptyCart = errors.New("cart is empty")
var ErrUnknownSKU = errors.New("unknown sku")

type Line struct {
	SKU   string
	Qty   int
	Cents int
}

type Order struct {
	ID     string
	Status string
	Total  int
	Lines  []Line
}

// PlaceOrder builds an order from cart lines, applying the volume discount.
func PlaceOrder(id string, lines []Line) (Order, error) {
	if len(lines) == 0 {
		return Order{}, ErrEmptyCart
	}
	total := 0
	for i := range lines {
		lines[i].SKU = normalizeSKU(lines[i].SKU)
		if lines[i].SKU == "" {
			return Order{}, ErrUnknownSKU
		}
		total += lines[i].Cents * lines[i].Qty
	}
	total = ApplyDiscount(total)
	return Order{ID: id, Status: "placed", Total: total, Lines: lines}, nil
}

// ApplyDiscount takes 10% off orders of 10000 cents or more.
func ApplyDiscount(cents int) int {
	if cents >= 10000 {
		return cents - cents/10
	}
	return cents
}

// CancelOrder transitions a placed order to cancelled and zeroes the total.
func CancelOrder(o Order) (Order, error) {
	if o.Status != "placed" {
		return o, errors.New("only placed orders can be cancelled")
	}
	o.Status = "cancelled"
	o.Total = 0
	return o, nil
}

func normalizeSKU(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
