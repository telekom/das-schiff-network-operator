// Package garp resolves whether a Layer 2 bridge accepts gratuitous ARP.
package garp

import (
	"os"
	"sync"

	"github.com/go-logr/logr"
)

// Env overrides the agent default for every bridge: "always" or "never".
const Env = "NWOP_L2_ACCEPT_GARP"

// policyVersion names the gratuitous ARP rendering of the agents. Change it
// when the rendering of an unchanged NodeNetworkConfig changes.
const policyVersion = "garp/v1"

var warnOnce sync.Once

// AppliedStateID identifies the policy version and the effective Env
// override, for common.ReconcilerOptions.AppliedStateID: when either changes,
// the agent applies an unchanged NodeNetworkConfig again after its restart.
func AppliedStateID() string {
	v := os.Getenv(Env)
	if v != "always" && v != "never" {
		v = "" // unknown values are ignored like an unset Env
	}
	return policyVersion + " " + Env + "=" + v
}

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
