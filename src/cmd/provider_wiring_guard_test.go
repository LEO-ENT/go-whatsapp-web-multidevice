package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	providerWiringHelper      = "registerProviderLookupRoutes"
	providerCompositionHelper = "registerProviderAndDeviceScopedRoutes"
)

func TestProviderRouteWiringGuard(t *testing.T) {
	sources := productionGoSources(t)
	if defects := providerWiringDefects(sources); len(defects) != 0 {
		t.Fatalf("provider wiring defects: %s", strings.Join(defects, "; "))
	}
}

func TestProviderRouteWiringGuardRejectsBypasses(t *testing.T) {
	canonical := `package cmd
func restServer() {
	registerProviderAndDeviceScopedRoutes(apiGroup, accounts, dm, providerUsecase, registerDeviceScopedRoutes)
}
func registerProviderAndDeviceScopedRoutes(apiGroup fiber.Router, accounts map[string]string, dm *whatsapp.DeviceManager, service domainProvider.IMessageLookupUsecase, registerDeviceScopedRoutes func(fiber.Router)) {
	registerProviderLookupRoutes(apiGroup, accounts, dm, service)
	headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))
	registerDeviceScopedRoutes(headerDeviceGroup)
}
func registerProviderLookupRoutes(apiGroup fiber.Router, accounts map[string]string, dm *whatsapp.DeviceManager, service domainProvider.IMessageLookupUsecase) {
	apiGroup.Use(rest.ProviderLookupPath, providerLookupAuthMiddleware(accounts), middleware.OpaqueDeviceMiddleware(dm))
	rest.InitRestProvider(apiGroup, service)
}`

	mutants := map[string]map[string]string{
		"extra direct mount in another production file": {
			"rest.go":  canonical,
			"extra.go": `package cmd; func extra() { rest.InitRestProvider(apiGroup, service) }`,
		},
		"function value alias hides an extra mount": {
			"rest.go":  canonical,
			"extra.go": `package cmd; var mountProvider = rest.InitRestProvider; func extra() { mountProvider(apiGroup, service) }`,
		},
		"protected decoy router does not secure mounted router": {
			"rest.go": `package cmd
func restServer() { registerProviderAndDeviceScopedRoutes(apiGroup, accounts, dm, providerUsecase, registerDeviceScopedRoutes) }
func registerProviderAndDeviceScopedRoutes(apiGroup fiber.Router, accounts map[string]string, dm *whatsapp.DeviceManager, service domainProvider.IMessageLookupUsecase, registerDeviceScopedRoutes func(fiber.Router)) {
	registerProviderLookupRoutes(apiGroup, accounts, dm, service)
	headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))
	registerDeviceScopedRoutes(headerDeviceGroup)
}
func registerProviderLookupRoutes(apiGroup fiber.Router, accounts map[string]string, dm *whatsapp.DeviceManager, service domainProvider.IMessageLookupUsecase) {
	decoy := apiGroup.Group("")
	decoy.Use(rest.ProviderLookupPath, providerLookupAuthMiddleware(accounts), middleware.OpaqueDeviceMiddleware(dm))
	rest.InitRestProvider(apiGroup, service)
}`,
		},
		"helper is mounted twice": {
			"rest.go": strings.Replace(canonical,
				"registerProviderAndDeviceScopedRoutes(apiGroup, accounts, dm, providerUsecase, registerDeviceScopedRoutes)",
				"registerProviderAndDeviceScopedRoutes(apiGroup, accounts, dm, providerUsecase, registerDeviceScopedRoutes); registerProviderAndDeviceScopedRoutes(apiGroup, accounts, dm, providerUsecase, registerDeviceScopedRoutes)", 1),
		},
		"auth middleware removed": {
			"rest.go": strings.Replace(canonical, "providerLookupAuthMiddleware(accounts),", "", 1),
		},
		"opaque device middleware downgraded": {
			"rest.go": strings.Replace(canonical, "middleware.OpaqueDeviceMiddleware(dm)", "middleware.DeviceMiddleware(dm)", 1),
		},
		"provider middleware widened to root": {
			"rest.go": strings.Replace(canonical, "rest.ProviderLookupPath", `""`, 1),
		},
		"provider registered after reflective device middleware": {
			"rest.go": strings.Replace(canonical,
				`registerProviderLookupRoutes(apiGroup, accounts, dm, service)
	headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))
	registerDeviceScopedRoutes(headerDeviceGroup)`,
				`headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))
	registerDeviceScopedRoutes(headerDeviceGroup)
	registerProviderLookupRoutes(apiGroup, accounts, dm, service)`, 1),
		},
	}

	for name, sources := range mutants {
		t.Run(name, func(t *testing.T) {
			if defects := providerWiringDefects(sources); len(defects) == 0 {
				t.Fatal("mutant escaped provider wiring guard")
			}
		})
	}
}

