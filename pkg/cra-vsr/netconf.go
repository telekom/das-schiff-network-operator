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
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/nemith/netconf"
	ncssh "github.com/nemith/netconf/transport/ssh"
	"golang.org/x/crypto/ssh"
)

type Netconf struct {
	session *netconf.Session
	timeout time.Duration
}

type GetConfigReq struct {
	netconf.GetConfigReq
	Filter string `xml:",innerxml"`
}

func (nc *Netconf) openSession(
	ctx context.Context, urls []string,
	timeout time.Duration, sshConfig *ssh.ClientConfig,
) error {
	nc.timeout = timeout

	ctx, cancel := context.WithTimeout(ctx, nc.timeout)
	defer cancel()

	if nc.session != nil {
		nc.session.Close(ctx)
		nc.session = nil
	}

	for _, url := range urls {
		transport, err := ncssh.Dial(ctx, "tcp", url, sshConfig)
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

func (nc *Netconf) getConfig(ctx context.Context, source netconf.Datastore) ([]byte, error) {
	req := GetConfigReq{
		GetConfigReq: netconf.GetConfigReq{
			Source: source,
		},
		Filter: `<filter>
				<config xmlns="urn:6wind:vrouter">
				</config>
			</filter>`,
	}
	var reply netconf.GetConfigReply

	ctx, cancel := context.WithTimeout(ctx, nc.timeout)
	defer cancel()

	if err := nc.session.Call(ctx, &req, &reply); err != nil {
		return []byte{}, fmt.Errorf("failed to get netconf %s: %w", source, err)
	}

	return reply.Config, nil
}

func (nc *Netconf) getVRouter(ctx context.Context, source netconf.Datastore) (*VRouter, error) {
	var vrouter VRouter

	configXML, err := nc.getConfig(ctx, source)
	if err != nil {
		return nil, err
	}

	if err := xml.Unmarshal(configXML, &vrouter); err != nil {
		return nil, fmt.Errorf("failed to un-marshal netconf %s: %w", source, err)
	}

	return &vrouter, nil
}

func (nc *Netconf) editConfig(
	ctx context.Context,
	config any,
	source netconf.Datastore,
	strategy netconf.MergeStrategy,
) error {
	ctx, cancel := context.WithTimeout(ctx, nc.timeout)
	defer cancel()

	err := nc.session.EditConfig(ctx, source, config,
		netconf.WithDefaultMergeStrategy(strategy))
	if err != nil {
		return fmt.Errorf("failed to edit netconf: %w", err)
	}

	return nil
}

func (nc *Netconf) commit(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, nc.timeout)
	defer cancel()

	err := nc.session.Commit(ctx)
	if err != nil {
		return fmt.Errorf("failed to commit netconf: %w", err)
	}

	return nil
}
