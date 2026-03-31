/*
Copyright 2024.

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

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/cmd/kubectl-nnc/renderer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <node-name>",
		Short: "Show detailed NodeNetworkConfig for a node",
		Args:  cobra.ExactArgs(1),
		RunE:  runShow,
	}
}

func runShow(_ *cobra.Command, args []string) error {
	nodeName := args[0]

	c, err := newClient()
	if err != nil {
		return err
	}

	nnc := &networkv1alpha1.NodeNetworkConfig{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: nodeName}, nnc); err != nil {
		return fmt.Errorf("getting NodeNetworkConfig for node %q: %w", nodeName, err)
	}

	// Parse origins annotation if present.
	origins := renderer.ParseOrigins(nnc.Annotations)

	r := renderer.New(os.Stdout, !noColor)
	r.RenderNNC(nnc, origins)
	return nil
}
