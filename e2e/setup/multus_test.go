package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallMultus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     string
		installExit string
		patchExit   string
		rolloutExit string
		wantError   string
		wantRollout bool
	}{
		{name: "default version", wantRollout: true},
		{name: "version override", version: "v4.2.0", wantRollout: true},
		{name: "installation failure", installExit: "1", wantError: "installing Multus"},
		{name: "readiness patch failure", patchExit: "1", wantError: "configuring Multus readiness"},
		{name: "rollout failure", rolloutExit: "1", wantError: "waiting for Multus", wantRollout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const docker = `#!/bin/sh
set -eu
if [ "$1" != exec ]; then exit 2; fi
case "$*" in
*" apply "*)
  printf '%s\n' "$@" > "$TEST_DIR/install"
  exit "${INSTALL_EXIT:-0}"
  ;;
*" patch "*)
  printf '%s\n' "$@" > "$TEST_DIR/patch"
  exit "${PATCH_EXIT:-0}"
  ;;
esac
printf '%s\n' "$@" > "$TEST_DIR/rollout"
exit "${ROLLOUT_EXIT:-0}"
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0o700); err != nil { //nolint:gosec // Executable stub in a private test directory.
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_DIR", dir)
			t.Setenv("MULTUS_VERSION", tc.version)
			t.Setenv("INSTALL_EXIT", tc.installExit)
			t.Setenv("PATCH_EXIT", tc.patchExit)
			t.Setenv("ROLLOUT_EXIT", tc.rolloutExit)

			err := InstallMultus("test-control-plane")
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			version := tc.version
			if version == "" {
				version = "v4.1.4"
			}
			assertFileContains(t, filepath.Join(dir, "install"),
				"test-control-plane", "/"+version+"/deployments/multus-daemonset-thick.yml")
			if tc.installExit == "" {
				assertFileContains(t, filepath.Join(dir, "patch"),
					"--type=strategic", `"name": "kube-multus"`,
					"test -s /host/etc/cni/net.d/00-multus.conf",
					"test -S /host/run/multus/multus.sock")
			}
			if tc.wantRollout {
				assertFileContains(t, filepath.Join(dir, "rollout"),
					"test-control-plane", "rollout\nstatus\ndaemonset/kube-multus-ds\n--timeout=120s")
			} else if _, err := os.Stat(filepath.Join(dir, "rollout")); !os.IsNotExist(err) {
				t.Fatalf("rollout must not run after installation failure: %v", err)
			}
		})
	}
}

func assertFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("%s does not contain %q", path, fragment)
		}
	}
}
