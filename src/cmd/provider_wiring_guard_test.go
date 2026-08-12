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
	"strconv"
	"strings"
	"testing"
)

const (
	providerWiringHelper      = "registerProviderLookupRoutes"
	providerCompositionHelper = "registerProviderAndDeviceScopedRoutes"
)

type parsedProductionFile struct {
	path string
	file *ast.File
}

type lookupCallSite struct {
	path     string
	function string
	call     *ast.CallExpr
}

func TestProviderRouteBoundaryGuard(t *testing.T) {
	if defects := providerBoundaryDefects(productionGoSources(t)); len(defects) != 0 {
		t.Fatalf("provider boundary defects: %s", strings.Join(defects, "; "))
	}
}

func TestProviderRouteBoundaryGuardRejectsPermanentAdversarialMutants(t *testing.T) {
	baseline := productionGoSources(t)
	mutants := map[string]map[string]string{
		"M4 route loses its resolved boundary marker":    cloneSources(baseline),
		"M4 reflective root preempts the provider route": cloneSources(baseline),
		"E2 Use binds a compatibility lookup":            cloneSources(baseline),
		"E3 FuncLit hides a compatibility lookup":        cloneSources(baseline),
		"E4 direct usecase call bypasses the controller": cloneSources(baseline),
		"provider authentication is removed":             cloneSources(baseline),
		"opaque device boundary is downgraded":           cloneSources(baseline),
		"provider route moves behind compatibility":      cloneSources(baseline),
	}

	mutants["M4 route loses its resolved boundary marker"]["cmd/rest.go"] = strings.Replace(
		baseline["cmd/rest.go"],
		"middleware.ProviderLookupBoundary(),",
		"",
		1,
	)
	mutants["M4 reflective root preempts the provider route"]["cmd/rest.go"] = strings.Replace(
		baseline["cmd/rest.go"],
		"registerProviderAndDeviceScopedRoutes(\n\t\tapiGroup,",
		"apiGroup.Use(middleware.DeviceMiddleware(dm))\n\tregisterProviderAndDeviceScopedRoutes(\n\t\tapiGroup,",
		1,
	)
	mutants["E2 Use binds a compatibility lookup"]["cmd/provider_compat.go"] = `package cmd
func bindCompat(apiGroup fiber.Router, providerCompat *rest.Provider) {
	apiGroup.Use("/provider/compat", providerCompat.LookupMessage)
}`
	mutants["E3 FuncLit hides a compatibility lookup"]["cmd/provider_compat.go"] = `package cmd
func bindCompat(apiGroup fiber.Router, providerCompat *rest.Provider) {
	apiGroup.Post("/provider/lookup-compat", func(c fiber.Ctx) error {
		return providerCompat.LookupMessage(c)
	})
}`
	mutants["E4 direct usecase call bypasses the controller"]["cmd/provider_compat.go"] = `package cmd
func bypass(providerUsecase domainProvider.IMessageLookupUsecase, c fiber.Ctx) error {
	_, _ = providerUsecase.LookupMessage(c.Context(), "opaque-id")
	return nil
}`
	mutants["provider authentication is removed"]["cmd/rest.go"] = strings.Replace(
		baseline["cmd/rest.go"],
		"providerLookupAuthMiddleware(accounts),",
		"",
		1,
	)
	mutants["opaque device boundary is downgraded"]["cmd/rest.go"] = strings.Replace(
		baseline["cmd/rest.go"],
		"middleware.OpaqueDeviceMiddleware(dm)",
		"middleware.DeviceMiddleware(dm)",
		1,
	)
	mutants["provider route moves behind compatibility"]["cmd/rest.go"] = strings.Replace(
		baseline["cmd/rest.go"],
		`registerProviderLookupRoutes(apiGroup, accounts, dm, service)

	headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))
	registerDeviceScopedRoutes(headerDeviceGroup)`,
		`headerDeviceGroup := apiGroup.Group("", middleware.DeviceMiddleware(dm))
	registerDeviceScopedRoutes(headerDeviceGroup)

	registerProviderLookupRoutes(apiGroup, accounts, dm, service)`,
		1,
	)

	for name, sources := range mutants {
		t.Run(name, func(t *testing.T) {
			if defects := providerBoundaryDefects(sources); len(defects) == 0 {
				t.Fatal("semantic provider boundary mutant escaped")
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

func providerBoundaryDefects(sources map[string]string) []string {
	parsed, defects := parseProductionSources(sources)
	if len(parsed) == 0 {
		return append(defects, "no production Go sources parsed")
	}

	reachesLookup := lookupCallGraph(parsed)
	var helperDefinitions int
	var canonicalHelper bool
	var helperCalls int
	var canonicalHelperCall bool
	var compositionDefinitions int
	var canonicalComposition bool
	var compositionCalls int
	var canonicalCompositionCall bool
	var routeBindings int
	var canonicalBindings int
	var providerConstructors int
	var boundaryDefinitions int
	var canonicalBoundary bool
	var opaqueDefinitions int
	var canonicalOpaque bool
	var controllerDefinitions int
	var canonicalController bool
	var deviceMiddlewareReferences int
	var opaqueMiddlewareReferences int
	var boundaryMiddlewareReferences int
	var lookupCalls []lookupCallSite

	for _, source := range parsed {
		aliases := lookupHandlerAliases(source.file, reachesLookup)
		ast.Inspect(source.file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || !isIdent(selector.X, "middleware") {
				return true
			}
			switch selector.Sel.Name {
			case "DeviceMiddleware":
				deviceMiddlewareReferences++
			case "OpaqueDeviceMiddleware":
				opaqueMiddlewareReferences++
			case "ProviderLookupBoundary":
				boundaryMiddlewareReferences++
			}
			return true
		})
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			functionName := function.Name.Name
			switch functionName {
			case providerWiringHelper:
				helperDefinitions++
				canonicalHelper = canonicalHelper || isCanonicalProviderWiringHelper(function)
			case providerCompositionHelper:
				compositionDefinitions++
				canonicalComposition = canonicalComposition || isCanonicalProviderCompositionHelper(function)
			case "ProviderLookupBoundary":
				if source.path == "ui/rest/middleware/device.go" {
					boundaryDefinitions++
					canonicalBoundary = canonicalBoundary || isCanonicalProviderBoundary(function)
				}
			case "OpaqueDeviceMiddleware":
				if source.path == "ui/rest/middleware/device.go" {
					opaqueDefinitions++
					canonicalOpaque = canonicalOpaque || isCanonicalOpaqueDeviceMiddleware(function)
				}
			case "LookupMessage":
				if source.path == "ui/rest/provider.go" {
					controllerDefinitions++
					canonicalController = canonicalController || isBoundaryGuardedLookupController(function)
				}
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if isIdent(call.Fun, providerWiringHelper) {
					helperCalls++
					canonicalHelperCall = canonicalHelperCall || (functionName == providerCompositionHelper && hasIdentArgs(call, "apiGroup", "accounts", "dm", "service"))
				}
				if isIdent(call.Fun, providerCompositionHelper) {
					compositionCalls++
					canonicalCompositionCall = canonicalCompositionCall || (functionName == "restServer" && hasIdentArgs(call, "apiGroup", "accounts", "dm", "providerUsecase", "registerDeviceScopedRoutes"))
				}
				if isSelectorCall(call, "rest", "NewProvider") {
					providerConstructors++
				}
				if isLookupInvocation(call) {
					lookupCalls = append(lookupCalls, lookupCallSite{path: source.path, function: functionName, call: call})
				}

				pathIndex, ok := routePathArgument(call)
				if !ok {
					return true
				}
				for _, handler := range call.Args[pathIndex+1:] {
					if !handlerReachesLookup(handler, aliases, reachesLookup) {
						continue
					}
					routeBindings++
					if functionName == providerWiringHelper && isCanonicalProviderRouteCall(call) {
						canonicalBindings++
					}
				}
				return true
			})
		}
	}

	if helperDefinitions != 1 || !canonicalHelper {
		defects = append(defects, fmt.Sprintf("canonical provider helper definitions/valid = %d/%t, want 1/true", helperDefinitions, canonicalHelper))
	}
	if helperCalls != 1 || !canonicalHelperCall {
		defects = append(defects, fmt.Sprintf("canonical provider helper calls/site = %d/%t, want 1/true", helperCalls, canonicalHelperCall))
	}
	if compositionDefinitions != 1 || !canonicalComposition {
		defects = append(defects, fmt.Sprintf("canonical provider composition definitions/valid = %d/%t, want 1/true", compositionDefinitions, canonicalComposition))
	}
	if compositionCalls != 1 || !canonicalCompositionCall {
		defects = append(defects, fmt.Sprintf("canonical provider composition calls/site = %d/%t, want 1/true", compositionCalls, canonicalCompositionCall))
	}
	if routeBindings != 1 || canonicalBindings != 1 {
		defects = append(defects, fmt.Sprintf("semantic LookupMessage route bindings/canonical = %d/%d, want 1/1", routeBindings, canonicalBindings))
	}
	if providerConstructors != 1 {
		defects = append(defects, fmt.Sprintf("provider controller constructors = %d, want 1", providerConstructors))
	}
	if boundaryDefinitions != 1 || !canonicalBoundary {
		defects = append(defects, fmt.Sprintf("resolved-route boundary definitions/valid = %d/%t, want 1/true", boundaryDefinitions, canonicalBoundary))
	}
	if opaqueDefinitions != 1 || !canonicalOpaque {
		defects = append(defects, fmt.Sprintf("opaque device middleware definitions/valid = %d/%t, want 1/true", opaqueDefinitions, canonicalOpaque))
	}
	if controllerDefinitions != 1 || !canonicalController {
		defects = append(defects, fmt.Sprintf("boundary-guarded lookup controller definitions/valid = %d/%t, want 1/true", controllerDefinitions, canonicalController))
	}
	if deviceMiddlewareReferences != 1 || opaqueMiddlewareReferences != 1 || boundaryMiddlewareReferences != 1 {
		defects = append(defects, fmt.Sprintf("device/opaque/boundary middleware references = %d/%d/%d, want 1/1/1", deviceMiddlewareReferences, opaqueMiddlewareReferences, boundaryMiddlewareReferences))
	}
	if len(lookupCalls) != 1 || !isCanonicalUsecaseLookupCall(lookupCalls) {
		locations := make([]string, 0, len(lookupCalls))
		for _, site := range lookupCalls {
			locations = append(locations, site.path+":"+site.function)
		}
		defects = append(defects, fmt.Sprintf("semantic LookupMessage invocation census = %v, want only ui/rest/provider.go:LookupMessage", locations))
	}

	defects = append(defects, providerRouteMetadataDefects(parsed)...)
	return defects
}

func parseProductionSources(sources map[string]string) ([]parsedProductionFile, []string) {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, filepath.ToSlash(path))
	}
	sort.Strings(paths)
	parsed := make([]parsedProductionFile, 0, len(paths))
	var defects []string
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, sources[path], 0)
		if err != nil {
			defects = append(defects, fmt.Sprintf("%s does not parse: %v", path, err))
			continue
		}
		parsed = append(parsed, parsedProductionFile{path: path, file: file})
	}
	return parsed, defects
}

