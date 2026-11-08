package orders

import "fmt"

type DB struct {
	rows map[string]Order
}

func OpenDB() *DB {
	return &DB{rows: map[string]Order{}}
}

func (d *DB) Save(o Order) error {
	if _, exists := d.rows[o.ID]; exists {
		return fmt.Errorf("duplicate order %s", o.ID)
	}
	d.rows[o.ID] = o
	return nil
}

func (d *DB) Get(id string) (Order, bool) {
	o, ok := d.rows[id]
	return o, ok
}

func (d *DB) Count() int {
	return len(d.rows)
}