func productionGoSources(t *testing.T) map[string]string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	moduleRoot := filepath.Dir(filepath.Dir(testFile))
	sources := make(map[string]string)
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(relative)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("read production Go sources: %v", err)
	}
	return sources
}

func providerWiringDefects(sources map[string]string) []string {
	var defects []string
	var initReferences int
	var initCalls int
	var helperCalls int
	var helperDefinitions int
	var canonicalHelper bool
	var canonicalCallSite bool
	var compositionCalls int
	var compositionDefinitions int
	var canonicalComposition bool
	var canonicalCompositionCallSite bool

	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, sources[path], 0)
		if err != nil {
			defects = append(defects, fmt.Sprintf("%s does not parse: %v", path, err))
			continue
		}
		// Count every production reference, including package-level function-value
		// aliases. Restricting this census to function bodies lets
		// `var mount = rest.InitRestProvider` hide a second mount.
		ast.Inspect(parsed, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "InitRestProvider" {
				initReferences++
			}
			if call, ok := node.(*ast.CallExpr); ok && calledName(call.Fun) == "InitRestProvider" {
				initCalls++
			}
			return true
		})
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			if function.Name.Name == providerWiringHelper {
				helperDefinitions++
				canonicalHelper = canonicalHelper || isCanonicalProviderWiringHelper(function)
			}
			if function.Name.Name == providerCompositionHelper {
				compositionDefinitions++
				canonicalComposition = canonicalComposition || isCanonicalProviderCompositionHelper(function)
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch calledName(call.Fun) {
				case providerWiringHelper:
					helperCalls++
					canonicalCallSite = canonicalCallSite || (function.Name.Name == providerCompositionHelper && hasIdentArgs(call, "apiGroup", "accounts", "dm", "service"))
				case providerCompositionHelper:
					compositionCalls++
					canonicalCompositionCallSite = canonicalCompositionCallSite || (function.Name.Name == "restServer" && hasIdentArgs(call, "apiGroup", "accounts", "dm", "providerUsecase", "registerDeviceScopedRoutes"))
				}
				return true
			})
		}
	}

	if initReferences != 1 || initCalls != 1 {
		defects = append(defects, fmt.Sprintf("InitRestProvider references/calls = %d/%d, want 1/1", initReferences, initCalls))
	}
	if helperDefinitions != 1 || !canonicalHelper {
		defects = append(defects, fmt.Sprintf("canonical helper definitions/valid = %d/%t, want 1/true", helperDefinitions, canonicalHelper))
	}
	if helperCalls != 1 || !canonicalCallSite {
		defects = append(defects, fmt.Sprintf("canonical helper calls/site = %d/%t, want 1/true", helperCalls, canonicalCallSite))
	}
	if compositionDefinitions != 1 || !canonicalComposition {
		defects = append(defects, fmt.Sprintf("canonical composition definitions/valid = %d/%t, want 1/true", compositionDefinitions, canonicalComposition))
	}
	if compositionCalls != 1 || !canonicalCompositionCallSite {
		defects = append(defects, fmt.Sprintf("canonical composition calls/site = %d/%t, want 1/true", compositionCalls, canonicalCompositionCallSite))
	}
	return defects
}

