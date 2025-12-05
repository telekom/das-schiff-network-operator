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

package cra

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nemith/netconf"
	ncssh "github.com/nemith/netconf/transport/ssh"
	"golang.org/x/crypto/ssh"
)

type Datastore string

const (
	Running     Datastore = "running"
	Candidate   Datastore = "candidate"
	Startup     Datastore = "startup"
	Operational Datastore = "operational"
)

type Operation string

const (
	Merge   Operation = "merge"
	Replace Operation = "replace"
	None    Operation = "none"
)

type GetData struct {
	XMLName            xml.Name  `xml:"urn:ietf:params:xml:ns:yang:ietf-netconf-nmda get-data"`
	XmlnsDatastoreAttr string    `xml:"xmlns:ds,attr"`
	Datastore          Datastore `xml:"datastore"`
	Filter             string    `xml:"xpath-filter,omitempty"`
}

type EditData struct {
	XMLName            xml.Name  `xml:"urn:ietf:params:xml:ns:yang:ietf-netconf-nmda edit-data"`
	XmlnsBaseAttr      string    `xml:"xmlns:nc,attr"`
	XmlnsDatastoreAttr string    `xml:"xmlns:ds,attr"`
	Datastore          Datastore `xml:"datastore"`
	Operation          Operation `xml:"default-operation,omitempty"`
	Data               any       `xml:"config,omitempty"`
}

type DataReply struct {
	XMLName xml.Name `xml:"data"`
	Data    []byte   `xml:",innerxml"`
}

type RPCReply struct {
	Refresh []byte `xml:"refresh-rpc,omitempty"`
	Status  []byte `xml:"status-rpc,omitempty"`
	Stop    []byte `xml:"stop-command,omitempty"`
	Exit    *int   `xml:"exit-code,omitempty"`
	Body    []byte `xml:",innerxml"`
}

type Netconf struct {
	session   *netconf.Session
	timeout   time.Duration
	sshConfig *ssh.ClientConfig
	urls      []string
}

func NewNetconf(urls []string, user, pwd string, timeout time.Duration) *Netconf {
	return &Netconf{
		urls:    urls,
		timeout: timeout,
		sshConfig: &ssh.ClientConfig{
			User: user,
			Auth: []ssh.AuthMethod{
				ssh.Password(pwd),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		},
	}
}

func (nc *Netconf) Open(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, nc.timeout)
	defer cancel()

	if nc.session != nil {
		nc.session.Close(ctx)
		nc.session = nil
	}

	for _, url := range nc.urls {
		transport, err := ncssh.Dial(ctx, "tcp", url, nc.sshConfig)
		if err != nil {
			fmt.Println(err)
			continue
		}

		nc.session, err = netconf.Open(transport)
		if err != nil {
			return fmt.Errorf("failed to open netconf session on %s: %w", url, err)
		}

		return nil
	}

	return fmt.Errorf("all CRA URLs failed due to connection issues")
}

func (nc *Netconf) Send(ctx context.Context, req, rep any, wrap bool) error {
	if nc.session == nil {
		if err := nc.Open(ctx); err != nil {
			return fmt.Errorf("failed to open netconf session: %w", err)
		}
	}

	for i := 0; i < 2; i++ {
		var err error

		subctx, cancel := context.WithTimeout(ctx, nc.timeout)
		if !wrap {
			err = nc.session.Call(subctx, req, rep)
		} else {
			err = nc.session.CallWrap(subctx, req, rep)
		}
		cancel()

		if err == nil {
			return nil
		} else if !errors.Is(err, io.EOF) {
			return fmt.Errorf("failed to send netconf message: %w", err)
		}

		if i == 0 {
			if err := nc.Open(ctx); err != nil {
				return fmt.Errorf("failed to re-open netconf session: %w", err)
			}
		}
	}

	return fmt.Errorf("all netconf send attempt failed with EOF")
}

func (nc *Netconf) Get(ctx context.Context, ds Datastore, filter string) ([]byte, error) {
	filter = strings.Join(strings.Fields(filter), " ")
	req := GetData{
		XmlnsDatastoreAttr: "urn:ietf:params:xml:ns:yang:ietf-datastores",
		Datastore:          "ds:" + ds,
		Filter:             filter,
	}

	var rep DataReply
	if err := nc.Send(ctx, &req, &rep, false); err != nil {
		return []byte{}, fmt.Errorf("failed to get netconf ds=%s filter=%s: %w", ds, filter, err)
	}

	return rep.Data, nil
}

func (nc *Netconf) GetUnmarshal(ctx context.Context, ds Datastore, filter string, out any) error {
	data, err := nc.Get(ctx, ds, filter)
	if err != nil {
		return err
	}

	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("failed to un-marshal netconf data=%s: %w", data, err)
	}

	return nil
}

func (nc *Netconf) Edit(
	ctx context.Context,
	ds Datastore,
	op Operation,
	data any,
) error {
	req := EditData{
		XmlnsBaseAttr:      "urn:ietf:params:xml:ns:netconf:base:1.0",
		XmlnsDatastoreAttr: "urn:ietf:params:xml:ns:yang:ietf-datastores",
		Datastore:          "ds:" + ds,
		Operation:          op,
	}

	switch v := data.(type) {
	case string:
		req.Data = struct {
			Inner []byte `xml:",innerxml"`
		}{
			Inner: []byte(v),
		}
	case []byte:
		req.Data = struct {
			Inner []byte `xml:",innerxml"`
		}{
			Inner: v,
		}
	default:
		req.Data = struct {
			Inner any `xml:"config"`
		}{
			Inner: data,
		}
	}

	var rep netconf.OKResp
	if err := nc.Send(ctx, &req, &rep, false); err != nil {
		return fmt.Errorf("failed to edit netconf: %w", err)
	}

	return nil
}

func (nc *Netconf) Commit(ctx context.Context) error {
	var req netconf.CommitReq
	var rep netconf.OKResp

	if err := nc.Send(ctx, &req, &rep, false); err != nil {
		return fmt.Errorf("failed to commit netconf: %w", err)
	}

	return nil
}

func (nc *Netconf) RPC(ctx context.Context, req, out any) error {
	var rep RPCReply
	if err := nc.Send(ctx, req, &rep, true); err != nil {
		return fmt.Errorf("failed to send netconf rpc: %w", err)
	}

	buf := bytes.Buffer{}
	buf.WriteString("<wrap>")
	buf.Write(rep.Body)

	refresh := rep.Refresh
	status := rep.Status
	isLong := len(refresh) > 0 && len(status) > 0

	//nolint:mnd
	for isLong && rep.Exit != nil {
		time.Sleep(50 * time.Millisecond)

		rep = RPCReply{}
		if err := nc.Send(ctx, &refresh, &rep, true); err != nil {
			return fmt.Errorf("failed to refresh netconf rpc: %w", err)
		}

		rep = RPCReply{}
		if err := nc.Send(ctx, &status, &rep, true); err != nil {
			return fmt.Errorf("failed to get status of netconf rpc: %w", err)
		}

		buf.Write(rep.Body)
	}

	buf.WriteString("</wrap>")

	if err := xml.Unmarshal(buf.Bytes(), out); err != nil {
		return fmt.Errorf("failed to unmarshal netconf rpc: %w", err)
	}

	return nil
}
