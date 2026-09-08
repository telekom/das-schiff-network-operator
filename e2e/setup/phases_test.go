package setup

import (
	"errors"
	"reflect"
	"testing"
)

func TestInstallMultus(t *testing.T) {
	wantURL := "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/v4.1.4/deployments/multus-daemonset-thick.yml"
	for _, tt := range []struct {
		name string
		fail int
	}{
		{name: "success", fail: -1}, {name: "apply", fail: 0}, {name: "patch", fail: 1}, {name: "rollout", fail: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got [][]string
			wantErr := errors.New("boom")
			err := installMultus(func(args ...string) error {
				got = append(got, args)
				if len(got)-1 == tt.fail {
					return wantErr
				}
				return nil
			}, "v4.1.4")
			if (tt.fail >= 0) != (err != nil) || tt.fail >= 0 && !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want failure %d", err, tt.fail)
			}
			want := [][]string{
				{"apply", "-f", wantURL},
				{"-n", "kube-system", "patch", "daemonset", "kube-multus-ds", "--type=json", `-p=[{"op":"replace","path":"/spec/template/spec/containers/0/resources/limits/memory","value":"512Mi"},{"op":"replace","path":"/spec/template/spec/containers/0/resources/requests/memory","value":"512Mi"}]`},
				{"-n", "kube-system", "rollout", "status", "daemonset/kube-multus-ds", "--timeout=180s"},
			}
			if tt.fail >= 0 {
				want = want[:tt.fail+1]
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("commands = %#v, want %#v", got, want)
			}
		})
	}
}
