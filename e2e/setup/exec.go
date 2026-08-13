package setup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DockerExec runs a command inside a container via docker exec.
// Returns combined stdout+stderr output.
func DockerExec(container string, args ...string) (string, error) {
	cmdArgs := append([]string{"exec", container}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker exec %s %v: %w\n%s", container, args, err, out.String())
	}
	return out.String(), nil
}

// DockerExecShell runs a shell command inside a container.
func DockerExecShell(container, script string) (string, error) {
	return DockerExec(container, "bash", "-c", script)
}

// DockerExecInput runs a command with stdin piped in.
func DockerExecInput(container string, input string, args ...string) error {
	cmdArgs := append([]string{"exec", "-i", container}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// DockerCopy copies a file into a container.
func DockerCopy(src, container, dst string) error {
	cmd := exec.Command("docker", "cp", src, container+":"+dst)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// RunCmd runs a host command with stdout/stderr forwarded.
func RunCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// RunCmdOutput runs a host command and returns its stdout.
func RunCmdOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %v: %w", name, args, err)
	}
	return strings.TrimSpace(out.String()), nil
}

// RunCmdCombined runs a host command, forwards its output so it still appears
// inline in the job log, and returns stdout and stderr together on failure. Use
// it where the command's own message is the diagnosis -- a registry error, say
// -- and would otherwise be reduced to "exit status 1" by the time it reaches
// the caller.
func RunCmdCombined(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	// os/exec writes the two streams from separate goroutines, so the shared
	// buffer has to be synchronised.
	out := &lockedBuffer{}
	cmd.Stdout = io.MultiWriter(os.Stdout, out)
	cmd.Stderr = io.MultiWriter(os.Stderr, out)
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// lockedBuffer is a bytes.Buffer that is safe for concurrent writers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// WaitFor polls fn every interval until it returns true or timeout expires.
func WaitFor(description string, timeout, interval time.Duration, fn func() (bool, error)) error {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		ok, err := fn()
		if err != nil {
			Logf("  %s: %v", description, err)
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s (%v)", description, timeout)
		}
		Logf("  waiting for %s... (%v / %v)", description,
			time.Since(start).Round(time.Second), timeout)
		time.Sleep(interval)
	}
}

// Logf prints a timestamped log message.
func Logf(format string, args ...interface{}) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
