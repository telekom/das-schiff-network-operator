package garp

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

func TestAppliedStateID(t *testing.T) {
	ids := map[string]string{}
	for _, env := range []string{"", "always", "never", "true"} {
		t.Setenv(Env, env)
		ids[env] = AppliedStateID()
	}
	require.Equal(t, "garp/v1 NWOP_L2_ACCEPT_GARP=", ids[""])
	require.Equal(t, ids[""], ids["true"], "an ignored value must not force a new apply")
	require.NotEqual(t, ids[""], ids["always"])
	require.NotEqual(t, ids[""], ids["never"])
	require.NotEqual(t, ids["always"], ids["never"])
}

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
