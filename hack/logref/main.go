// Command logref generates the message catalogue in docs/reference/logging.md
// from the log calls in the Go code of this module.
//
//	go run ./hack/logref          # rewrite the generated section
//	go run ./hack/logref -check   # fail if the section is out of date
//
// It parses the source without type information. A call is a log call when
// its receiver expression contains "log" (but not "executionLog", the netplan
// D-Bus command trace file) and it is either a logr call (Info, Error, with an
// optional V(n)) or a logrus call (Debug, Info, Warn, Error, ... with an
// optional f suffix). The level is the one the call has after
// logging.RedirectLogrus; the fields are the literal keys of key/value pairs
// and of WithValues/WithField calls on the receiver.
//
// The scan also fails when non-test code imports the standard library "log"
// package, because those lines would bypass the --zap-* flags.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	docPath    = "docs/reference/logging.md"
	beginMark  = "<!-- BEGIN GENERATED: go run ./hack/logref -->"
	endMark    = "<!-- END GENERATED -->"
	dynamicMsg = "(dynamic)"
	docMode    = 0o600
)

var (
	scanDirs = []string{"api", "cmd", "controllers", "pkg"}
	// skipDirs hold no log calls of their own: pkg/logging forwards logrus lines.
	skipDirs = map[string]bool{"pkg/logging": true}
	vLevelRe = regexp.MustCompile(`\.V\((\d+)\)`)
	// logrusLevels maps logrus methods to the level they have after logging.RedirectLogrus.
	logrusLevels = map[string]string{
		"Trace": "debug", "Debug": "debug", "Info": "info", "Print": "info", "Warn": "info", "Warning": "info",
		"Error": "error", "Fatal": "error", "Panic": "error",
	}
	levelOrder = []string{"error", "info", "debug"}
)

type entry struct {
	level, message string
	fields         map[string]bool
}

type pkgInfo struct {
	imports map[string]bool
	entries map[string]*entry // key: level + "\x00" + message
	logrus  bool
}

func main() {
	check := flag.Bool("check", false, "fail if "+docPath+" is out of date instead of rewriting it")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	pkgs, err := scan(root)
	if err != nil {
		return err
	}
	path := filepath.Join(root, docPath)
	doc, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", docPath, err)
	}
	begin := bytes.Index(doc, []byte(beginMark))
	end := bytes.Index(doc, []byte(endMark))
	if begin < 0 || end < begin {
		return fmt.Errorf("%s needs the markers %q and %q", docPath, beginMark, endMark)
	}
	updated := string(doc[:begin+len(beginMark)]) + "\n" + render(pkgs) + string(doc[end:])
	if updated == string(doc) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is out of date with the log calls in the code; run `go run ./hack/logref` and commit the result", docPath)
	}
	if err := os.WriteFile(path, []byte(updated), docMode); err != nil {
		return fmt.Errorf("write %s: %w", docPath, err)
	}
	return nil
}

func modulePath(root string) (string, error) {
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(gomod), "\n") {
		if m, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

func scan(root string) (map[string]*pkgInfo, error) {
	mod, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	s := &scanner{root: root, mod: mod, fset: token.NewFileSet(), pkgs: map[string]*pkgInfo{}}
	for _, top := range scanDirs {
		if err := filepath.WalkDir(filepath.Join(root, top), s.visit); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("scan %s: %w", top, err)
		}
	}
	if len(s.stdlibLog) > 0 {
		return nil, fmt.Errorf("these files import the standard library \"log\" package, whose output ignores --zap-log-level and --zap-encoder; use a logr logger (ctrl.Log.WithName(...)) instead: %s", strings.Join(s.stdlibLog, ", "))
	}
	return s.pkgs, nil
}

type scanner struct {
	root, mod string
	fset      *token.FileSet
	pkgs      map[string]*pkgInfo
	stdlibLog []string
}

func (s *scanner) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(s.root, path)
	rel = filepath.ToSlash(rel)
	if d.IsDir() {
		if skipDirs[rel] || d.Name() == "testdata" {
			return filepath.SkipDir
		}
		return nil
	}
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(d.Name(), "zz_generated") {
		return nil
	}
	f, err := parser.ParseFile(s.fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w", rel, err)
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	p := s.pkgs[dir]
	if p == nil {
		p = &pkgInfo{imports: map[string]bool{}, entries: map[string]*entry{}}
		s.pkgs[dir] = p
	}
	for _, imp := range f.Imports {
		ip, _ := strconv.Unquote(imp.Path.Value)
		if ip == "log" {
			s.stdlibLog = append(s.stdlibLog, rel)
		}
		if dep, ok := strings.CutPrefix(ip, s.mod+"/"); ok {
			p.imports[dep] = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			addCall(p, call)
		}
		return true
	})
	return nil
}

