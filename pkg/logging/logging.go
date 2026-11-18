// Package logging routes the loggers of libraries into the controller-runtime
// logger, so the --zap-* flags control all log output of a binary.
package logging

import (
	"io"
	stdlog "log"
	"strings"

	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

// RedirectLibraryLoggers sends the lines of the global logrus logger, of klog
// (client-go) and of the standard library log package to log, named
// "logrus", "klog" and "stdlib". Call it right after ctrl.SetLogger.
//
// logrus info and warn lines become info, error, fatal and panic lines become
// error, debug lines V(2) and trace lines V(3), so the default debug (V(1))
// level keeps them hidden as logrus' default info level did. klog lines keep
// klog's own verbosity filter (-v is not exposed, so only V(0)); info lines
// become info and error lines become error. Standard library lines, for
// example of net/http servers and reverse proxies without an ErrorLog, become
// error.
func RedirectLibraryLoggers(log logr.Logger) {
	logrus.SetOutput(io.Discard)
	logrus.SetLevel(logrus.TraceLevel)
	logrus.AddHook(logrusHook{log: log.WithName("logrus")})
	klog.SetLogger(log.WithName("klog"))
	stdlog.SetFlags(0)
	stdlog.SetOutput(stdlibWriter{log: log.WithName("stdlib")})
}

// stdlibWriter writes each line of the standard library logger as an error line.
type stdlibWriter struct{ log logr.Logger }

func (w stdlibWriter) Write(p []byte) (int, error) {
	w.log.Error(nil, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

type logrusHook struct{ log logr.Logger }

func (logrusHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h logrusHook) Fire(e *logrus.Entry) error {
	// An error value in the error field of an error line becomes the error of
	// logr.Error; any other error field stays a field.
	err, isErr := e.Data[logrus.ErrorKey].(error)
	isErrorLine := e.Level <= logrus.ErrorLevel
	kv := make([]any, 0, 2*len(e.Data)) //nolint:mnd // key and value
	for k, v := range e.Data {
		if k != logrus.ErrorKey || !isErr || !isErrorLine {
			kv = append(kv, k, v)
		}
	}
	switch {
	case isErrorLine:
		h.log.Error(err, e.Message, kv...)
	case e.Level <= logrus.InfoLevel:
		h.log.Info(e.Message, kv...)
	case e.Level == logrus.DebugLevel:
		h.log.V(2).Info(e.Message, kv...) //nolint:mnd // logrus debug
	default:
		h.log.V(3).Info(e.Message, kv...) //nolint:mnd // logrus trace
	}
	return nil
}
