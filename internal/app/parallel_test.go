package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryTestSaysWhetherItRunsInParallel: the package's tests run side
// by side, so each test calls t.Parallel() first, or says why it cannot
// in a comment starting "Serial:" before its first statement. A test that
// changes what the whole process shares cannot: its environment
// (t.Setenv; setenv changes the test's own, envOf), its working
// directory, the standard logger (captureLog), a package variable, or one
// that measures the process (its allocations, its open files). The
// serial ones run first, one at a time, before the parallel ones start.
func TestEveryTestSaysWhetherItRunsInParallel(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join(pkgDir, "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files in %s: %v", pkgDir, err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "Test") || fd.Name.Name == "TestMain" {
				continue
			}
			param := testingParam(fd)
			if param == "" {
				continue
			}
			if !callsParallelFirst(fd, param) && !saysSerial(f, fd) {
				t.Errorf("%s (%s): call %s.Parallel() first, or say why it cannot run in parallel in a // Serial: comment before its first statement",
					fd.Name.Name, filepath.Base(name), param)
			}
		}
	}
}

// testingParam is the name of fd's *testing.T parameter, or "" when fd
// takes no single *testing.T.
func testingParam(fd *ast.FuncDecl) string {
	params := fd.Type.Params.List
	if len(params) != 1 || len(params[0].Names) != 1 {
		return ""
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return ""
	}
	return params[0].Names[0].Name
}

// callsParallelFirst reports whether fd's first statement is
// param.Parallel().
func callsParallelFirst(fd *ast.FuncDecl, param string) bool {
	if len(fd.Body.List) == 0 {
		return false
	}
	stmt, ok := fd.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == param
}

// saysSerial reports whether fd's body opens with a comment that starts
// "Serial:", before its first statement.
func saysSerial(f *ast.File, fd *ast.FuncDecl) bool {
	end := fd.Body.Rbrace
	if len(fd.Body.List) > 0 {
		end = fd.Body.List[0].Pos()
	}
	for _, g := range f.Comments {
		if g.Pos() > fd.Body.Lbrace && g.End() <= end && strings.HasPrefix(g.Text(), "Serial:") {
			return true
		}
	}
	return false
}
