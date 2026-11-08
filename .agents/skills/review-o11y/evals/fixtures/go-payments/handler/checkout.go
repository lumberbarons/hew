package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"example.com/payments/repo"
	"example.com/payments/stripe"
)

type CheckoutHandler struct {
	orders *repo.Orders
	stripe *stripe.Client
}

func (h *CheckoutHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
		CardNo string `json:"card_no"`
		Amount int    `json:"amount_cents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("failed to decode checkout request", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if req.CardNo == "" {
		slog.Error("missing card number in checkout", "user_id", req.UserID)
		http.Error(w, "card required", http.StatusBadRequest)
		return
	}

	order, err := h.orders.Create(r.Context(), req.UserID, req.Amount)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to create order: %s", err.Error()), "user_id", req.UserID)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	charge, err := h.stripe.Charge(r.Context(), req.CardNo, req.Amount)
	if err != nil {
		slog.Error("charge failed", "err", err, "card_no", req.CardNo, "user_id", req.UserID)
		http.Error(w, "payment failed", http.StatusPaymentRequired)
		return
	}

	if err := h.orders.MarkPaid(r.Context(), order.ID, charge.ID); err != nil {
		slog.Error("mark paid failed", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"order_id": order.ID})
}
