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

package intent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	"github.com/telekom/das-schiff-network-operator/pkg/debounce"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/assembler"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/builder"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/intent/resolver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	defaultDebounceTime = 1 * time.Second
	// DefaultTimeout is the default API timeout.
	DefaultTimeout = "60s"
)

// Reconciler is the intent-based reconciler that watches all intent CRDs
// and produces NodeNetworkConfig objects per node.
type Reconciler struct {
	logger    logr.Logger
	debouncer *debounce.Debouncer
	client    client.Client
	timeout   time.Duration
	builders  []builder.Builder
}

// NewReconciler creates a new intent reconciler.
func NewReconciler(clusterClient client.Client, logger logr.Logger, timeout time.Duration) (*Reconciler, error) {
	r := &Reconciler{
		logger:  logger,
		timeout: timeout,
		client:  clusterClient,
		builders: []builder.Builder{
			builder.NewL2ABuilder(),
			builder.NewInboundBuilder(),
			builder.NewOutboundBuilder(),
			builder.NewPodNetworkBuilder(),
			builder.NewBGPPeeringBuilder(),
			builder.NewCollectorBuilder(),
			builder.NewMirrorBuilder(),
			builder.NewAnnouncementBuilder(),
		},
	}

	r.debouncer = debounce.NewDebouncer(r.ReconcileDebounced, defaultDebounceTime, logger)

	return r, nil
}

// Reconcile triggers the debounced reconciliation.
func (r *Reconciler) Reconcile(ctx context.Context) {
	r.debouncer.Debounce(ctx)
}

// ReconcileDebounced is the main reconciliation logic executed after debounce.
func (r *Reconciler) ReconcileDebounced(ctx context.Context) error {
	r.logger.Info("starting intent reconciliation")

	timeoutCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// 1. Fetch all intent CRDs + nodes.
	fetched, err := r.fetchAll(timeoutCtx)
	if err != nil {
		return fmt.Errorf("failed to fetch intent resources: %w", err)
	}

	// 2. Resolve references (VRF, Network, Destination name → data).
	resolved, err := resolver.ResolveAll(fetched)
	if err != nil {
		return fmt.Errorf("failed to resolve references: %w", err)
	}
	// 3. Run all builders → per-node contributions.
	contributions := make(map[string][]*builder.NodeContribution) // nodeName → contributions
	for _, b := range r.builders {
		nodeContribs, err := b.Build(ctx, resolved)
		if err != nil {
			r.logger.Error(err, "builder failed", "builder", b.Name())
			return fmt.Errorf("builder %s failed: %w", b.Name(), err)
		}
		for nodeName, contrib := range nodeContribs {
			contributions[nodeName] = append(contributions[nodeName], contrib)
		}
	}

	// 4. Assemble NNC spec per node.
	for _, node := range fetched.Nodes {
		nncSpec, err := assembler.Assemble(contributions[node.Name])
		if err != nil {
			r.logger.Error(err, "assembly failed", "node", node.Name)
			continue
		}

		// Compute revision hash.
		revision, err := computeRevision(nncSpec)
		if err != nil {
			r.logger.Error(err, "revision hash failed", "node", node.Name)
			continue
		}
		nncSpec.Revision = revision

		// 5. Create or update NNC.
		if err := r.applyNNC(timeoutCtx, &node, nncSpec); err != nil {
			r.logger.Error(err, "failed to apply NodeNetworkConfig", "node", node.Name)
			continue
		}
	}

	r.logger.Info("intent reconciliation complete")
	return nil
}

