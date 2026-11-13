// Package garp resolves whether a Layer 2 bridge accepts gratuitous ARP.
package garp

import (
	"os"
	"sync"

	"github.com/go-logr/logr"
)

// Env overrides the agent default for every bridge: "always" or "never".
const Env = "NWOP_L2_ACCEPT_GARP"

var warnOnce sync.Once

// Resolve returns the setting for one bridge. A network that opts in always
// accepts; otherwise Env decides. nil means Env is unset (or unknown, which is
// logged once) and the agent default applies.
func Resolve(networkOptIn bool, logger logr.Logger) *bool {
	accept, reject := true, false
	if networkOptIn {
		return &accept
	}
	switch v := os.Getenv(Env); v {
	case "always":
		return &accept
	case "never":
		return &reject
	case "":
		return nil
	default:
		warnOnce.Do(func() {
			logger.Info("WARNING: ignoring unknown value, using the agent default",
				"variable", Env, "value", v, "allowed", "always, never")
		})
		return nil
	}
}
