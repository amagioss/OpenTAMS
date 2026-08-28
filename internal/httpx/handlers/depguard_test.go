package handlers_test

// SCN-HTTP-41 — Handler does not call api.As*/api.From* (depguard backstop)
//
// Trace: REQ-HTTP-23, INV-HTTP-11, BR-HTTP-18.
//
// The handler MUST NOT call any oapi-codegen union accessor (`AsXxx`
// or `FromXxx`). All wire ↔ domain hops go through
// `internal/httpx/conversion`. depguard catches the import-shape part
// of this rule (it can forbid packages, but not symbol references);
// this test is the runtime backstop that scans handler sources for
// the call expressions themselves.
//
// The check is type-aware: it loads the handlers package via
// `golang.org/x/tools/go/packages`, walks every `*ast.CallExpr` in
// non-test source files, and flags a call only when its selector's
// receiver type lives in the `gen/api` package. A bare-name match
// would fire on innocuous calls like `logger.FromContext` and
// `errors.As`; the type check eliminates those false positives.

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	apiPkgPath = "github.com/amagioss/opentams/gen/api"
	handlerPkg = "github.com/amagioss/opentams/internal/httpx/handlers"
)

func Test_SCN_HTTP_41_HandlerDoesNotCallApiUnion(t *testing.T) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
			packages.NeedImports,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, handlerPkg)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("package load reported errors")
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	pkg := pkgs[0]

	var offences []string
	for i, file := range pkg.Syntax {
		path := pkg.GoFiles[i]
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !isUnionAccessor(sel.Sel.Name) {
				return true
			}
			if !receiverInAPIPackage(pkg.TypesInfo, sel) {
				return true
			}
			pos := pkg.Fset.Position(sel.Pos())
			offences = append(offences,
				filepath.Base(pos.Filename)+":"+itoa(pos.Line)+" — "+sel.Sel.Name)
			return true
		})
	}

	if len(offences) > 0 {
		t.Fatalf("INV-HTTP-11 violated: handler must not call As*/From* union accessors on api.* types (route via internal/httpx/conversion):\n  %s",
			strings.Join(offences, "\n  "))
	}
}

// receiverInAPIPackage reports whether the selector's receiver expression
// has a type whose underlying named type is declared in `gen/api`.
// Pointer receivers and aliases are unwrapped.
func receiverInAPIPackage(info *types.Info, sel *ast.SelectorExpr) bool {
	tv, ok := info.Types[sel.X]
	if !ok {
		return false
	}
	t := tv.Type
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == apiPkgPath
}

// isUnionAccessor matches `AsXxx` / `FromXxx` where the suffix begins with
// an uppercase letter — the oapi-codegen union helper naming pattern.
func isUnionAccessor(name string) bool {
	switch {
	case strings.HasPrefix(name, "As") && len(name) > 2 && isUpper(name[2]):
		return true
	case strings.HasPrefix(name, "From") && len(name) > 4 && isUpper(name[4]):
		return true
	}
	return false
}

func isUpper(b byte) bool { return b >= 'A' && b <= 'Z' }

// itoa avoids a strconv import for one line position.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
