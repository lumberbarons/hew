// The daemon module is a scratch proof that the public tracker, pr and migrate
// packages are enough to build a long-running server. It deliberately cannot
// import internal/*. Not part of CI, coverage or releases.
module github.com/lumberbarons/hew/daemon

go 1.26.11

require github.com/lumberbarons/hew v0.0.0

require (
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/cli/go-gh/v2 v2.16.1 // indirect
	github.com/cli/safeexec v1.0.1 // indirect
	github.com/cli/shurcooL-graphql v0.0.4 // indirect
	github.com/henvic/httpretty v0.2.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/thlib/go-timezone-local v0.0.8 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/lumberbarons/hew => ../