func providerRouteMetadataDefects(parsed []parsedProductionFile) []string {
	want := map[string]string{
		"ProviderLookupPath":          "/provider/messages/lookup",
		"ProviderLookupBoundaryLocal": "gowa.provider.lookup.boundary",
	}
	seen := make(map[string]string)
	for _, source := range parsed {
		if source.path != "pkg/routepath/provider.go" {
			continue
		}
		for _, declaration := range source.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				values, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range values.Names {
					if index >= len(values.Values) {
						continue
					}
					literal, ok := values.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(literal.Value)
					if err == nil {
						seen[name.Name] = value
					}
				}
			}
		}
	}
	var defects []string
	for name, value := range want {
		if seen[name] != value {
			defects = append(defects, fmt.Sprintf("provider route metadata %s = %q, want %q", name, seen[name], value))
		}
	}
	return defects
}

func lookupCallGraph(parsed []parsedProductionFile) map[string]bool {
	direct := make(map[string]bool)
	callees := make(map[string]map[string]struct{})
	for _, source := range parsed {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			name := function.Name.Name
			if callees[name] == nil {
				callees[name] = make(map[string]struct{})
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if isLookupInvocation(call) {
					direct[name] = true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok {
					callees[name][ident.Name] = struct{}{}
				}
				return true
			})
		}
	}
	reaches := make(map[string]bool)
	for name := range direct {
		reaches[name] = true
	}
	changed := true
	for changed {
		changed = false
		for caller, names := range callees {
			if reaches[caller] {
				continue
			}
			for callee := range names {
				if reaches[callee] {
					reaches[caller] = true
					changed = true
					break
				}
			}
		}
	}
	return reaches
}

