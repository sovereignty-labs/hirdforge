package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type graphOutput struct {
	Packages map[string][]string   `json:"packages"`
	Files    map[string]fileInfo   `json:"files"`
	Symbols  map[string]symbolInfo `json:"symbols"`
}

type fileInfo struct {
	Package   string         `json:"package"`
	Imports   []string       `json:"imports"`
	Types     []typeInfo     `json:"types"`
	Functions []functionInfo `json:"functions"`
}

type typeInfo struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Fields  []string `json:"fields,omitempty"`
	Methods []string `json:"methods,omitempty"`
}

type functionInfo struct {
	Name     string   `json:"name"`
	Receiver string   `json:"receiver,omitempty"`
	Params   []string `json:"params,omitempty"`
	Returns  []string `json:"returns,omitempty"`
}

type symbolInfo struct {
	DefinedIn    string   `json:"defined_in"`
	Kind         string   `json:"kind"`
	ReferencedBy []string `json:"referenced_by"`
}

type packageSymbols struct {
	Types  map[string]string
	Funcs  map[string]string
	Vars   map[string]string
	Consts map[string]string
}

type fileRecord struct {
	info          fileInfo
	packageName   string
	packagePath   string
	importAliases map[string]string
	unresolved    map[string]struct{}
	selectors     []selectorRef
}

type selectorRef struct {
	importPath string
	name       string
}

type graphBuilder struct {
	root         string
	moduleRoot   string
	modulePath   string
	fset         *token.FileSet
	packages     map[string][]string
	files        map[string]*fileRecord
	symbols      map[string]*symbolInfo
	pkgSymbols   map[string]*packageSymbols
	typeRegistry map[string]*typeInfo
}

func main() {
	dir := "."
	if len(os.Args) > 2 {
		fail("usage: astgraph [directory]")
	}
	if len(os.Args) == 2 {
		dir = os.Args[1]
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fail(err.Error())
	}
	builder, err := newGraphBuilder(absDir)
	if err != nil {
		fail(err.Error())
	}
	if err := builder.walk(); err != nil {
		fail(err.Error())
	}
	builder.resolveReferences()
	out := builder.output()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func newGraphBuilder(root string) (*graphBuilder, error) {
	moduleRoot, modulePath := findModuleRoot(root)
	return &graphBuilder{
		root:         root,
		moduleRoot:   moduleRoot,
		modulePath:   modulePath,
		fset:         token.NewFileSet(),
		packages:     make(map[string][]string),
		files:        make(map[string]*fileRecord),
		symbols:      make(map[string]*symbolInfo),
		pkgSymbols:   make(map[string]*packageSymbols),
		typeRegistry: make(map[string]*typeInfo),
	}, nil
}

func findModuleRoot(start string) (string, string) {
	dir := start
	for {
		goModPath := filepath.Join(dir, "go.mod")
		data, err := os.ReadFile(goModPath)
		if err == nil {
			return dir, parseModulePath(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return start, ""
}

func parseModulePath(goMod string) string {
	for _, line := range strings.Split(goMod, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func (g *graphBuilder) walk() error {
	return filepath.WalkDir(g.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "seidr" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return g.parseFile(path)
	})
}

func (g *graphBuilder) parseFile(path string) error {
	file, err := parser.ParseFile(g.fset, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	relPath := g.relPath(path)
	pkgPath := g.packagePath(filepath.Dir(path))
	record := &fileRecord{
		info: fileInfo{
			Package:   pkgPath,
			Imports:   []string{},
			Types:     []typeInfo{},
			Functions: []functionInfo{},
		},
		packageName:   file.Name.Name,
		packagePath:   pkgPath,
		importAliases: make(map[string]string),
		unresolved:    make(map[string]struct{}),
	}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		record.info.Imports = append(record.info.Imports, importPath)
		alias := filepath.Base(importPath)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias != "_" && alias != "." {
			record.importAliases[alias] = importPath
		}
	}
	sort.Strings(record.info.Imports)

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			g.handleGenDecl(relPath, pkgPath, d, &record.info)
		case *ast.FuncDecl:
			g.handleFuncDecl(relPath, pkgPath, d, &record.info)
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if ident, ok := n.X.(*ast.Ident); ok {
				if importPath, found := record.importAliases[ident.Name]; found {
					record.selectors = append(record.selectors, selectorRef{
						importPath: importPath,
						name:       n.Sel.Name,
					})
					return false
				}
			}
		case *ast.Ident:
			if n.Name == "_" || n.Obj != nil {
				return true
			}
			if _, isAlias := record.importAliases[n.Name]; isAlias {
				return true
			}
			record.unresolved[n.Name] = struct{}{}
		}
		return true
	})

	g.packages[pkgPath] = append(g.packages[pkgPath], relPath)
	g.files[relPath] = record
	return nil
}

func (g *graphBuilder) handleGenDecl(filePath, pkgPath string, decl *ast.GenDecl, info *fileInfo) {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			kind := kindForExpr(s.Type)
			typeInfo := typeInfo{
				Name:    s.Name.Name,
				Kind:    kind,
				Fields:  fieldsForType(g.fset, s.Type),
				Methods: []string{},
			}
			info.Types = append(info.Types, typeInfo)
			key := pkgPath + "." + s.Name.Name
			g.typeRegistry[key] = &info.Types[len(info.Types)-1]
			g.addSymbol(pkgPath, s.Name.Name, filePath, "type")
		case *ast.ValueSpec:
			kind := "var"
			if decl.Tok == token.CONST {
				kind = "const"
			}
			for _, name := range s.Names {
				g.addSymbol(pkgPath, name.Name, filePath, kind)
			}
		}
	}
}

