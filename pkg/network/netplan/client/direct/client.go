package direct

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/telekom/das-schiff-network-operator/pkg/network/netplan"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const defaultNetplanBinary = "netplan"

type Client struct {
	binaryPath string
}
type Opts struct {
	NetPlanPath string
}

func New(opts Opts) *Client {
	result := Client{
		binaryPath: defaultNetplanBinary,
	}
	if opts.NetPlanPath != "" {
		result.binaryPath = opts.NetPlanPath
	}
	return &result
}

func (dc *Client) netplanWithInput(arguments []string, input string) (string, error) {
	cmd := exec.Command(dc.binaryPath, arguments...) //nolint:gosec
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if input != "" {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return "", fmt.Errorf("failed to create pipe for writing into %s: %w", defaultNetplanBinary, err)
		}
		go func() {
			defer stdin.Close()
			_, err = io.WriteString(stdin, input)
			if err != nil {
				fmt.Printf("failed to write input into stdin: %s\n", err.Error())
			}
		}()
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"failed to execute %s %s: '%w' '%s' '%s'",
			defaultNetplanBinary,
			strings.Join(arguments, " "),
			err,
			stdout.String(),
			stderr.String(),
		)
	}
	return stdout.String(), nil
}

//nolint:unused
func (dc *Client) netplanWithStdin(arguments []string) (string, error) {
	cmd := exec.Command(dc.binaryPath, arguments...) //nolint:gosec
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create pipe for writing into %s: %w", defaultNetplanBinary, err)
	}
	defer stdin.Close()

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"failed to execute %s %s: '%w' '%s' '%s'",
			defaultNetplanBinary,
			strings.Join(arguments, " "),
			err,
			stdout.String(),
			stderr.String(),
		)
	}
	return stdout.String(), nil
}

func (dc *Client) netplan(arguments []string) (string, error) {
	return dc.netplanWithInput(arguments, "")
}
func (*Client) Version() (string, netplan.Error) {
	return "", nil
}
func (*Client) Generate() netplan.Error {
	return nil
}

type infoNetPlanResultType struct {
	Website  string   `json:"website"`
	Features []string `json:"features"`
}
type infoResultType struct {
	NetPlan infoNetPlanResultType `json:"netplan.io"`
}

func (dc *Client) Info() ([]string, netplan.Error) {
	var infoResult infoResultType
	resultJSON, err := dc.netplan([]string{"info", "--json"})
	if err != nil {
		return nil, netplan.ParseError(err)
	}
	if err = json.Unmarshal([]byte(resultJSON), &infoResult); err != nil {
		return nil, netplan.ParseError(err)
	}
	return infoResult.NetPlan.Features, nil
}

func (dc *Client) Get() (*netplan.State, netplan.Error) {
	var resultYaml string
	var err error
	if resultYaml, err = dc.netplan([]string{"get", "all"}); err != nil {
		return nil, netplan.ParseError(err)
	}
	var result netplan.State
	if err := yaml.Unmarshal([]byte(resultYaml), &result); err != nil {
		return nil, netplan.ParseError(err)
	}
	return &result, nil
}
func (*Client) Apply(_ string, _ *netplan.State, _ time.Duration, _ func() error) netplan.Error {
	panic("NotImplemented")
}

//nolint:unused
func (dc *Client) setSingle(hint, path string, value interface{}) error {
	delta := path + "="
	if value == nil {
		delta += "NULL"
	} else {
		if jsonValue, err := json.Marshal(value); err == nil {
			delta += string(jsonValue)
		} else {
			delta += fmt.Sprint(value)
		}
	}
	res, err := dc.netplan([]string{"set", delta, "--origin-hint", hint})
	if err != nil {
		return err
	}
	logrus.Info(res)
	return nil
}
