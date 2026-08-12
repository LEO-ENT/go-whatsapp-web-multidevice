package whatsapp

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	logrusImportPath = "github.com/sirupsen/logrus"
	waLogImportPath  = "go.mau.fi/whatsmeow/util/log"
)

type receiptBoundaryRule struct {
	method   string
	category string
	countArg bool
}

var receiptBoundaryRules = map[string]receiptBoundaryRule{
	"logReceiptRead":                         {method: "Infof", category: "whatsapp_receipt.read count=%d", countArg: true},
	"logReceiptDelivered":                    {method: "Infof", category: "whatsapp_receipt.delivered count=%d", countArg: true},
	"logReceiptLinkedDeviceSkipped":          {method: "Debug", category: "whatsapp_receipt.linked_device_skipped"},
	"logReceiptStorageUnavailable":           {method: "Warn", category: "chatwoot_receipt.storage_unavailable"},
	"logChatwootReceiptLookupFailed":         {method: "Error", category: "chatwoot_receipt.lookup_failed"},
	"logChatwootReceiptMissingSource":        {method: "Debug", category: "chatwoot_receipt.missing_source"},
	"logChatwootReceiptUpdateLastSeenFailed": {method: "Error", category: "chatwoot_receipt.update_last_seen_failed"},
	"logChatwootReceiptMarkReadFailed":       {method: "Error", category: "chatwoot_receipt.mark_read_failed"},
}

type receiptGuardImporter struct {
	fallback types.Importer
	packages map[string]*types.Package
}

func newReceiptGuardImporter() *receiptGuardImporter {
	return &receiptGuardImporter{
		fallback: importer.Default(),
		packages: map[string]*types.Package{
			logrusImportPath: fakeLogrusPackage(),
			waLogImportPath:  fakeWALogPackage(),
		},
	}
}

func (i *receiptGuardImporter) Import(path string) (*types.Package, error) {
	if pkg := i.packages[path]; pkg != nil {
		return pkg, nil
	}
	if !strings.Contains(path, ".") {
		return i.fallback.Import(path)
	}
	name := filepath.Base(path)
	pkg := types.NewPackage(path, name)
	pkg.MarkComplete()
	i.packages[path] = pkg
	return pkg, nil
}

func variadicLogSignature(recv *types.Var) *types.Signature {
	anyType := types.NewInterfaceType(nil, nil).Complete()
	params := types.NewTuple(
		types.NewParam(token.NoPos, nil, "format", types.Typ[types.String]),
		types.NewParam(token.NoPos, nil, "args", types.NewSlice(anyType)),
	)
	return types.NewSignatureType(recv, nil, nil, params, nil, true)
}

func fixedLogSignature(recv *types.Var) *types.Signature {
	anyType := types.NewInterfaceType(nil, nil).Complete()
	params := types.NewTuple(types.NewParam(token.NoPos, nil, "args", types.NewSlice(anyType)))
	return types.NewSignatureType(recv, nil, nil, params, nil, true)
}

func fakeLogrusPackage() *types.Package {
	pkg := types.NewPackage(logrusImportPath, "logrus")
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Logger", nil), types.NewStruct(nil, nil), nil)
	recv := types.NewParam(token.NoPos, pkg, "logger", types.NewPointer(named))
	for _, name := range []string{"Errorf", "Warnf", "Infof", "Debugf"} {
		named.AddMethod(types.NewFunc(token.NoPos, pkg, name, variadicLogSignature(recv)))
	}
	for _, name := range []string{"Error", "Warn", "Info", "Debug"} {
		named.AddMethod(types.NewFunc(token.NoPos, pkg, name, fixedLogSignature(recv)))
		pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, name, fixedLogSignature(nil)))
	}
	for _, name := range []string{"Errorf", "Warnf", "Infof", "Debugf"} {
		pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, name, variadicLogSignature(nil)))
	}
	result := types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.NewPointer(named)))
	pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, "StandardLogger", types.NewSignatureType(nil, nil, nil, nil, result, false)))
	pkg.Scope().Insert(named.Obj())
	pkg.MarkComplete()
	return pkg
}

