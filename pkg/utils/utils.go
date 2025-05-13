package utils

import (
	"net/http"
	"os"
)

func GetNamespace() string {
	ns, found := os.LookupEnv("POD_NAMESPACE")
	if !found {
		return "kube-system"
	}
	return ns
}

// ExactPathHandler returns an http.Handler that only serves the handler if the request path matches exactly.
func ExactPathHandler(path string, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}
