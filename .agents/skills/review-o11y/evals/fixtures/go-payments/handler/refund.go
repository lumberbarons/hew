package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"example.com/payments/repo"
	"example.com/payments/stripe"
)

type RefundHandler struct {
	orders *repo.Orders
	stripe *stripe.Client
}

func (h *RefundHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID string `json:"order_id"`
		Uid     string `json:"uid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("Could not decode refund request.", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	order, err := h.orders.Get(r.Context(), req.OrderID)
	if err != nil {
		slog.Error("Could not find order.", "order_id", req.OrderID, "uid", req.Uid)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if _, err := h.stripe.Refund(r.Context(), order.ChargeID); err != nil {
		slog.Error("refund failed", "err", err)
		http.Error(w, "refund failed", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "refunded"})
}