func fakeWALogPackage() *types.Package {
	pkg := types.NewPackage(waLogImportPath, "log")
	methods := make([]*types.Func, 0, 4)
	for _, name := range []string{"Errorf", "Warnf", "Infof", "Debugf"} {
		methods = append(methods, types.NewFunc(token.NoPos, pkg, name, variadicLogSignature(nil)))
	}
	iface := types.NewInterfaceType(methods, nil).Complete()
	pkg.Scope().Insert(types.NewTypeName(token.NoPos, pkg, "Logger", iface))
	pkg.MarkComplete()
	return pkg
}

func loggingObject(obj types.Object) (string, bool) {
	if obj == nil || obj.Pkg() == nil {
		return "", false
	}
	if _, ok := obj.(*types.Func); !ok {
		return "", false
	}
	pkgPath := obj.Pkg().Path()
	if pkgPath != logrusImportPath && pkgPath != waLogImportPath {
		return "", false
	}
	switch obj.Name() {
	case "Errorf", "Warnf", "Infof", "Debugf", "Error", "Warn", "Info", "Debug":
		return obj.Name(), true
	default:
		return "", false
	}
}

func loggingFunctionExpr(info *types.Info, expr ast.Expr, aliases map[types.Object]string) (string, bool) {
	switch value := expr.(type) {
	case *ast.ParenExpr:
		return loggingFunctionExpr(info, value.X, aliases)
	case *ast.Ident:
		obj := info.Uses[value]
		if method, ok := aliases[obj]; ok {
			return method, true
		}
		return loggingObject(obj)
	case *ast.SelectorExpr:
		if selection := info.Selections[value]; selection != nil {
			return loggingObject(selection.Obj())
		}
		return loggingObject(info.Uses[value.Sel])
	default:
		return "", false
	}
}

func loggingAliases(info *types.Info, body *ast.BlockStmt) map[types.Object]string {
	aliases := make(map[types.Object]string)
	changed := true
	for changed {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			switch stmt := node.(type) {
			case *ast.AssignStmt:
				if len(stmt.Lhs) != len(stmt.Rhs) {
					return true
				}
				for index, lhs := range stmt.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					obj := info.Defs[ident]
					if obj == nil {
						obj = info.Uses[ident]
					}
					method, logging := loggingFunctionExpr(info, stmt.Rhs[index], aliases)
					if obj != nil && logging && aliases[obj] != method {
						aliases[obj] = method
						changed = true
					}
				}
			case *ast.ValueSpec:
				if len(stmt.Names) != len(stmt.Values) {
					return true
				}
				for index, ident := range stmt.Names {
					obj := info.Defs[ident]
					method, logging := loggingFunctionExpr(info, stmt.Values[index], aliases)
					if obj != nil && logging && aliases[obj] != method {
						aliases[obj] = method
						changed = true
					}
				}
			}
			return true
		})
	}
	return aliases
}

func localFunctionExpr(info *types.Info, expr ast.Expr, aliases map[types.Object]types.Object, functions map[types.Object]guardFunction) types.Object {
	switch value := expr.(type) {
	case *ast.ParenExpr:
		return localFunctionExpr(info, value.X, aliases, functions)
	case *ast.Ident:
		obj := info.Uses[value]
		if target := aliases[obj]; target != nil {
			return target
		}
		if _, local := functions[obj]; local {
			return obj
		}
	}
	return nil
}

func localFunctionAliases(info *types.Info, body *ast.BlockStmt, functions map[types.Object]guardFunction) map[types.Object]types.Object {
	aliases := make(map[types.Object]types.Object)
	changed := true
	for changed {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			switch stmt := node.(type) {
			case *ast.AssignStmt:
				if len(stmt.Lhs) != len(stmt.Rhs) {
					return true
				}
				for index, lhs := range stmt.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					obj := info.Defs[ident]
					if obj == nil {
						obj = info.Uses[ident]
					}
					target := localFunctionExpr(info, stmt.Rhs[index], aliases, functions)
					if obj != nil && target != nil && aliases[obj] != target {
						aliases[obj] = target
						changed = true
					}
				}
			case *ast.ValueSpec:
				if len(stmt.Names) != len(stmt.Values) {
					return true
				}
				for index, ident := range stmt.Names {
					obj := info.Defs[ident]
					target := localFunctionExpr(info, stmt.Values[index], aliases, functions)
					if obj != nil && target != nil && aliases[obj] != target {
						aliases[obj] = target
						changed = true
					}
				}
			}
			return true
		})
	}
	return aliases
}

