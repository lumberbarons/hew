// The evals harnesses live in their own module so the tokenizer's embedded
// vocabularies never reach the CLI's dependency graph: `go install
// .../cmd/hew@latest` should pull go-gh and urfave/cli, nothing else.
module github.com/lumberbarons/hew/evals

go 1.25.0

require github.com/tiktoken-go/tokenizer v0.7.0

require github.com/dlclark/regexp2 v1.11.5 // indirect
