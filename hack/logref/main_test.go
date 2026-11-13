package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggingReferenceUpToDate fails when docs/reference/logging.md does not
// list exactly the log messages, levels and fields of the code.
func TestLoggingReferenceUpToDate(t *testing.T) {
	if err := run(filepath.Join("..", ".."), true); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "cmd/app/main.go", `package main

import "example.com/m/pkg/x"

func main() {
	setupLog.Error(err, "start failed", "addr", a)
	x.F()
}
`)
	writeFile(t, root, "pkg/x/x.go", `package x

func F() {
	r.logger.WithValues("node", n).Info("reconciled", "name", n)
	logger.V(1).Info("cache hit")
	logger.V(2).Info("trace")
	logrus.WithField("path", p).Debugf("delta %s", d)
	logrus.Warnf("retry")
	n.log.Errorf("failed: %v", err)
	executionLog.Infof("busctl call")
	logger.Info(fmt.Sprintf("vrf %s", v))
	logger.Info(msg)
	http.Error(w, "not a log", 500)
	err.Error()
}
`)
	writeFile(t, root, "pkg/x/x_test.go", "package x\nfunc g() { logger.Info(\"test only\") }\n")
	writeFile(t, root, "pkg/unused/u.go", "package unused\nfunc g() { logger.Info(\"unused\") }\n")

	pkgs, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for dir, p := range pkgs {
		for _, e := range p.entries {
			got[dir+" "+e.level+" "+e.message] = strings.Join(sortedKeys(e.fields), ",")
		}
	}
	want := map[string]string{
		"cmd/app error start failed": "addr",
		"pkg/x info reconciled":      "name,node",
		"pkg/x debug cache hit":      "",
		"pkg/x V(2) trace":           "",
		"pkg/x debug delta %s":       "path",
		"pkg/x info retry":           "",
		"pkg/x error failed: %v":     "",
		"pkg/x info vrf %s":          "",
		"pkg/x info (dynamic)":       "",
		"pkg/unused info unused":     "",
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%q: fields %q (present %t), want %q", k, g, ok, v)
		}
	}

	md := render(pkgs)
	for _, s := range []string{
		"| `app` | 2 | 4 | 2 | 1 | yes |",
		"#### `pkg/x`\n\nLinked into: `app`",
		"#### `pkg/unused`\n\nLinked into: none",
		"| info | `reconciled` | `name`, `node` |",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("rendered catalogue lacks %q:\n%s", s, md)
		}
	}
}

func TestScanRejectsStdlibLog(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "pkg/x/x.go", "package x\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n")
	if _, err := scan(root); err == nil || !strings.Contains(err.Error(), "pkg/x/x.go") {
		t.Fatalf("scan accepted the stdlib log import: %v", err)
	}
}

func TestRunCheckDetectsDrift(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n")
	writeFile(t, root, "pkg/x/x.go", "package x\nfunc f() { logger.Info(\"hello\") }\n")
	writeFile(t, root, docPath, "# Ref\n"+beginMark+"\n"+endMark+"\n")
	if err := run(root, true); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("check on a stale doc: %v", err)
	}
	if err := run(root, false); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err != nil {
		t.Fatalf("check after regeneration: %v", err)
	}
	doc, err := os.ReadFile(filepath.Join(root, docPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "`hello`") {
		t.Fatalf("regenerated doc lacks the message:\n%s", doc)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
