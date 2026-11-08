package orders

import "testing"

type fakeNotifier struct {
	calls   int
	lastID  string
	reasons []string
}

func (f *fakeNotifier) Notify(orderID, reason string) error {
	f.calls++
	f.lastID = orderID
	f.reasons = append(f.reasons, reason)
	return nil
}

func TestHandler(t *testing.T) {
	n := &fakeNotifier{}
	h := &Handler{DB: OpenDB(), Notifier: n}

	_, err := h.Checkout("H-1", nil)
	if err != ErrEmptyCart {
		t.Fatalf("err = %v, want ErrEmptyCart", err)
	}
	if n.calls != 1 {
		t.Errorf("calls = %d, want 1", n.calls)
	}
}

func TestCheckout_PersistsOrder(t *testing.T) {
	n := &fakeNotifier{}
	db := OpenDB()
	h := &Handler{DB: db, Notifier: n}

	o, err := h.Checkout("H-2", []Line{{SKU: "bolt", Qty: 1, Cents: 250}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Total != 250 {
		t.Errorf("Total = %d, want 250", o.Total)
	}
	if _, ok := db.Get("H-2"); !ok {
		t.Error("order not persisted")
	}
}
