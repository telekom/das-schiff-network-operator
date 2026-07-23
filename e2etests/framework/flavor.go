package framework

import "os"

const (
	craFlavorEnv   = "E2E_CRA_FLAVOR"
	craFlavorFRR   = "frr"
	craFlavorGrout = "grout"
)

// CRAFlavor returns the configured CRA flavor. It mirrors e2e/setup/flavor.go.
func (*Framework) CRAFlavor() string {
	switch os.Getenv(craFlavorEnv) {
	case craFlavorGrout:
		return craFlavorGrout
	default:
		return craFlavorFRR
	}
}

// IsGrout reports whether the e2e lab is using the grout CRA flavor.
func (f *Framework) IsGrout() bool {
	return f.CRAFlavor() == craFlavorGrout
}
