package usecase

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProviderEvidencePostACKSourceGuard(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(testFile), "send.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse send.go: %v", err)
	}

	var wrap *ast.FuncDecl
	var helper *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch function.Name.Name {
		case "wrapSendMessage":
			wrap = function
		case "recordProviderMessagePresentAfterACK":
			helper = function
		}
	}
	if wrap == nil || helper == nil {
		t.Fatalf("post-ACK boundary missing: wrap=%t helper=%t", wrap != nil, helper != nil)
	}

	var errorGateEnd token.Pos
	for _, statement := range wrap.Body.List {
		ifStatement, ok := statement.(*ast.IfStmt)
		if !ok {
			continue
		}
		ident, ok := ifStatement.Cond.(*ast.BinaryExpr)
		if !ok {
			continue
		}
		left, ok := ident.X.(*ast.Ident)
		if ok && left.Name == "err" {
			errorGateEnd = ifStatement.End()
			break
		}
	}

	var helperCallPositions []token.Pos
	var directEvidenceCalls int
	ast.Inspect(wrap.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "recordProviderMessagePresentAfterACK":
			helperCallPositions = append(helperCallPositions, call.Pos())
		case "RecordProviderMessagePresent":
			directEvidenceCalls++
		}
		return true
	})
	if errorGateEnd == token.NoPos || len(helperCallPositions) != 1 || helperCallPositions[0] <= errorGateEnd || directEvidenceCalls != 0 {
		t.Fatalf("post-ACK ordering gate=%v helper_calls=%v direct_calls=%d", errorGateEnd, helperCallPositions, directEvidenceCalls)
	}

	var helperEvidenceCalls int
	ast.Inspect(helper.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "RecordProviderMessagePresent" {
			helperEvidenceCalls++
		}
		return true
	})
	if helperEvidenceCalls != 1 {
		t.Fatalf("post-ACK helper evidence calls = %d, want one", helperEvidenceCalls)
	}
}
