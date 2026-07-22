//go:build linux

/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cni

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndRemoveDeviceInfo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "device-info.json")
	conf := &NetConf{
		Transport:  TransportVhostUser,
		SocketPath: "/run/vhost/net1.sock",
		SocketMode: SocketModeServer,
	}
	conf.RuntimeConfig.CNIDeviceInfoFile = path

	if err := writeDeviceInfo(conf); err != nil {
		t.Fatalf("writeDeviceInfo: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading device info: %v", err)
	}
	var info deviceInfo
	if uerr := json.Unmarshal(data, &info); uerr != nil {
		t.Fatalf("unmarshalling device info: %v", uerr)
	}
	if info.Type != "vhost-user" {
		t.Errorf("Type = %q, want vhost-user", info.Type)
	}
	if info.VhostUser == nil || info.VhostUser.Path != conf.SocketPath || info.VhostUser.Mode != conf.SocketMode {
		t.Errorf("VhostUser = %+v, want path=%s mode=%s", info.VhostUser, conf.SocketPath, conf.SocketMode)
	}

	if err := removeDeviceInfo(conf); err != nil {
		t.Fatalf("removeDeviceInfo: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("device info file still present after remove: %v", err)
	}
	// Removing again is a no-op.
	if err := removeDeviceInfo(conf); err != nil {
		t.Errorf("removeDeviceInfo (second) = %v, want nil", err)
	}
}

func TestWriteDeviceInfoNoFile(t *testing.T) {
	// No CNIDeviceInfoFile requested: writing is a no-op, not an error.
	conf := &NetConf{Transport: TransportVhostUser, SocketPath: "/run/x.sock", SocketMode: SocketModeClient}
	if err := writeDeviceInfo(conf); err != nil {
		t.Errorf("writeDeviceInfo (no file) = %v, want nil", err)
	}
}