func loggingCall(info *types.Info, call *ast.CallExpr, aliases map[types.Object]string) (string, bool) {
	return loggingFunctionExpr(info, call.Fun, aliases)
}

func exactReceiptCount(info *types.Info, expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	lenIdent, ok := call.Fun.(*ast.Ident)
	if !ok || lenIdent.Name != "len" {
		return false
	}
	builtin, ok := info.Uses[lenIdent].(*types.Builtin)
	if !ok || builtin.Name() != "len" {
		return false
	}
	messageIDs, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || messageIDs.Sel.Name != "MessageIDs" {
		return false
	}
	evt, ok := messageIDs.X.(*ast.Ident)
	return ok && evt.Name == "evt"
}

func validateBoundaryInvocation(info *types.Info, call *ast.CallExpr, boundaries map[string]types.Object) (bool, bool) {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false, false
	}
	rule, boundary := receiptBoundaryRules[ident.Name]
	if !boundary {
		return false, false
	}
	if boundaries[ident.Name] == nil || info.Uses[ident] != boundaries[ident.Name] {
		return true, false
	}
	if rule.countArg {
		return true, len(call.Args) == 1 && exactReceiptCount(info, call.Args[0])
	}
	return true, len(call.Args) == 0
}

func receiptBoundaryObjects(info *types.Info, files []*ast.File, fileNames map[*ast.File]string) map[string]types.Object {
	objects := make(map[string]types.Object, len(receiptBoundaryRules))
	for _, file := range files {
		if fileNames[file] != "receipt_logging.go" {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if _, boundary := receiptBoundaryRules[fn.Name.Name]; boundary {
				objects[fn.Name.Name] = info.Defs[fn.Name]
			}
		}
	}
	return objects
}

type guardFunction struct {
	file string
	decl *ast.FuncDecl
}

func guardFunctions(info *types.Info, files []*ast.File, fileNames map[*ast.File]string) map[types.Object]guardFunction {
	functions := make(map[types.Object]guardFunction)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if obj := info.Defs[fn.Name]; obj != nil {
				functions[obj] = guardFunction{file: fileNames[file], decl: fn}
			}
		}
	}
	return functions
}

func receiptTraversalExit(fn guardFunction) bool {
	// Generic delivery performs its own independently-audited logging. The
	// receipt-specific guard stops at that boundary instead of treating every
	// webhook event log as receipt metadata.
	return fn.file == "webhook_forward.go" && fn.decl.Name.Name == "forwardPayloadToConfiguredWebhooks"
}