func (g *graphBuilder) handleFuncDecl(filePath, pkgPath string, decl *ast.FuncDecl, info *fileInfo) {
	fn := functionInfo{
		Name:    decl.Name.Name,
		Params:  fieldListStrings(g.fset, decl.Type.Params),
		Returns: fieldListStrings(g.fset, decl.Type.Results),
	}
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		fn.Receiver = exprString(g.fset, decl.Recv.List[0].Type)
	}
	info.Functions = append(info.Functions, fn)
	g.addSymbol(pkgPath, decl.Name.Name, filePath, "func")

	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		if recvName := receiverBaseName(decl.Recv.List[0].Type); recvName != "" {
			if typeInfo := g.typeRegistry[pkgPath+"."+recvName]; typeInfo != nil {
				typeInfo.Methods = append(typeInfo.Methods, methodSignature(fn))
			}
		}
	}
}

func (g *graphBuilder) addSymbol(pkgPath, name, filePath, kind string) {
	pkg := g.ensurePackageSymbols(pkgPath)
	switch kind {
	case "type":
		pkg.Types[name] = filePath
	case "func":
		pkg.Funcs[name] = filePath
	case "var":
		pkg.Vars[name] = filePath
	case "const":
		pkg.Consts[name] = filePath
	}
	key := pkgPath + "." + name
	if _, exists := g.symbols[key]; !exists {
		g.symbols[key] = &symbolInfo{
			DefinedIn:    filePath,
			Kind:         kind,
			ReferencedBy: []string{},
		}
	}
}

func (g *graphBuilder) ensurePackageSymbols(pkgPath string) *packageSymbols {
	if existing := g.pkgSymbols[pkgPath]; existing != nil {
		return existing
	}
	pkg := &packageSymbols{
		Types:  make(map[string]string),
		Funcs:  make(map[string]string),
		Vars:   make(map[string]string),
		Consts: make(map[string]string),
	}
	g.pkgSymbols[pkgPath] = pkg
	return pkg
}

func (g *graphBuilder) resolveReferences() {
	for filePath, record := range g.files {
		if pkg := g.pkgSymbols[record.packagePath]; pkg != nil {
			for name := range record.unresolved {
				if defFile, ok := lookupLocalSymbol(pkg, name); ok && defFile != filePath {
					g.addReference(record.packagePath+"."+name, filePath)
				}
			}
		}
		for _, sel := range record.selectors {
			if sel.importPath == "" || !g.isLocalImport(sel.importPath) {
				continue
			}
			key := sel.importPath + "." + sel.name
			if sym := g.symbols[key]; sym != nil && sym.DefinedIn != filePath {
				g.addReference(key, filePath)
			}
		}
	}
	for _, record := range g.files {
		for i := range record.info.Types {
			sort.Strings(record.info.Types[i].Fields)
			sort.Strings(record.info.Types[i].Methods)
		}
		sort.Slice(record.info.Types, func(i, j int) bool { return record.info.Types[i].Name < record.info.Types[j].Name })
		sort.Slice(record.info.Functions, func(i, j int) bool {
			if record.info.Functions[i].Name == record.info.Functions[j].Name {
				return record.info.Functions[i].Receiver < record.info.Functions[j].Receiver
			}
			return record.info.Functions[i].Name < record.info.Functions[j].Name
		})
	}
}

