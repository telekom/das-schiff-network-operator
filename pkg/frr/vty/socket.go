package vty

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
)

var delimiter = []byte{0x00, 0x00, 0x00, 0x00}

const bufSize = 1024

type Socket struct {
	socketPath string
}

func NewSocket(socketPath string) *Socket {
	return &Socket{
		socketPath: socketPath,
	}
}

func (s *Socket) GetConfig(xpath string, v any) error {
	data, err := s.runCmd("mgmtd", fmt.Sprintf("show mgmt datastore-contents running xpath %s json", xpath))
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, v)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}
	return nil
}

func (s *Socket) RunJSON(module, cmd string, v any) error {
	data, err := s.runCmd(module, cmd)
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, v)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}
	return nil
}

func (s *Socket) Run(module, cmd string) ([]byte, error) {
	data, err := s.runCmd(module, cmd)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func readMessage(reader *bufio.Reader) ([]byte, error) {
	buffer := make([]byte, 0)

	for {
		chunk := make([]byte, bufSize)
		n, err := reader.Read(chunk)
		if err != nil {
			return nil, fmt.Errorf("failed to read from socket: %w", err)
		}

		buffer = append(buffer, chunk[:n]...)

		index := bytes.Index(buffer, delimiter)
		if index != -1 {
			// Got the full message
			message := buffer[:index]
			return message, nil
		}
	}
}

func (s *Socket) runCmd(module, cmd string) ([]byte, error) {
	socket, err := net.Dial("unix", fmt.Sprintf("%s/%s.vty", s.socketPath, module))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to socket: %w", err)
	}
	defer socket.Close()

	reader := bufio.NewReader(socket)

	_, err = socket.Write([]byte("enable\x00"))
	if err != nil {
		return nil, fmt.Errorf("failed to write to socket: %w", err)
	}

	_, err = readMessage(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read from socket: %w", err)
	}

	cmd += "\x00"
	_, err = socket.Write([]byte(cmd))
	if err != nil {
		return nil, fmt.Errorf("failed to write to socket: %w", err)
	}

	return readMessage(reader)
}
