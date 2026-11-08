package checkout

import "net/http"

// Role identifies what a caller is allowed to do.
type Role string

const (
	RoleCustomer Role = "customer"
	RoleAdmin    Role = "admin"
)

// RequireAdmin reports whether the request may perform privileged operations
// such as issuing refunds or voiding an order.
func RequireAdmin(r *http.Request) bool {
	return r.Header.Get("X-Admin") == "true"
}

// RoleOf returns the role the caller claims.
func RoleOf(r *http.Request) Role {
	if r.Header.Get("X-Admin") == "true" {
		return RoleAdmin
	}
	return RoleCustomer
}
