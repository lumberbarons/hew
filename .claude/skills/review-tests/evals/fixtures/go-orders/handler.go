package orders

// Notifier is told why an order reached a terminal state.
type Notifier interface {
	Notify(orderID, reason string) error
}

type Handler struct {
	DB       *DB
	Notifier Notifier
}

// Checkout places an order, persists it, and notifies on rejection.
func (h *Handler) Checkout(id string, lines []Line) (Order, error) {
	o, err := PlaceOrder(id, lines)
	if err != nil {
		_ = h.Notifier.Notify(id, "rejected: "+err.Error())
		return Order{}, err
	}
	if err := h.DB.Save(o); err != nil {
		_ = h.Notifier.Notify(id, "rejected: duplicate")
		return Order{}, err
	}
	return o, nil
}
