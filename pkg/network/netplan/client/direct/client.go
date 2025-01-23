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
	cmd := exec.Command(dc.binaryPath, arguments...)
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if input != "" {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return "", fmt.Errorf("failed to create pipe for writing into %s: %v", defaultNetplanBinary, err)
		}
		go func() {
			defer stdin.Close()
			_, err = io.WriteString(stdin, input)
			if err != nil {
				fmt.Printf("failed to write input into stdin: %v\n", err)
			}
		}()
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"failed to execute %s %s: '%v' '%s' '%s'",
			defaultNetplanBinary,
			strings.Join(arguments, " "),
			err,
			stdout.String(),
			stderr.String(),
		)
	}
	return stdout.String(), nil
}
func (dc *Client) netplanWithStdin(arguments []string) (string, error) {
	cmd := exec.Command(dc.binaryPath, arguments...)
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create pipe for writing into %s: %v", defaultNetplanBinary, err)
	}
	defer stdin.Close()

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"failed to execute %s %s: '%v' '%s' '%s'",
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
func (dc *Client) Version() (string, netplan.Error) {
	return "", nil
}
func (dc *Client) Generate() netplan.Error {
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
	if resultJson, err := dc.netplan([]string{"info", "--json"}); err != nil {
		return nil, netplan.ParseError(err)
	} else {
		if err := json.Unmarshal([]byte(resultJson), &infoResult); err != nil {
			return nil, netplan.ParseError(err)
		}
		return infoResult.NetPlan.Features, nil
	}
}

func (dc *Client) Get() (netplan.State, netplan.Error) {
	if resultYaml, err := dc.netplan([]string{"get", "all"}); err != nil {
		return netplan.State{}, netplan.ParseError(err)
	} else {
		var result netplan.State
		if err := yaml.Unmarshal([]byte(resultYaml), &result); err != nil {
			return netplan.State{}, netplan.ParseError(err)
		} else {
			return result, nil
		}
	}
}
func (dc *Client) Apply(hint string, state netplan.State, tryTimeout time.Duration, persistFn func() error) netplan.Error {
	panic("NotImplemented")
}

func (client *Client) setSingle(hint string, path string, value interface{}) error {
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
	if res, err := client.netplan([]string{"set", delta, "--origin-hint", hint}); err != nil {
		return err
	} else {
		logrus.Infof(res)
	}
	return nil
}
