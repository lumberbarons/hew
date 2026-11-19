// This file is done-when's contract-and-boundary item, checked rather than
// asserted: the consumer module follows the documented toolchain, the public
// packages' production dependency graph excludes the CLI and renderers, the
// consumer imports nothing internal, and the names DESIGN.md
// promises are exported.
package consumer_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// goDirective returns the `go` line of a go.mod.
func goDirective(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(raw)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("%s carries no go directive", path)
	return ""
}

// TestConsumerModuleFollowsTheDocumentedToolchain is done-when's first item's
// toolchain half: the consumer module declares the same minimum Go release as
// the library it consumes, because that directive is the promise.
func TestConsumerModuleFollowsTheDocumentedToolchain(t *testing.T) {
	root := goDirective(t, filepath.Join("..", "go.mod"))
	consumer := goDirective(t, "go.mod")
	if consumer != root {
		t.Errorf("consumer go directive = %q, library = %q; the consumer must follow the documented minimum", consumer, root)
	}
}

// TestPublicDependencyGraphExcludesTheAdapter is done-when's first item's
// dependency half: a production build of each public package pulls no CLI,
// renderer or command wiring.
func TestPublicDependencyGraphExcludesTheAdapter(t *testing.T) {
	forbidden := []string{
		"github.com/lumberbarons/hew/internal/cli",
		"github.com/lumberbarons/hew/internal/render",
		"github.com/lumberbarons/hew/cmd/hew",
	}
	for _, pkg := range []string{"tracker", "pr", "migrate"} {
		t.Run(pkg, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-deps", "github.com/lumberbarons/hew/"+pkg)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps: %v", err)
			}
			deps := strings.Fields(string(out))
			for _, dep := range deps {
				if slices.Contains(forbidden, dep) {
					t.Errorf("%s depends on %s", pkg, dep)
				}
			}
		})
	}
}

// TestConsumerModuleImportsOnlyPublicPackages is the module boundary itself:
// every hew import in this module names tracker, pr or migrate, never an
// internal path or the command wiring.
func TestConsumerModuleImportsOnlyPublicPackages(t *testing.T) {
	allowed := map[string]bool{
		"github.com/lumberbarons/hew/tracker": true,
		"github.com/lumberbarons/hew/pr":      true,
		"github.com/lumberbarons/hew/migrate": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range parsed.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			if !strings.HasPrefix(imported, "github.com/lumberbarons/hew/") {
				continue
			}
			if !allowed[imported] {
				t.Errorf("%s imports %s; the consumer module may import only the public packages", path, imported)
			}
		}
	}
}

// packageSurface collects a package's exported names and the methods each
// exported type declares, from its non-test files.
func packageSurface(t *testing.T, dir string) (map[string]bool, map[string]map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	methods := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil {
					names[decl.Name.Name] = true
					continue
				}
				recv := decl.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				ident, ok := recv.(*ast.Ident)
				if !ok {
					continue
				}
				if methods[ident.Name] == nil {
					methods[ident.Name] = map[string]bool{}
				}
				methods[ident.Name][decl.Name.Name] = true
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						names[spec.Name.Name] = true
					case *ast.ValueSpec:
						for _, ident := range spec.Names {
							names[ident.Name] = true
						}
					}
				}
			}
		}
	}
	return names, methods
}

// exportedNames collects the exported type, func, const and var names a package
// declares in its non-test files, including methods.
func exportedNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	names, methods := packageSurface(t, dir)
	for _, declared := range methods {
		for method := range declared {
			names[method] = true
		}
	}
	return names
}

// designContractTypes parses the "Types an external module names" table in
// DESIGN.md into package → names.
func designContractTypes(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := string(raw)
	start := strings.Index(section, "### Types an external module names")
	if start < 0 {
		t.Fatal("DESIGN.md has no types section")
	}
	section = section[start:]
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}
	contract := map[string][]string{}
	row := regexp.MustCompile("^\\| `(tracker|pr|migrate)` \\|(.*)\\|(.*)\\|\\s*$")
	name := regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")
	for line := range strings.Lines(section) {
		match := row.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		for _, cell := range match[2:] {
			for _, found := range name.FindAllStringSubmatch(cell, -1) {
				contract[match[1]] = append(contract[match[1]], found[1])
			}
		}
	}
	if len(contract) != 3 {
		t.Fatalf("DESIGN.md types table parsed %d package rows, want 3", len(contract))
	}
	return contract
}

// TestDesignContractNamesExportedTypes is done-when's contract item: every
// request, result and error type DESIGN.md tells a consumer to name is exported
// by the package it attributes it to.
func TestDesignContractNamesExportedTypes(t *testing.T) {
	contract := designContractTypes(t)
	for pkg, promised := range contract {
		exported := exportedNames(t, filepath.Join("..", pkg))
		for _, want := range promised {
			if !exported[want] {
				t.Errorf("DESIGN.md promises %s.%s, which the package does not export", pkg, want)
			}
		}
	}
}

// TestDesignOperationInventoryIsCallable is done-when's contract item for the
// operation inventory: every command's public operation is a real exported
// function or method, so no workflow in the inventory lives only in the CLI.
func TestDesignOperationInventoryIsCallable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "### Operation inventory")
	if start < 0 {
		t.Fatal("DESIGN.md has no operation inventory")
	}
	text = text[start:]
	if end := strings.Index(text, "\nTwo supporting capabilities"); end >= 0 {
		text = text[:end]
	}

	type surface struct {
		names   map[string]bool
		methods map[string]map[string]bool
	}
	surfaces := map[string]surface{}
	for _, pkg := range []string{"tracker", "pr", "migrate"} {
		names, methods := packageSurface(t, filepath.Join("..", pkg))
		surfaces[pkg] = surface{names: names, methods: methods}
	}

	receiver := regexp.MustCompile(`^\(\*(\w+)\.(\w+)\)\.(\w+)`)
	function := regexp.MustCompile(`^(\w+)\.(\w+)\(`)
	backtick := regexp.MustCompile("`([^`]+)`")
	checked := 0
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		columns := strings.Split(line, "|")
		if len(columns) < 4 {
			continue
		}
		for _, token := range backtick.FindAllStringSubmatch(columns[2], -1) {
			code := token[1]
			if match := receiver.FindStringSubmatch(code); match != nil {
				pkg, typ, method := match[1], match[2], match[3]
				s, ok := surfaces[pkg]
				if !ok {
					continue
				}
				checked++
				if !s.methods[typ][method] {
					t.Errorf("DESIGN.md names (*%s.%s).%s, which is not an exported method", pkg, typ, method)
				}
				continue
			}
			if match := function.FindStringSubmatch(code); match != nil {
				pkg, fn := match[1], match[2]
				s, ok := surfaces[pkg]
				if !ok {
					continue
				}
				checked++
				if !s.names[fn] {
					t.Errorf("DESIGN.md names %s.%s, which is not an exported function", pkg, fn)
				}
			}
		}
	}
	if checked < 20 {
		t.Fatalf("the inventory check found %d operations, want the whole table", checked)
	}
}