func exactBoundaryCall(call *ast.CallExpr, method string, rule receiptBoundaryRule) bool {
	if method != rule.method || len(call.Args) == 0 {
		return false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	category, err := strconv.Unquote(literal.Value)
	if err != nil || category != rule.category {
		return false
	}
	if !rule.countArg {
		return len(call.Args) == 1
	}
	if len(call.Args) != 2 {
		return false
	}
	count, ok := call.Args[1].(*ast.Ident)
	return ok && count.Name == "count"
}

func analyzeReceiptLoggingSources(sources map[string]string, targets map[string]map[string]bool) []string {
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(sources)+1)
	fileNames := make(map[*ast.File]string, len(sources))
	for name, source := range sources {
		file, err := parser.ParseFile(fset, name, source, parser.AllErrors)
		if err != nil {
			return []string{fmt.Sprintf("parse %s: %v", name, err)}
		}
		files = append(files, file)
		fileNames[file] = name
	}
	// The production package declares log in init.go, outside the bounded receipt
	// files. Inject only its real nominal type so go/types can resolve aliases.
	stub, err := parser.ParseFile(fset, "receipt_guard_stub.go", "package whatsapp\nimport walog \"go.mau.fi/whatsmeow/util/log\"\nvar log walog.Logger", parser.AllErrors)
	if err != nil {
		return []string{err.Error()}
	}
	files = append(files, stub)

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	conf := types.Config{Importer: newReceiptGuardImporter(), Error: func(error) {}}
	_, _ = conf.Check("receiptguard.local/whatsapp", fset, files, info)

	violations := make([]string, 0)
	boundarySeen := make(map[string]int)
	boundaries := receiptBoundaryObjects(info, files[:len(files)-1], fileNames)
	functions := guardFunctions(info, files[:len(files)-1], fileNames)
	queue := make([]types.Object, 0, len(functions))
	queued := make(map[types.Object]bool)
	for obj, fn := range functions {
		if targets[fn.file][fn.decl.Name.Name] {
			queue = append(queue, obj)
			queued[obj] = true
		}
	}
	for len(queue) > 0 {
		obj := queue[0]
		queue = queue[1:]
		current := functions[obj]
		name := current.file
		fn := current.decl
		aliases := loggingAliases(info, fn.Body)
		localAliases := localFunctionAliases(info, fn.Body, functions)
		canonicalBoundary := boundaries[fn.Name.Name] != nil && obj == boundaries[fn.Name.Name]
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if boundary, exact := validateBoundaryInvocation(info, call, boundaries); boundary && !exact {
				violations = append(violations, fmt.Sprintf("%s:%s has non-exact receipt boundary invocation", name, fn.Name.Name))
			}
			if called := localFunctionExpr(info, call.Fun, localAliases, functions); called != nil {
				callee := functions[called]
				_, isBoundary := receiptBoundaryRules[callee.decl.Name.Name]
				if isBoundary {
					boundary, exact := validateBoundaryInvocation(info, call, boundaries)
					if !boundary || !exact {
						violations = append(violations, fmt.Sprintf("%s:%s aliases a receipt boundary", name, fn.Name.Name))
					}
				}
				if !isBoundary && !receiptTraversalExit(callee) && !queued[called] {
					queue = append(queue, called)
					queued[called] = true
				}
			}
			method, isLog := loggingCall(info, call, aliases)
			if !isLog {
				return true
			}
			rule, boundary := receiptBoundaryRules[fn.Name.Name]
			if !boundary || !canonicalBoundary {
				violations = append(violations, fmt.Sprintf("%s:%s calls logger.%s outside receipt boundary", name, fn.Name.Name, method))
				return true
			}
			boundarySeen[fn.Name.Name]++
			if !exactBoundaryCall(call, method, rule) {
				violations = append(violations, fmt.Sprintf("%s:%s has non-exact receipt log call", name, fn.Name.Name))
			}
			return true
		})
	}
	for fn := range receiptBoundaryRules {
		if targets["receipt_logging.go"][fn] && boundarySeen[fn] != 1 {
			violations = append(violations, fmt.Sprintf("receipt_logging.go:%s log call count=%d, want 1", fn, boundarySeen[fn]))
		}
	}
	return violations
}