func lookupHandlerAliases(file *ast.File, reachesLookup map[string]bool) map[string]bool {
	aliases := make(map[string]bool)
	changed := true
	for changed {
		changed = false
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, expression := range value.Rhs {
					if index >= len(value.Lhs) || !handlerReachesLookup(expression, aliases, reachesLookup) {
						continue
					}
					if ident, ok := value.Lhs[index].(*ast.Ident); ok && !aliases[ident.Name] {
						aliases[ident.Name] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				for index, expression := range value.Values {
					if index >= len(value.Names) || !handlerReachesLookup(expression, aliases, reachesLookup) || aliases[value.Names[index].Name] {
						continue
					}
					aliases[value.Names[index].Name] = true
					changed = true
				}
			}
			return true
		})
	}
	return aliases
}

func handlerReachesLookup(expression ast.Expr, aliases map[string]bool, reachesLookup map[string]bool) bool {
	switch value := expression.(type) {
	case *ast.SelectorExpr:
		return value.Sel.Name == "LookupMessage"
	case *ast.Ident:
		return aliases[value.Name] || reachesLookup[value.Name]
	case *ast.FuncLit:
		reaches := false
		ast.Inspect(value.Body, func(node ast.Node) bool {
			if reaches {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isLookupInvocation(call) {
				reaches = true
				return false
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && reachesLookup[ident.Name] {
				reaches = true
				return false
			}
			return true
		})
		return reaches
	default:
		return false
	}
}

func routePathArgument(call *ast.CallExpr) (int, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	switch selector.Sel.Name {
	case "Use", "Get", "Post", "Put", "Patch", "Delete", "Head", "Connect", "Options", "Trace", "Query", "All":
		if len(call.Args) >= 2 {
			return 0, true
		}
	case "Add":
		if len(call.Args) >= 3 {
			return 1, true
		}
	}
	return 0, false
}

func isCanonicalProviderWiringHelper(function *ast.FuncDecl) bool {
	if len(function.Body.List) != 2 {
		return false
	}
	assignment, ok := function.Body.List[0].(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 || !isIdent(assignment.Lhs[0], "controller") {
		return false
	}
	constructor, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok || !isSelectorCall(constructor, "rest", "NewProvider") || !hasIdentArgs(constructor, "service") {
		return false
	}
	routeStatement, ok := function.Body.List[1].(*ast.ExprStmt)
	if !ok {
		return false
	}
	routeCall, ok := routeStatement.X.(*ast.CallExpr)
	return ok && isCanonicalProviderRouteCall(routeCall)
}

func isCanonicalProviderRouteCall(call *ast.CallExpr) bool {
	if !isSelectorCall(call, "apiGroup", "Post") || len(call.Args) != 5 {
		return false
	}
	return isSelectorExpr(call.Args[0], "rest", "ProviderLookupPath") &&
		isSelectorNoArgCall(call.Args[1], "middleware", "ProviderLookupBoundary") &&
		isIdentCall(call.Args[2], "providerLookupAuthMiddleware", "accounts") &&
		isSelectorExprCall(call.Args[3], "middleware", "OpaqueDeviceMiddleware", "dm") &&
		isSelectorExpr(call.Args[4], "controller", "LookupMessage")
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

func isCanonicalProviderBoundary(function *ast.FuncDecl) bool {
	if len(function.Body.List) != 1 {
		return false
	}
	outerReturn, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(outerReturn.Results) != 1 {
		return false
	}
	handler, ok := outerReturn.Results[0].(*ast.FuncLit)
	if !ok || len(handler.Body.List) != 2 {
		return false
	}
	markerStatement, ok := handler.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	markerCall, ok := markerStatement.X.(*ast.CallExpr)
	if !ok || !isSelectorCall(markerCall, "c", "Locals") || !hasSelectorAndBoolArgs(markerCall, "routepath", "ProviderLookupBoundaryLocal", true) {
		return false
	}
	nextReturn, ok := handler.Body.List[1].(*ast.ReturnStmt)
	if !ok || len(nextReturn.Results) != 1 {
		return false
	}
	nextCall, ok := nextReturn.Results[0].(*ast.CallExpr)
	return ok && isSelectorCall(nextCall, "c", "Next") && len(nextCall.Args) == 0
}

func isCanonicalOpaqueDeviceMiddleware(function *ast.FuncDecl) bool {
	if len(function.Body.List) != 1 {
		return false
	}
	result, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 1 {
		return false
	}
	call, ok := result.Results[0].(*ast.CallExpr)
	return ok && isIdent(call.Fun, "deviceMiddleware") && len(call.Args) == 2 && isIdent(call.Args[0], "dm") && isBool(call.Args[1], true)
}

func isBoundaryGuardedLookupController(function *ast.FuncDecl) bool {
	if len(function.Body.List) < 3 {
		return false
	}
	assignment, ok := function.Body.List[0].(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 2 || len(assignment.Rhs) != 1 || !isIdent(assignment.Lhs[0], "boundary") || !isIdent(assignment.Lhs[1], "_") {
		return false
	}
	assertion, ok := assignment.Rhs[0].(*ast.TypeAssertExpr)
	if !ok || !isIdent(assertion.Type, "bool") {
		return false
	}
	localCall, ok := assertion.X.(*ast.CallExpr)
	if !ok || !isSelectorCall(localCall, "c", "Locals") || len(localCall.Args) != 1 || !isSelectorExpr(localCall.Args[0], "routepath", "ProviderLookupBoundaryLocal") {
		return false
	}
	guard, ok := function.Body.List[1].(*ast.IfStmt)
	if !ok || guard.Init != nil || guard.Else != nil || len(guard.Body.List) != 1 {
		return false
	}
	negation, ok := guard.Cond.(*ast.UnaryExpr)
	if !ok || negation.Op != token.NOT || !isIdent(negation.X, "boundary") {
		return false
	}
	statement, ok := guard.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	panicCall, ok := statement.X.(*ast.CallExpr)
	return ok && isIdent(panicCall.Fun, "panic") && len(panicCall.Args) == 1
}

func isCanonicalUsecaseLookupCall(calls []lookupCallSite) bool {
	if len(calls) != 1 || calls[0].path != "ui/rest/provider.go" || calls[0].function != "LookupMessage" {
		return false
	}
	selector, ok := calls[0].call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "LookupMessage" {
		return false
	}
	service, ok := selector.X.(*ast.SelectorExpr)
	return ok && service.Sel.Name == "Service" && isIdent(service.X, "controller")
}

func isLookupInvocation(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "LookupMessage"
}

func cloneSources(sources map[string]string) map[string]string {
	clone := make(map[string]string, len(sources))
	for path, source := range sources {
		clone[path] = source
	}
	return clone
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

func hasSelectorAndBoolArgs(call *ast.CallExpr, receiver string, name string, value bool) bool {
	return len(call.Args) == 2 && isSelectorExpr(call.Args[0], receiver, name) && isBool(call.Args[1], value)
}

func isIdentCall(expression ast.Expr, function string, argument string) bool {
	call, ok := expression.(*ast.CallExpr)
	return ok && isIdent(call.Fun, function) && hasIdentArgs(call, argument)
}

func isSelectorExprCall(expression ast.Expr, receiver string, method string, argument string) bool {
	call, ok := expression.(*ast.CallExpr)
	return ok && isSelectorCall(call, receiver, method) && hasIdentArgs(call, argument)
}

func isSelectorNoArgCall(expression ast.Expr, receiver string, method string) bool {
	call, ok := expression.(*ast.CallExpr)
	return ok && isSelectorCall(call, receiver, method) && len(call.Args) == 0
}

func isEmptyString(expression ast.Expr) bool {
	literal, ok := expression.(*ast.BasicLit)
	return ok && literal.Kind == token.STRING && literal.Value == `""`
}

func isBool(expression ast.Expr, want bool) bool {
	ident, ok := expression.(*ast.Ident)
	if !ok {
		return false
	}
	if want {
		return ident.Name == "true"
	}
	return ident.Name == "false"
}
