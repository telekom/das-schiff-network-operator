package cra

import ctrl "sigs.k8s.io/controller-runtime"

// netconfLog is used by Netconf, which has no logger of its own.
var netconfLog = ctrl.Log.WithName("netconf")
