package checkout

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

// Handler serves the checkout HTTP endpoints.
type Handler struct {
	svc  *Service
	repo *OrderRepo
}

// NewHandler returns a handler for the checkout endpoints.
func NewHandler(svc *Service, repo *OrderRepo) *Handler {
	return &Handler{svc: svc, repo: repo}
}

// Routes registers the checkout endpoints on mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /checkout", h.processOrder)
	mux.HandleFunc("POST /checkout/refund", h.refund)
	mux.HandleFunc("GET /checkout/quote", h.quote)
}

// processOrder handles a customer submitting the checkout form.
func (h *Handler) processOrder(w http.ResponseWriter, r *http.Request) {
	var req PlaceOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	order, err := h.svc.PlaceOrder(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(order); err != nil {
		log.Printf("encode order: %v", err)
	}
}

// quote returns the price a customer would pay without placing an order.
func (h *Handler) quote(w http.ResponseWriter, r *http.Request) {
	sku := r.URL.Query().Get("sku")
	qty, err := strconv.Atoi(r.URL.Query().Get("qty"))
	if err != nil || qty <= 0 {
		http.Error(w, "qty must be a positive integer", http.StatusBadRequest)
		return
	}

	unit, err := h.svc.catalog.UnitCents(r.Context(), sku)
	if err != nil {
		http.Error(w, "unknown sku", http.StatusNotFound)
		return
	}

	gross := unit * int64(qty)
	var fraction float64
	if qty >= 100 {
		fraction = 0.15
	} else if qty >= 50 {
		fraction = 0.10
	} else if qty >= 10 {
		fraction = 0.05
	}
	subtotal := gross - int64(float64(gross)*fraction)
	rate := h.svc.rates[r.URL.Query().Get("region")]
	total := subtotal + int64(float64(subtotal)*rate+0.5)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int64{"total_cents": total}); err != nil {
		log.Printf("encode quote: %v", err)
	}
}

// refund voids an order and returns the money.
func (h *Handler) refund(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "id must be an integer", http.StatusBadRequest)
		return
	}

	rows, err := h.repo.Query(r.Context(),
		`UPDATE orders SET refunded = true WHERE id = $1 RETURNING total_cents`, id)
	if err != nil {
		http.Error(w, "refund failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var cents int64
	if rows.Next() {
		if err := rows.Scan(&cents); err != nil {
			http.Error(w, "refund failed", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int64{"refunded_cents": cents}); err != nil {
		log.Printf("encode refund: %v", err)
	}
}
