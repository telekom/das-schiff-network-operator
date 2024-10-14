// based on https://github.com/kubernetes-sigs/cluster-api/version/version.go

// Package version implements version handling code.
package version

import (
	"fmt"
)

var (
	gitVersion string // semantic version, derived by build scripts
	gitCommit  string // sha1 from git, output of $(git rev-parse HEAD)
)

// Info exposes information about the version used for the current running code.
type Info struct {
	GitVersion string `json:"gitVersion,omitempty"`
	GitCommit  string `json:"gitCommit,omitempty"`
}

// Get returns an Info object with all the information about the current running code.
func Get() *Info {
	return &Info{
		GitVersion: gitVersion,
		GitCommit:  gitCommit,
	}
}

func (i *Info) Print(name string) {
	fmt.Printf("%s info - version: %s, commit: %s\n", name, i.GitVersion, i.GitCommit)
}