func (r *Reconciler) fetchAll(ctx context.Context) (*resolver.FetchedResources, error) {
	f := &resolver.FetchedResources{}

	// Fetch nodes.
	nodeList := &corev1.NodeList{}
	if err := r.client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("error listing Nodes: %w", err)
	}
	f.Nodes = nodeList.Items

	// Fetch VRFs.
	vrfList := &nc.VRFList{}
	if err := r.client.List(ctx, vrfList); err != nil {
		return nil, fmt.Errorf("error listing VRFs: %w", err)
	}
	f.VRFs = vrfList.Items

	// Fetch Networks.
	networkList := &nc.NetworkList{}
	if err := r.client.List(ctx, networkList); err != nil {
		return nil, fmt.Errorf("error listing Networks: %w", err)
	}
	f.Networks = networkList.Items

	// Fetch Destinations.
	destList := &nc.DestinationList{}
	if err := r.client.List(ctx, destList); err != nil {
		return nil, fmt.Errorf("error listing Destinations: %w", err)
	}
	f.Destinations = destList.Items

	// Fetch Layer2Attachments.
	l2aList := &nc.Layer2AttachmentList{}
	if err := r.client.List(ctx, l2aList); err != nil {
		return nil, fmt.Errorf("error listing Layer2Attachments: %w", err)
	}
	f.Layer2Attachments = l2aList.Items

	// Fetch Inbounds.
	inboundList := &nc.InboundList{}
	if err := r.client.List(ctx, inboundList); err != nil {
		return nil, fmt.Errorf("error listing Inbounds: %w", err)
	}
	f.Inbounds = inboundList.Items

	// Fetch Outbounds.
	outboundList := &nc.OutboundList{}
	if err := r.client.List(ctx, outboundList); err != nil {
		return nil, fmt.Errorf("error listing Outbounds: %w", err)
	}
	f.Outbounds = outboundList.Items

	// Fetch PodNetworks.
	podNetworkList := &nc.PodNetworkList{}
	if err := r.client.List(ctx, podNetworkList); err != nil {
		return nil, fmt.Errorf("error listing PodNetworks: %w", err)
	}
	f.PodNetworks = podNetworkList.Items

	// Fetch BGPPeerings.
	bgpList := &nc.BGPPeeringList{}
	if err := r.client.List(ctx, bgpList); err != nil {
		return nil, fmt.Errorf("error listing BGPPeerings: %w", err)
	}
	f.BGPPeerings = bgpList.Items

	// Fetch Collectors.
	collectorList := &nc.CollectorList{}
	if err := r.client.List(ctx, collectorList); err != nil {
		return nil, fmt.Errorf("error listing Collectors: %w", err)
	}
	f.Collectors = collectorList.Items

	// Fetch TrafficMirrors.
	mirrorList := &nc.TrafficMirrorList{}
	if err := r.client.List(ctx, mirrorList); err != nil {
		return nil, fmt.Errorf("error listing TrafficMirrors: %w", err)
	}
	f.TrafficMirrors = mirrorList.Items

	// Fetch AnnouncementPolicies.
	policyList := &nc.AnnouncementPolicyList{}
	if err := r.client.List(ctx, policyList); err != nil {
		return nil, fmt.Errorf("error listing AnnouncementPolicies: %w", err)
	}
	f.AnnouncementPolicies = policyList.Items

	return f, nil
}

// applyNNC creates or updates a NodeNetworkConfig for a node.
// It skips nodes that are currently provisioning (rolling update gate).
func (r *Reconciler) applyNNC(ctx context.Context, node *corev1.Node, spec *networkv1alpha1.NodeNetworkConfigSpec) error {
	existing := &networkv1alpha1.NodeNetworkConfig{}
	err := r.client.Get(ctx, client.ObjectKey{Name: node.Name}, existing)

	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error getting NodeNetworkConfig for node %s: %w", node.Name, err)
	}

	if err == nil {
		// NNC exists — check if update needed.
		if existing.Spec.Revision == spec.Revision {
			return nil // no change
		}

		// Skip nodes currently provisioning (rolling update gate).
		if existing.Status.ConfigStatus == "provisioning" {
			r.logger.Info("skipping node — currently provisioning", "node", node.Name)
			return nil
		}

		existing.Spec = *spec
		return r.client.Update(ctx, existing)
	}

	// NNC does not exist — create it.
	nnc := &networkv1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
		},
		Spec: *spec,
	}

	if err := controllerutil.SetOwnerReference(node, nnc, r.client.Scheme()); err != nil {
		return fmt.Errorf("error setting owner reference for NNC %s: %w", node.Name, err)
	}

	return r.client.Create(ctx, nnc)
}

// computeRevision computes a SHA256 hash of the NNC spec for change detection.
func computeRevision(spec *networkv1alpha1.NodeNetworkConfigSpec) (string, error) {
	// Zero out revision before hashing to avoid self-reference.
	specCopy := *spec
	specCopy.Revision = ""

	data, err := json.Marshal(specCopy)
	if err != nil {
		return "", fmt.Errorf("error marshaling NNC spec: %w", err)
	}

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}