func lookupLocalSymbol(pkg *packageSymbols, name string) (string, bool) {
	if path, ok := pkg.Types[name]; ok {
		return path, true
	}
	if path, ok := pkg.Funcs[name]; ok {
		return path, true
	}
	if path, ok := pkg.Vars[name]; ok {
		return path, true
	}
	if path, ok := pkg.Consts[name]; ok {
		return path, true
	}
	return "", false
}

func (g *graphBuilder) addReference(key, filePath string) {
	sym := g.symbols[key]
	if sym == nil {
		return
	}
	for _, existing := range sym.ReferencedBy {
		if existing == filePath {
			return
		}
	}
	sym.ReferencedBy = append(sym.ReferencedBy, filePath)
	sort.Strings(sym.ReferencedBy)
}

func (g *graphBuilder) isLocalImport(importPath string) bool {
	return g.modulePath != "" && (importPath == g.modulePath || strings.HasPrefix(importPath, g.modulePath+"/"))
}

func (g *graphBuilder) output() graphOutput {
	packages := make(map[string][]string, len(g.packages))
	for pkgPath, files := range g.packages {
		cp := append([]string(nil), files...)
		sort.Strings(cp)
		packages[pkgPath] = cp
	}
	files := make(map[string]fileInfo, len(g.files))
	for path, record := range g.files {
		files[path] = record.info
	}
	symbols := make(map[string]symbolInfo, len(g.symbols))
	for key, sym := range g.symbols {
		refCopy := append([]string(nil), sym.ReferencedBy...)
		sort.Strings(refCopy)
		symbols[key] = symbolInfo{
			DefinedIn:    sym.DefinedIn,
			Kind:         sym.Kind,
			ReferencedBy: refCopy,
		}
	}
	return graphOutput{
		Packages: packages,
		Files:    files,
		Symbols:  symbols,
	}
}

func (g *graphBuilder) relPath(path string) string {
	rel, err := filepath.Rel(g.root, path)
	if err == nil && rel != "" {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func (g *graphBuilder) packagePath(dir string) string {
	base := g.root
	if g.moduleRoot != "" {
		base = g.moduleRoot
	}
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." {
		if g.modulePath != "" {
			return g.modulePath
		}
		return filepath.Base(dir)
	}
	rel = filepath.ToSlash(rel)
	if g.modulePath != "" {
		return g.modulePath + "/" + rel
	}
	return rel
}

func kindForExpr(expr ast.Expr) string {
	switch expr.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	default:
		return "type"
	}
}

func fieldsForType(fset *token.FileSet, expr ast.Expr) []string {
	switch t := expr.(type) {
	case *ast.StructType:
		return fieldListStrings(fset, t.Fields)
	case *ast.InterfaceType:
		return fieldListStrings(fset, t.Methods)
	default:
		return nil
	}
}

func fieldListStrings(fset *token.FileSet, list *ast.FieldList) []string {
	if list == nil {
		return nil
	}
	var out []string
	for _, field := range list.List {
		typeStr := exprString(fset, field.Type)
		if len(field.Names) == 0 {
			out = append(out, typeStr)
			continue
		}
		for _, name := range field.Names {
			out = append(out, strings.TrimSpace(name.Name+" "+typeStr))
		}
	}
	return out
}

func methodSignature(fn functionInfo) string {
	var parts []string
	parts = append(parts, fn.Name)
	if len(fn.Params) > 0 {
		parts = append(parts, "("+strings.Join(fn.Params, ", ")+")")
	} else {
		parts = append(parts, "()")
	}
	if len(fn.Returns) > 0 {
		parts = append(parts, " ("+strings.Join(fn.Returns, ", ")+")")
	}
	return strings.Join(parts, "")
}

func receiverBaseName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverBaseName(t.X)
	case *ast.IndexExpr:
		return receiverBaseName(t.X)
	case *ast.IndexListExpr:
		return receiverBaseName(t.X)
	default:
		return ""
	}
}

func exprString(fset *token.FileSet, expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}
