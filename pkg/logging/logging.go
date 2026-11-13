// Package logging routes the loggers of libraries into the controller-runtime
// logger, so the --zap-* flags control all log output of a binary.
package logging

import (
	"io"

	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

// RedirectLibraryLoggers sends the lines of the global logrus logger and of
// klog (client-go) to log, named "logrus" and "klog". Call it right after
// ctrl.SetLogger.
//
// logrus debug and trace lines become debug (V(1)), info and warn lines
// become info, and error, fatal and panic lines become error. klog lines keep
// klog's own verbosity filter (-v is not exposed, so only V(0)); info lines
// become info and error lines become error.
func RedirectLibraryLoggers(log logr.Logger) {
	logrus.SetOutput(io.Discard)
	logrus.SetLevel(logrus.TraceLevel)
	logrus.AddHook(logrusHook{log: log.WithName("logrus")})
	klog.SetLogger(log.WithName("klog"))
}

type logrusHook struct{ log logr.Logger }

func (logrusHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h logrusHook) Fire(e *logrus.Entry) error {
	kv := make([]any, 0, 2*len(e.Data)) //nolint:mnd // key and value
	for k, v := range e.Data {
		kv = append(kv, k, v)
	}
	switch {
	case e.Level <= logrus.ErrorLevel:
		h.log.Error(nil, e.Message, kv...)
	case e.Level <= logrus.InfoLevel:
		h.log.Info(e.Message, kv...)
	default:
		h.log.V(1).Info(e.Message, kv...)
	}
	return nil
}
