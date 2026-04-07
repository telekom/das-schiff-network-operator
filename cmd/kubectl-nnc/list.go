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
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all NodeNetworkConfigs with summary",
		Args:  cobra.NoArgs,
		RunE:  runList,
	}
}

func runList(_ *cobra.Command, _ []string) error {
	c, err := newClient()
	if err != nil {
		return err
	}

	nncList := &networkv1alpha1.NodeNetworkConfigList{}
	if err := c.List(context.Background(), nncList); err != nil {
		return fmt.Errorf("listing NodeNetworkConfigs: %w", err)
	}

	r := renderer.New(os.Stdout, !noColor)
	r.RenderList(nncList)
	return nil
}