func TestReceiptLoggingGuardRejectsAliasesAndComputedFormats(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		wantReject bool
	}{
		{
			name:       "direct import alias",
			source:     `package whatsapp; import lr "github.com/sirupsen/logrus"; func target(){ lr.Errorf("unsafe") }`,
			wantReject: true,
		},
		{
			name: "logger object alias and constant format",
			source: `package whatsapp
				import lr "github.com/sirupsen/logrus"
				func target(){ const receiptFormat = "%s: %v"; receiptLogger := lr.StandardLogger(); receiptLogger.Errorf(receiptFormat, "3EB0000000000000000001", nil) }`,
			wantReject: true,
		},
		{
			name: "wa logger alias",
			source: `package whatsapp
				import walog "go.mau.fi/whatsmeow/util/log"
				var fixtureLog walog.Logger
				func target(){ receiptLogger := fixtureLog; receiptLogger.Infof("%s", "unsafe") }`,
			wantReject: true,
		},
		{
			name: "computed format",
			source: `package whatsapp
				import (lr "github.com/sirupsen/logrus"; "fmt")
				func target(){ lr.Errorf(fmt.Sprintf("%s", "unsafe")) }`,
			wantReject: true,
		},
		{
			name: "constant concatenation",
			source: `package whatsapp
				import lr "github.com/sirupsen/logrus"
				func target(){ const left = "%s"; const right = ": %v"; lr.Errorf(left+right, "3EB0000000000000001", nil) }`,
			wantReject: true,
		},
		{
			name: "local boundary shadow",
			source: `package whatsapp
				func target(){ logReceiptRead := func(int){}; var evt struct{ MessageIDs []string }; logReceiptRead(len(evt.MessageIDs)) }`,
			wantReject: true,
		},
		{
			name: "builtin len shadow",
			source: `package whatsapp
				func target(){ len := func([]string) int { return 37 }; var evt struct{ MessageIDs []string }; logReceiptRead(len(evt.MessageIDs)) }`,
			wantReject: true,
		},
		{
			name: "boundary function value alias",
			source: `package whatsapp
				func target(){ var evt struct{ MessageIDs []string }; receiptLog := logReceiptRead; receiptLog(len(evt.MessageIDs)) }`,
			wantReject: true,
		},
		{
			name:       "dot import logger",
			source:     `package whatsapp; import . "github.com/sirupsen/logrus"; func target(){ Errorf("unsafe") }`,
			wantReject: true,
		},
		{
			name: "logger function value alias",
			source: `package whatsapp
				import lr "github.com/sirupsen/logrus"
				func target(){ f := lr.Errorf; f("%s", "3EB0000000000000001") }`,
			wantReject: true,
		},
		{
			name: "safe helper plus unsafe function alias",
			source: `package whatsapp
				import lr "github.com/sirupsen/logrus"
				func safeHelper(){}
				func target(){ helper := safeHelper; helper(); unsafe := lr.Errorf; again := unsafe; again("unsafe") }`,
			wantReject: true,
		},
		{
			name: "unsafe helper reached from receipt path",
			source: `package whatsapp
				import lr "github.com/sirupsen/logrus"
				func unsafeHelper(){ lr.Errorf("unsafe") }
				func target(){ unsafeHelper() }`,
			wantReject: true,
		},
		{
			name: "comments strings and unrelated dead function",
			source: `package whatsapp
				func target(){ _ = "logrus.Errorf(receiptFormat, messageID, err)" /* receiptLogger.Errorf */; _ = "evt.SourceString()" }
				func dead(){ println("not a target") }`,
			wantReject: false,
		},
		{
			name:       "safe boundary rejects arbitrary numeric data",
			source:     `package whatsapp; func target(){ logReceiptRead(37) }`,
			wantReject: true,
		},
		{
			name:       "nominal safe helper",
			source:     `package whatsapp; func safeReceiptCategory(){}; func target(){ safeReceiptCategory() }`,
			wantReject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Provide the canonical package-level facade declarations so fixtures can
			// prove local shadowing and builtin identity without importing production.
			boundaryFixture := `package whatsapp
				func logReceiptRead(int){}
				func logReceiptDelivered(int){}
				func logReceiptLinkedDeviceSkipped(){}
				func logReceiptStorageUnavailable(){}
				func logChatwootReceiptLookupFailed(){}
				func logChatwootReceiptMissingSource(){}
				func logChatwootReceiptUpdateLastSeenFailed(){}
				func logChatwootReceiptMarkReadFailed(){}`
			violations := analyzeReceiptLoggingSources(
				map[string]string{"fixture.go": tt.source, "receipt_logging.go": boundaryFixture},
				map[string]map[string]bool{"fixture.go": {"target": true}},
			)
			if got := len(violations) > 0; got != tt.wantReject {
				t.Fatalf("rejected=%t, want %t; violations=%v", got, tt.wantReject, violations)
			}
		})
	}
}

func TestProductionReceiptPathUsesOnlyNominalSafeLoggingBoundary(t *testing.T) {
	targets := map[string]map[string]bool{
		"event_handler.go":   {"handleReceipt": true},
		"event_receipt.go":   {"forwardReceiptToWebhook": true},
		"webhook_forward.go": {"syncReadReceiptsToChatwoot": true},
		"receipt_logging.go": {},
	}
	for fn := range receiptBoundaryRules {
		targets["receipt_logging.go"][fn] = true
	}

	sources := make(map[string]string, len(targets))
	for name := range targets {
		source, err := os.ReadFile(name)
		if err != nil {
			if os.IsNotExist(err) && name == "receipt_logging.go" {
				sources[name] = "package whatsapp"
				continue
			}
			t.Fatalf("read %s: %v", name, err)
		}
		sources[name] = string(source)
	}

	if violations := analyzeReceiptLoggingSources(sources, targets); len(violations) > 0 {
		t.Fatalf("receipt logging boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}
