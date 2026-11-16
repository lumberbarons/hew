// Package consumer is hew's external-consumer module: a Go module outside
// github.com/lumberbarons/hew that imports only its public packages, tracker,
// pr and migrate. It is the completion gate for the library extraction in
// DESIGN.md ("Tracker library"): internal unit tests can pass while a public
// operation is missing or returns an internal error type, but a module that
// cannot import an internal path fails to compile instead.
//
// The tests are the scenarios a consumer runs — initialization, reads and
// queries, creation, claims, dependencies, plans, pull requests, both
// migrations — against a real local tracker and an isolated GitHub transport.
// Nothing here executes the hew binary, reads a gh credential or reaches the
// network; CI runs the module with `go test -race ./...` on a PATH without hew.
package consumer