func addCall(p *pkgInfo, call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 {
		return
	}
	recv := types.ExprString(sel.X)
	lower := strings.ToLower(recv)
	if !strings.Contains(lower, "log") || strings.Contains(lower, "executionlog") {
		return
	}
	method := sel.Sel.Name
	isLogrus := strings.HasPrefix(recv, "logrus") || (method != "Info" && method != "Error")
	var level string
	var msgArg ast.Expr
	var kv []ast.Expr
	switch {
	case isLogrus:
		var ok bool
		if level, ok = logrusLevels[strings.TrimSuffix(strings.TrimSuffix(method, "ln"), "f")]; !ok {
			return
		}
		msgArg = call.Args[0]
		p.logrus = true
	case method == "Error":
		if len(call.Args) < 2 { //nolint:mnd // err, msg
			return
		}
		level, msgArg, kv = "error", call.Args[1], call.Args[2:]
	default:
		level, msgArg, kv = "info", call.Args[0], call.Args[1:]
		if m := vLevelRe.FindAllStringSubmatch(recv, -1); m != nil {
			if v := m[len(m)-1][1]; v != "0" {
				level = "debug"
				if v != "1" {
					level = "V(" + v + ")"
				}
			}
		}
	}
	msg := message(msgArg)
	key := level + "\x00" + msg
	e := p.entries[key]
	if e == nil {
		e = &entry{level: level, message: msg, fields: map[string]bool{}}
		p.entries[key] = e
	}
	for i := 0; i < len(kv); i += 2 {
		if k := literal(kv[i]); k != "" {
			e.fields[k] = true
		}
	}
	receiverFields(sel.X, e.fields)
}

// receiverFields adds the literal keys of WithValues/WithField(s) calls in a receiver chain.
func receiverFields(x ast.Expr, fields map[string]bool) {
	for {
		switch v := x.(type) {
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			switch sel.Sel.Name {
			case "WithValues":
				for i := 0; i < len(v.Args); i += 2 {
					if k := literal(v.Args[i]); k != "" {
						fields[k] = true
					}
				}
			case "WithField":
				if k := literal(v.Args[0]); k != "" {
					fields[k] = true
				}
			}
			x = sel.X
		case *ast.SelectorExpr:
			x = v.X
		default:
			return
		}
	}
}

func literal(x ast.Expr) string {
	if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, err := strconv.Unquote(lit.Value)
		if err == nil {
			return s
		}
	}
	return ""
}

func message(x ast.Expr) string {
	if s := literal(x); s != "" {
		return s
	}
	switch v := x.(type) {
	case *ast.CallExpr:
		if types.ExprString(v.Fun) == "fmt.Sprintf" && len(v.Args) > 0 {
			if s := literal(v.Args[0]); s != "" {
				return s
			}
		}
	case *ast.BinaryExpr:
		if s := literal(v.X); s != "" && v.Op == token.ADD {
			return s + "…"
		}
	}
	return dynamicMsg
}

// binaries maps each cmd/<name> to the module packages it links.
func binaries(pkgs map[string]*pkgInfo) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for dir := range pkgs {
		name, ok := strings.CutPrefix(dir, "cmd/")
		if !ok || strings.Contains(name, "/") {
			continue
		}
		seen := map[string]bool{}
		queue := []string{dir}
		for len(queue) > 0 {
			d := queue[0]
			queue = queue[1:]
			if seen[d] || pkgs[d] == nil {
				continue
			}
			seen[d] = true
			for imp := range pkgs[d].imports {
				queue = append(queue, imp)
			}
		}
		out[name] = seen
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func code(s string) string {
	return "`" + strings.ReplaceAll(strings.ReplaceAll(s, "`", "'"), "|", `\|`) + "`"
}

func sortedEntries(p *pkgInfo) []*entry {
	es := make([]*entry, 0, len(p.entries))
	for _, k := range sortedKeys(p.entries) {
		es = append(es, p.entries[k])
	}
	rank := func(level string) int {
		if i := slices.Index(levelOrder, level); i >= 0 {
			return i
		}
		return len(levelOrder)
	}
	sort.SliceStable(es, func(i, j int) bool { return rank(es[i].level) < rank(es[j].level) })
	return es
}

func render(pkgs map[string]*pkgInfo) string {
	bins := binaries(pkgs)
	usedBy := map[string][]string{}
	var b strings.Builder
	b.WriteString("\n### Messages per binary\n\n")
	b.WriteString("Number of distinct messages each binary can log, per level. `logrus` marks binaries that link code logging through logrus.\n\n")
	b.WriteString("| Binary | error | info | debug | higher V-levels | logrus |\n|---|---|---|---|---|---|\n")
	for _, name := range sortedKeys(bins) {
		counts := map[string]int{}
		usesLogrus := "no"
		for _, dir := range sortedKeys(bins[name]) {
			p := pkgs[dir]
			if len(p.entries) > 0 {
				usedBy[dir] = append(usedBy[dir], name)
			}
			if p.logrus {
				usesLogrus = "yes"
			}
			for _, e := range p.entries {
				if slices.Contains(levelOrder, e.level) {
					counts[e.level]++
				} else {
					counts["other"]++
				}
			}
		}
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %s |\n", name, counts["error"], counts["info"], counts["debug"], counts["other"], usesLogrus)
	}
	b.WriteString("\n### Messages per package\n\n")
	b.WriteString("Messages are the literal message (logr) or format string (logrus) of the call. `(dynamic)` marks a message built at run time.\n")
	for _, dir := range sortedKeys(pkgs) {
		p := pkgs[dir]
		if len(p.entries) == 0 {
			continue
		}
		users := "none (not linked by a binary in `cmd/`)"
		if len(usedBy[dir]) > 0 {
			users = "`" + strings.Join(usedBy[dir], "`, `") + "`"
		}
		fmt.Fprintf(&b, "\n#### `%s`\n\nLinked into: %s\n\n| Level | Message | Fields |\n|---|---|---|\n", dir, users)
		for _, e := range sortedEntries(p) {
			fields := make([]string, 0, len(e.fields))
			for _, f := range sortedKeys(e.fields) {
				fields = append(fields, code(f))
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", e.level, code(e.message), strings.Join(fields, ", "))
		}
	}
	b.WriteString("\n")
	return b.String()
}
