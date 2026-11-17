// This empty module marks web/ as separate from the daemon module so Go
// tooling (go test ./..., go mod tidy, golangci-lint) skips web/node_modules,
// where some npm packages ship .go files. The daemon embeds the built UI from
// ../dist, which belongs to the daemon module.
module github.com/lumberbarons/hew/daemon/web

go 1.26.11
