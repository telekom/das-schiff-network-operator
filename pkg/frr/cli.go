package frr

type FRRCLI struct {
	binaryPath string
}

func NewFRRCLI() (*FRRCLI, error) {
	return &FRRCLI{
		binaryPath: "/usr/bin/vtysh",
	}, nil
}
