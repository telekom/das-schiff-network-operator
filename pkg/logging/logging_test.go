package logging

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

func TestRedirectLibraryLoggers(t *testing.T) {
	var lines []string
	RedirectLibraryLoggers(funcr.New(func(prefix, args string) {
		lines = append(lines, prefix+" "+args)
	}, funcr.Options{Verbosity: 3}))
	defer klog.ClearLogger()

	logrus.WithField("path", "/p").Debugf("debug %d", 1)
	logrus.Trace("trace")
	logrus.Info("info")
	logrus.Warn("warn")
	logrus.WithError(errors.New("bad")).Error("error")
	klog.InfoS("klog info", "k", "v")
	klog.ErrorS(errors.New("boom"), "klog error")

	want := []string{
		`logrus "level"=2 "msg"="debug 1" "path"="/p"`,
		`logrus "level"=3 "msg"="trace"`,
		`logrus "level"=0 "msg"="info"`,
		`logrus "level"=0 "msg"="warn"`,
		`logrus "msg"="error" "error"="bad"`,
		`klog "level"=0 "msg"="klog info" "k"="v"`,
		`klog "msg"="klog error" "error"="boom"`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i := range want {
		if !strings.Contains(lines[i], want[i]) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want[i])
		}
	}
}
