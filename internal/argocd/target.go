package argocd

// Routed mode (see internal/process_event/routed.go) diffs applications across
// several ArgoCD servers in a single run, one AppProject at a time. The
// package-level target set here overrides the ARGOCD_SERVER_ADDR /
// ARGOCD_AUTH_TOKEN environment configuration used by the CLI wrapper for
// subsequent invocations.
//
// Targets are processed sequentially by the routed orchestrator, so a single
// override slot is sufficient. Webhook-server mode never calls SetTarget and
// keeps the environment configuration untouched.

var (
	targetServerAddr string
	targetAuthToken  string
)

// SetTarget points subsequent argocd CLI invocations at the given server with
// the given bearer token, overriding the environment configuration.
func SetTarget(serverAddr, authToken string) {
	targetServerAddr = serverAddr
	targetAuthToken = authToken
}

// ClearTarget reverts SetTarget, restoring the environment configuration.
func ClearTarget() {
	targetServerAddr = ""
	targetAuthToken = ""
}

func effectiveServerAddr() string {
	if targetServerAddr != "" {
		return targetServerAddr
	}
	return envServerAddr
}

func effectiveAuthToken() string {
	if targetAuthToken != "" {
		return targetAuthToken
	}
	return httpBearerToken
}
