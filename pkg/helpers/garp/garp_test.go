package garp

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	yes, no := true, false
	for _, tt := range []struct {
		name  string
		optIn bool
		env   string
		want  *bool
	}{
		{name: "unset uses agent default"},
		{name: "unknown uses agent default", env: "true"},
		{name: "always", env: "always", want: &yes},
		{name: "never", env: "never", want: &no},
		{name: "network opt-in beats never", optIn: true, env: "never", want: &yes},
		{name: "network opt-in without env", optIn: true, want: &yes},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(Env, tt.env)
			require.Equal(t, tt.want, Resolve(tt.optIn, logr.Discard()))
		})
	}
}
