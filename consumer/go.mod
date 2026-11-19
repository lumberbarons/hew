// The consumer module is deliberately not part of the hew module: it can name
// only the public packages, so a test here fails to compile the moment a
// workflow needs an internal type. The go directive matches the root module's,
// which is the minimum toolchain a consumer is promised.
module github.com/lumberbarons/hew/consumer

go 1.26.11

require github.com/lumberbarons/hew v0.0.0

require (
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/cli/go-gh/v2 v2.17.0 // indirect
	github.com/cli/safeexec v1.0.1 // indirect
	github.com/cli/shurcooL-graphql v0.0.4 // indirect
	github.com/henvic/httpretty v0.2.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/thlib/go-timezone-local v0.0.9 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/term v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/lumberbarons/hew => ../