func isCanonicalProviderWiringHelper(function *ast.FuncDecl) bool {
	if len(function.Body.List) != 2 {
		return false
	}
	middlewareStatement, ok := function.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	middlewareCall, ok := middlewareStatement.X.(*ast.CallExpr)
	if !ok || !isSelectorCall(middlewareCall, "apiGroup", "Use") || len(middlewareCall.Args) != 3 || !isSelectorExpr(middlewareCall.Args[0], "rest", "ProviderLookupPath") {
		return false
	}
	if !isIdentCall(middlewareCall.Args[1], "providerLookupAuthMiddleware", "accounts") || !isSelectorExprCall(middlewareCall.Args[2], "middleware", "OpaqueDeviceMiddleware", "dm") {
		return false
	}
	mountStatement, ok := function.Body.List[1].(*ast.ExprStmt)
	if !ok {
		return false
	}
	mountCall, ok := mountStatement.X.(*ast.CallExpr)
	return ok && isSelectorCall(mountCall, "rest", "InitRestProvider") && hasIdentArgs(mountCall, "apiGroup", "service")
}

func isCanonicalProviderCompositionHelper(function *ast.FuncDecl) bool {
	if len(function.Body.List) != 3 {
		return false
	}
	providerStatement, ok := function.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	providerCall, ok := providerStatement.X.(*ast.CallExpr)
	if !ok || !isIdent(providerCall.Fun, providerWiringHelper) || !hasIdentArgs(providerCall, "apiGroup", "accounts", "dm", "service") {
		return false
	}
	assignment, ok := function.Body.List[1].(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 || !isIdent(assignment.Lhs[0], "headerDeviceGroup") {
		return false
	}
	groupCall, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok || !isSelectorCall(groupCall, "apiGroup", "Group") || len(groupCall.Args) != 2 || !isEmptyString(groupCall.Args[0]) || !isSelectorExprCall(groupCall.Args[1], "middleware", "DeviceMiddleware", "dm") {
		return false
	}
	deviceStatement, ok := function.Body.List[2].(*ast.ExprStmt)
	if !ok {
		return false
	}
	deviceCall, ok := deviceStatement.X.(*ast.CallExpr)
	return ok && isIdent(deviceCall.Fun, "registerDeviceScopedRoutes") && hasIdentArgs(deviceCall, "headerDeviceGroup")
}

func calledName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func isIdent(expression ast.Expr, want string) bool {
	ident, ok := expression.(*ast.Ident)
	return ok && ident.Name == want
}

func isSelectorCall(call *ast.CallExpr, receiver string, method string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && isIdent(selector.X, receiver) && selector.Sel.Name == method
}

func isSelectorExpr(expression ast.Expr, receiver string, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && isIdent(selector.X, receiver) && selector.Sel.Name == name
}

func hasIdentArgs(call *ast.CallExpr, names ...string) bool {
	if len(call.Args) != len(names) {
		return false
	}
	for index, name := range names {
		if !isIdent(call.Args[index], name) {
			return false
		}
	}
	return true
}

func isIdentCall(expression ast.Expr, function string, argument string) bool {
	call, ok := expression.(*ast.CallExpr)
	return ok && isIdent(call.Fun, function) && hasIdentArgs(call, argument)
}

func isSelectorExprCall(expression ast.Expr, receiver string, method string, argument string) bool {
	call, ok := expression.(*ast.CallExpr)
	return ok && isSelectorCall(call, receiver, method) && hasIdentArgs(call, argument)
}

func isEmptyString(expression ast.Expr) bool {
	literal, ok := expression.(*ast.BasicLit)
	return ok && literal.Kind == token.STRING && literal.Value == `""`
}
