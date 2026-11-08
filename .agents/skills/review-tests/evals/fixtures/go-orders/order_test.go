package orders

import (
	"os"
	"testing"
)

// testDB is opened once and shared by every test in the package.
var testDB *DB

func TestMain(m *testing.M) {
	testDB = OpenDB()
	os.Exit(m.Run())
}

func TestPlaceOrder(t *testing.T) {
	_, err := PlaceOrder("A-1", []Line{{SKU: "widget", Qty: 2, Cents: 500}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPlaceOrder_AppliesVolumeDiscount(t *testing.T) {
	o, err := PlaceOrder("A-2", []Line{{SKU: "widget", Qty: 30, Cents: 500}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Total != 13500 {
		t.Errorf("Total = %d, want 13500", o.Total)
	}
}

func TestPlaceOrder_EmptyCart(t *testing.T) {
	_, err := PlaceOrder("A-3", nil)
	if err != ErrEmptyCart {
		t.Errorf("err = %v, want ErrEmptyCart", err)
	}
}

func TestSaveOrder(t *testing.T) {
	if err := testDB.Save(Order{ID: "S-1", Status: "placed", Total: 100}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if testDB.Count() != 1 {
		t.Errorf("Count = %d, want 1", testDB.Count())
	}
}

func TestSaveOrder_Duplicate(t *testing.T) {
	err := testDB.Save(Order{ID: "S-1", Status: "placed", Total: 100})
	if err == nil {
		t.Error("expected duplicate error")
	}
}
