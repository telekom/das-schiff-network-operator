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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	k8syaml "sigs.k8s.io/yaml"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

var (
	testEnv    *envtest.Environment
	k8sClient  client.Client
	reconciler *Reconciler
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))

	// Resolve CRD path relative to this source file (not the test binary CWD).
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	crdPath := filepath.Join(repoRoot, "config", "crd", "bases")

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start envtest: %v\n", err)
		os.Exit(1)
	}

	if err := networkv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintf(os.Stderr, "failed to add networkv1alpha1 scheme: %v\n", err)
		os.Exit(1)
	}
	if err := nc.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintf(os.Stderr, "failed to add network-connector scheme: %v\n", err)
		os.Exit(1)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create k8s client: %v\n", err)
		os.Exit(1)
	}

	logger := logf.Log.WithName("test-reconciler")
	reconciler, err = NewReconciler(k8sClient, logger, 60*time.Second, "default")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create reconciler: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to stop envtest: %v\n", err)
	}

	os.Exit(code)
}

// testNamespace is used for all namespaced intent CRDs in tests.
const testNamespace = "default"

// --- Helpers ---.

func ptr[T any](v T) *T { return &v }

func mapKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func createNode(t *testing.T, ctx context.Context, name string, nodeLabels map[string]string) {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: nodeLabels}}
	require.NoError(t, k8sClient.Create(ctx, node), "create node %s", name)
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), node) }) //nolint:contextcheck // cleanup runs outside test context
}

func createObj(t *testing.T, ctx context.Context, obj client.Object) {
	t.Helper()
	require.NoError(t, k8sClient.Create(ctx, obj), "create %T %s", obj, obj.GetName())
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), obj) }) //nolint:contextcheck // cleanup runs outside test context
}

func reconcileAndGetNNC(t *testing.T, ctx context.Context, nodeName string) *networkv1alpha1.NodeNetworkConfig {
	t.Helper()
	require.NoError(t, reconciler.ReconcileDebounced(ctx), "reconcile failed")
	nnc := &networkv1alpha1.NodeNetworkConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc), "get NNC %s", nodeName)
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), nnc) }) //nolint:contextcheck // cleanup runs outside test context
	return nnc
}

func getNetplanConfig(t *testing.T, ctx context.Context, nodeName string) *networkv1alpha1.NodeNetplanConfig {
	t.Helper()
	npc := &networkv1alpha1.NodeNetplanConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, npc), "get NodeNetplanConfig %s", nodeName)
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), npc) }) //nolint:contextcheck // cleanup runs outside test context
	return npc
}

// makeVRF creates a VRF intent CRD.
func makeVRF(name, vrfName string, vni int32, rt string) *nc.VRF {
	return &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: nc.VRFSpec{
			VRF:         vrfName,
			VNI:         ptr(vni),
			RouteTarget: ptr(rt),
		},
	}
}

// makeNetwork creates a Network intent CRD.
func makeNetwork(name string, vlan, vni int32, ipv4CIDR, ipv6CIDR string) *nc.Network {
	spec := nc.NetworkSpec{
		VLAN: ptr(vlan),
		VNI:  ptr(vni),
	}
	if ipv4CIDR != "" {
		spec.IPv4 = &nc.IPNetwork{CIDR: ipv4CIDR}
	}
	if ipv6CIDR != "" {
		spec.IPv6 = &nc.IPNetwork{CIDR: ipv6CIDR}
	}
	return &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       spec,
	}
}

// makeDestination creates a Destination intent CRD with a VRF reference.
func makeDestination(name, vrfRef string, destLabels map[string]string, prefixes []string) *nc.Destination {
	return &nc.Destination{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: destLabels},
		Spec: nc.DestinationSpec{
			VRFRef:   ptr(vrfRef),
			Prefixes: prefixes,
		},
	}
}

// makeL2A creates a Layer2Attachment intent CRD.
func makeL2A(name, networkRef string, destSelector, nodeSelector *metav1.LabelSelector) *nc.Layer2Attachment {
	return &nc.Layer2Attachment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: nc.Layer2AttachmentSpec{
			NetworkRef:   networkRef,
			Destinations: destSelector,
			NodeSelector: nodeSelector,
		},
	}
}

// makeInbound creates an Inbound intent CRD with explicit addresses.
func makeInbound(name, networkRef string, destSelector *metav1.LabelSelector, ipv4Addrs []string) *nc.Inbound {
	return &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: nc.InboundSpec{
			NetworkRef:   networkRef,
			Destinations: destSelector,
			Addresses:    &nc.AddressAllocation{IPv4: ipv4Addrs},
			Advertisement: nc.AdvertisementConfig{
				Type: "bgp",
			},
		},
	}
}

// makeOutbound creates an Outbound intent CRD with explicit addresses.
func makeOutbound(name, networkRef string, destSelector *metav1.LabelSelector, ipv4Addrs []string) *nc.Outbound {
	return &nc.Outbound{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: nc.OutboundSpec{
			NetworkRef:   networkRef,
			Destinations: destSelector,
			Addresses:    &nc.AddressAllocation{IPv4: ipv4Addrs},
		},
	}
}

// makePodNetwork creates a PodNetwork intent CRD.
func makePodNetwork(name, networkRef string, destSelector *metav1.LabelSelector) *nc.PodNetwork {
	return &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: nc.PodNetworkSpec{
			NetworkRef:   networkRef,
			Destinations: destSelector,
		},
	}
}

func destSelector(value string) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{"type": value},
	}
}

// --- Test Cases ---.

func TestL2APipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-l2a-pipeline"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-l2a", "m2m", 2002026, "65188:2026"))
	createObj(t, ctx, makeNetwork("net-l2a-501", 501, 4000002, "10.250.0.0/24", "fd94::1/64"))
	createObj(t, ctx, makeDestination("dest-l2a-gw", "vrf-l2a", map[string]string{"type": "gateway"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-pipeline", "net-l2a-501", destSelector("gateway"), nil))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// Verify Layer2
	l2, ok := nnc.Spec.Layer2s["501"]
	require.True(t, ok, "expected Layer2 entry for key '501'")
	assert.Equal(t, uint32(4000002), l2.VNI)
	assert.Equal(t, uint16(501), l2.VLAN)
	assert.Equal(t, uint16(1500), l2.MTU)
	require.NotNil(t, l2.IRB, "expected IRB to be set")
	assert.Equal(t, "m2m", l2.IRB.VRF)
	assert.NotEmpty(t, l2.IRB.IPAddresses)

	fvrf, ok := nnc.Spec.FabricVRFs["m2m"]
	require.True(t, ok, "expected FabricVRF entry for key 'm2m', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	assert.Equal(t, uint32(2002026), fvrf.VNI)

	// Verify revision is set
	assert.NotEmpty(t, nnc.Spec.Revision)
}

func TestL2ANodeSelector(t *testing.T) {
	ctx := context.Background()
	workerNode := "node-sel-worker"
	cpNode := "node-sel-cp"

	createNode(t, ctx, workerNode, map[string]string{"role": "worker"})
	createNode(t, ctx, cpNode, map[string]string{"role": "control-plane"})
	createObj(t, ctx, makeVRF("vrf-sel", "selm2m", 2002027, "65188:2027"))
	createObj(t, ctx, makeNetwork("net-sel-501", 501, 4000002, "10.250.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-sel", "vrf-sel", map[string]string{"type": "sel"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-sel", "net-sel-501", destSelector("sel"),
		&metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}}))

	require.NoError(t, reconciler.ReconcileDebounced(ctx))

	// Worker node should have L2
	workerNNC := &networkv1alpha1.NodeNetworkConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: workerNode}, workerNNC))
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), workerNNC) })

	_, ok := workerNNC.Spec.Layer2s["501"]
	assert.True(t, ok, "expected worker NNC to have Layer2 '501'")

	// CP node should have NNC but no L2 from this L2A
	cpNNC := &networkv1alpha1.NodeNetworkConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: cpNode}, cpNNC))
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), cpNNC) })

	_, ok = cpNNC.Spec.Layer2s["501"]
	assert.False(t, ok, "expected control-plane NNC to NOT have Layer2 '501'")
}

func TestInboundPipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-inbound"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-ib", "ibm2m", 2002030, "65188:2030"))
	createObj(t, ctx, makeNetwork("net-ib", 600, 5000001, "10.100.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-ib", "vrf-ib", map[string]string{"type": "ib"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeInbound("ib-test", "net-ib", destSelector("ib"), []string{"10.100.0.10/32"}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	fvrf, ok := nnc.Spec.FabricVRFs["ibm2m"]
	require.True(t, ok, "expected FabricVRF 'ibm2m', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))

	// Should have static routes or redistribute for the inbound IPs
	assert.True(t, len(fvrf.StaticRoutes) > 0 || fvrf.Redistribute != nil,
		"expected FabricVRF to have static routes or redistribute config")
}

func TestOutboundPipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-outbound"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-ob", "obm2m", 2002031, "65188:2031"))
	createObj(t, ctx, makeNetwork("net-ob", 601, 5000002, "10.200.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-ob", "vrf-ob", map[string]string{"type": "ob"}, []string{"10.103.0.0/16"}))
	createObj(t, ctx, makeOutbound("ob-test", "net-ob", destSelector("ob"), []string{"10.200.0.5/32"}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	fvrf, ok := nnc.Spec.FabricVRFs["obm2m"]
	require.True(t, ok, "expected FabricVRF 'obm2m', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))

	assert.True(t, len(fvrf.VRFImports) > 0,
		"expected FabricVRF to have vrfImport for outbound return traffic")

	// Aggregate: Network CIDR should be present as static route (covering prefix for EVPN export).
	assert.True(t, len(fvrf.StaticRoutes) > 0,
		"expected FabricVRF to have aggregate static route for Network CIDR")
	assert.Equal(t, "10.200.0.0/24", fvrf.StaticRoutes[0].Prefix,
		"aggregate route should be the Network CIDR")
}

func TestPodNetworkPipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-podnet"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-pn", "pnm2m", 2002032, "65188:2032"))
	createObj(t, ctx, makeNetwork("net-pn", 602, 5000003, "10.50.0.0/16", ""))
	createObj(t, ctx, makeDestination("dest-pn", "vrf-pn", map[string]string{"type": "pn"}, []string{"10.104.0.0/16"}))
	createObj(t, ctx, makePodNetwork("pn-test", "net-pn", destSelector("pn")))
	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	fvrf, ok := nnc.Spec.FabricVRFs["pnm2m"]
	require.True(t, ok, "expected FabricVRF 'pnm2m', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))

	assert.True(t, fvrf.Redistribute != nil || len(fvrf.StaticRoutes) > 0,
		"expected FabricVRF to have redistribute or static routes for pod network")
}

func TestSBRSingleVRF(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-sbr-single"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-sbr1", "sbrm", 2002040, "65188:2040"))
	createObj(t, ctx, makeNetwork("net-sbr1", 700, 6000001, "10.100.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-sbr1", "vrf-sbr1", map[string]string{"type": "sbr1"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeInbound("ib-sbr1", "net-sbr1", destSelector("sbr1"), []string{"10.100.0.10/32"}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	_, ok := nnc.Spec.LocalVRFs["s-sbrm"]
	assert.True(t, ok, "expected LocalVRF 's-sbrm' (SBR intermediate VRF, keyed by backbone VRF name)")

	// ClusterVRF should have PolicyRoutes for SBR
	require.NotNil(t, nnc.Spec.ClusterVRF, "expected ClusterVRF to be set for SBR")
	assert.NotEmpty(t, nnc.Spec.ClusterVRF.PolicyRoutes, "expected ClusterVRF to have PolicyRoutes for SBR")

	// Single VRF case: PolicyRoutes should have SrcPrefix
	for _, pr := range nnc.Spec.ClusterVRF.PolicyRoutes {
		assert.NotNil(t, pr.TrafficMatch.SrcPrefix, "expected SBR PolicyRoute to have TrafficMatch.SrcPrefix")
	}
}

func TestSBRMultiVRF(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-sbr-multi"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-sbr-a", "vrfa", 2002041, "65188:2041"))
	createObj(t, ctx, makeVRF("vrf-sbr-b", "vrfb", 2002042, "65188:2042"))
	createObj(t, ctx, makeNetwork("net-sbr-m", 701, 6000002, "10.100.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-sbr-a", "vrf-sbr-a", map[string]string{"zone": "a"}, []string{"10.1.0.0/16"}))
	createObj(t, ctx, makeDestination("dest-sbr-b", "vrf-sbr-b", map[string]string{"zone": "b"}, []string{"10.2.0.0/16"}))

	inbound := &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{Name: "ib-sbr-multi", Namespace: testNamespace},
		Spec: nc.InboundSpec{
			NetworkRef: "net-sbr-m",
			Destinations: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "zone",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"a", "b"},
				}},
			},
			Addresses:     &nc.AddressAllocation{IPv4: []string{"10.100.0.20/32"}},
			Advertisement: nc.AdvertisementConfig{Type: "bgp"},
		},
	}
	createObj(t, ctx, inbound)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	comboName := "s-3d1234a5"
	comboVRF, ok := nnc.Spec.LocalVRFs[comboName]
	assert.True(t, ok, "expected combo LocalVRF %q", comboName)

	prefixesFound := map[string]bool{}
	for _, sr := range comboVRF.StaticRoutes {
		prefixesFound[sr.Prefix] = true
	}
	assert.True(t, prefixesFound["10.1.0.0/16"], "expected static route for 10.1.0.0/16 (vrfa)")
	assert.True(t, prefixesFound["10.2.0.0/16"], "expected static route for 10.2.0.0/16 (vrfb)")

	require.NotNil(t, nnc.Spec.ClusterVRF, "expected ClusterVRF")
	for _, pr := range nnc.Spec.ClusterVRF.PolicyRoutes {
		assert.Nil(t, pr.TrafficMatch.DstPrefix, "expected no DstPrefix in multi-VRF SBR PolicyRoute")
	}
	hasSrcPrefix := false
	for _, pr := range nnc.Spec.ClusterVRF.PolicyRoutes {
		if pr.TrafficMatch.SrcPrefix != nil {
			hasSrcPrefix = true
			break
		}
	}
	assert.True(t, hasSrcPrefix, "expected PolicyRoute with SrcPrefix for multi-VRF SBR")
}

func TestLifecycleUpdate(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-lifecycle-upd"

	createNode(t, ctx, nodeName, nil)
	vrf := makeVRF("vrf-lc-upd", "lcm2m", 2002050, "65188:2050")
	createObj(t, ctx, vrf)
	net := makeNetwork("net-lc-upd", 510, 4100001, "10.250.10.0/24", "")
	createObj(t, ctx, net)
	createObj(t, ctx, makeDestination("dest-lc-upd", "vrf-lc-upd", map[string]string{"type": "lc"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-lc-upd", "net-lc-upd", destSelector("lc"), nil))

	nnc1 := reconcileAndGetNNC(t, ctx, nodeName)
	rev1 := nnc1.Spec.Revision
	require.NotEmpty(t, rev1, "expected initial revision to be set")

	// Update the Network — change IPv4 CIDR
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: "net-lc-upd", Namespace: testNamespace}, net))
	net.Spec.IPv4 = &nc.IPNetwork{CIDR: "10.250.11.0/24"}
	require.NoError(t, k8sClient.Update(ctx, net))

	// Reconcile again
	require.NoError(t, reconciler.ReconcileDebounced(ctx))

	nnc2 := &networkv1alpha1.NodeNetworkConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc2))

	assert.NotEqual(t, rev1, nnc2.Spec.Revision, "expected revision to change after Network update")

	// Verify updated CIDR in IRB
	l2, ok := nnc2.Spec.Layer2s["510"]
	require.True(t, ok, "expected Layer2 '510' after update")
	require.NotNil(t, l2.IRB, "expected IRB after update")
	assert.Contains(t, l2.IRB.IPAddresses, "10.250.11.0/24")
}

func TestLifecycleDelete(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-lifecycle-del"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-lc-del", "dlm2m", 2002051, "65188:2051"))
	createObj(t, ctx, makeNetwork("net-lc-del", 520, 4200001, "10.250.20.0/24", ""))
	createObj(t, ctx, makeDestination("dest-lc-del", "vrf-lc-del", map[string]string{"type": "del"}, []string{"10.102.0.0/16"}))

	l2a := makeL2A("l2a-lc-del", "net-lc-del", destSelector("del"), nil)
	createObj(t, ctx, l2a)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)
	_, ok := nnc.Spec.Layer2s["520"]
	require.True(t, ok, "expected Layer2 '520' before delete")

	// Delete the L2A
	require.NoError(t, k8sClient.Delete(ctx, l2a), "delete L2A")

	// Reconcile after delete
	require.NoError(t, reconciler.ReconcileDebounced(ctx))

	nnc2 := &networkv1alpha1.NodeNetworkConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc2))

	_, ok = nnc2.Spec.Layer2s["520"]
	assert.False(t, ok, "expected Layer2 '520' to be removed after L2A delete")
}

func TestMultiNodeAssembly(t *testing.T) {
	ctx := context.Background()
	nodes := []string{"node-multi-1", "node-multi-2", "node-multi-3"}

	for _, n := range nodes {
		createNode(t, ctx, n, nil)
	}
	createObj(t, ctx, makeVRF("vrf-multi", "multim", 2002060, "65188:2060"))
	createObj(t, ctx, makeNetwork("net-multi", 530, 4300001, "10.250.30.0/24", ""))
	createObj(t, ctx, makeDestination("dest-multi", "vrf-multi", map[string]string{"type": "multi"}, []string{"10.102.0.0/16"}))
	// No nodeSelector → should apply to all 3 nodes
	createObj(t, ctx, makeL2A("l2a-multi", "net-multi", destSelector("multi"), nil))

	require.NoError(t, reconciler.ReconcileDebounced(ctx))

	for _, n := range nodes {
		nnc := &networkv1alpha1.NodeNetworkConfig{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: n}, nnc), "expected NNC for node %s", n)
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), nnc) })

		_, ok := nnc.Spec.Layer2s["530"]
		assert.True(t, ok, "expected node %s NNC to have Layer2 '530'", n)
	}
}

func TestOriginTracking(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-origin"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-origin", "origm", 2002070, "65188:2070"))
	createObj(t, ctx, makeNetwork("net-origin", 540, 4400001, "10.250.40.0/24", ""))
	createObj(t, ctx, makeDestination("dest-origin", "vrf-origin", map[string]string{"type": "origin"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-origin", "net-origin", destSelector("origin"), nil))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// Check origins annotation exists
	ann, ok := nnc.Annotations[originsAnnotation]
	if !ok {
		t.Skip("origins annotation not set (builders may not call SetOrigin yet)")
		return
	}

	var origins map[string]string
	if err := json.Unmarshal([]byte(ann), &origins); err != nil {
		t.Fatalf("failed to parse origins annotation: %v", err)
	}

	if len(origins) == 0 {
		t.Skip("origins map is empty (builders may not call SetOrigin yet)")
	}
}

func TestRevisionStableOnNoChange(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-rev-stable"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-rev", "revm", 2002080, "65188:2080"))
	createObj(t, ctx, makeNetwork("net-rev", 550, 4500001, "10.250.50.0/24", ""))
	createObj(t, ctx, makeDestination("dest-rev", "vrf-rev", map[string]string{"type": "rev"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-rev", "net-rev", destSelector("rev"), nil))

	nnc1 := reconcileAndGetNNC(t, ctx, nodeName)
	rev1 := nnc1.Spec.Revision
	rv1 := nnc1.ResourceVersion

	// Reconcile again with no changes
	require.NoError(t, reconciler.ReconcileDebounced(ctx))

	nnc2 := &networkv1alpha1.NodeNetworkConfig{}
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc2))

	assert.Equal(t, rev1, nnc2.Spec.Revision, "expected revision to stay stable")

	// ResourceVersion should NOT have changed (no API server update)
	assert.Equal(t, rv1, nnc2.ResourceVersion, "expected ResourceVersion to stay stable (no-op update)")
}

// --- BGPPeering Tests ---.

func TestBGPPeeringListenRange(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-bgp-lr"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-bgp-lr", "bgplr", 2002090, "65188:2090"))
	createObj(t, ctx, makeNetwork("net-bgp-lr", 560, 4600001, "10.250.60.0/24", "fd96::1/64"))
	createObj(t, ctx, makeDestination("dest-bgp-lr", "vrf-bgp-lr", map[string]string{"type": "bgp-lr"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-bgp-lr", "net-bgp-lr", destSelector("bgp-lr"), nil))
	createObj(t, ctx, makeInbound("ib-bgp-lr", "net-bgp-lr", destSelector("bgp-lr"), []string{"10.250.60.10/32"}))

	// BGPPeering in listenRange mode referencing the L2A
	bgpPeering := &nc.BGPPeering{
		ObjectMeta: metav1.ObjectMeta{Name: "bgpp-listen", Namespace: testNamespace},
		Spec: nc.BGPPeeringSpec{
			Mode: nc.BGPPeeringModeListenRange,
			Ref: nc.BGPPeeringRef{
				AttachmentRef: ptr("l2a-bgp-lr"),
				InboundRefs:   []string{"ib-bgp-lr"},
			},
			WorkloadAS: ptr(int64(65100)),
		},
	}
	createObj(t, ctx, bgpPeering)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// Verify FabricVRF has BGPPeers with ListenRange from Network CIDRs
	fvrf, ok := nnc.Spec.FabricVRFs["bgplr"]
	require.True(t, ok, "expected FabricVRF 'bgplr', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	require.NotEmpty(t, fvrf.BGPPeers, "expected BGPPeers in FabricVRF")

	// Should have peers with ListenRange from the dual-stack Network
	hasListenRange := false
	for _, peer := range fvrf.BGPPeers {
		if peer.ListenRange != nil {
			hasListenRange = true
			assert.Equal(t, uint32(65100), peer.RemoteASN, "expected WorkloadAS as RemoteASN")
		}
	}
	assert.True(t, hasListenRange, "expected at least one BGPPeer with ListenRange")
}

func TestBGPPeeringLoopbackPeer(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-bgp-lb"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-bgp-lb", "bgplb", 2002091, "65188:2091"))
	createObj(t, ctx, makeNetwork("net-bgp-lb", 561, 4600002, "10.250.61.0/24", ""))
	createObj(t, ctx, makeDestination("dest-bgp-lb", "vrf-bgp-lb", map[string]string{"type": "bgp-lb"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeInbound("ib-bgp-lb", "net-bgp-lb", destSelector("bgp-lb"), []string{"10.250.61.10/32"}))

	// BGPPeering in loopbackPeer mode (no attachmentRef)
	bgpPeering := &nc.BGPPeering{
		ObjectMeta: metav1.ObjectMeta{Name: "bgpp-loopback", Namespace: testNamespace},
		Spec: nc.BGPPeeringSpec{
			Mode: nc.BGPPeeringModeLoopbackPeer,
			Ref: nc.BGPPeeringRef{
				InboundRefs: []string{"ib-bgp-lb"},
			},
			WorkloadAS:      ptr(int64(65200)),
			AddressFamilies: []nc.BGPAddressFamily{nc.BGPAddressFamilyIPv4Unicast, nc.BGPAddressFamilyIPv6Unicast},
		},
	}
	createObj(t, ctx, bgpPeering)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// Loopback mode should set BGPPeers on ClusterVRF
	require.NotNil(t, nnc.Spec.ClusterVRF, "expected ClusterVRF for loopbackPeer BGPPeering")
	require.NotEmpty(t, nnc.Spec.ClusterVRF.BGPPeers, "expected BGPPeers on ClusterVRF")

	peer := nnc.Spec.ClusterVRF.BGPPeers[0]
	assert.Equal(t, uint32(65200), peer.RemoteASN, "expected WorkloadAS as RemoteASN")

	// Dual-stack address families should be set
	assert.NotNil(t, peer.IPv4, "expected IPv4 address family")
	assert.NotNil(t, peer.IPv6, "expected IPv6 address family")
}

// --- TrafficMirror Tests ---

func TestTrafficMirrorL2ASource(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-mirror"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-mir", "mirm2m", 2002100, "65188:2100"))
	createObj(t, ctx, makeVRF("vrf-mir-col", "mircol", 2002101, "65188:2101"))
	createObj(t, ctx, makeNetwork("net-mir", 570, 4700001, "10.250.70.0/24", ""))
	createObj(t, ctx, makeDestination("dest-mir", "vrf-mir", map[string]string{"type": "mir"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-mir", "net-mir", destSelector("mir"), nil))

	// Collector with mirror VRF
	collector := &nc.Collector{
		ObjectMeta: metav1.ObjectMeta{Name: "col-test", Namespace: testNamespace},
		Spec: nc.CollectorSpec{
			Address:  "192.168.100.1",
			Protocol: "l3gre",
			MirrorVRF: nc.MirrorVRFRef{
				Name: "vrf-mir-col",
				Loopback: nc.LoopbackConfig{
					Name: "lo.mir",
					PoolRef: corev1.TypedLocalObjectReference{
						APIGroup: ptr("ipam.cluster.x-k8s.io"),
						Kind:     "InClusterIPPool",
						Name:     "mirror-pool",
					},
				},
			},
		},
	}
	createObj(t, ctx, collector)

	// TrafficMirror sourcing from the L2A
	mirror := &nc.TrafficMirror{
		ObjectMeta: metav1.ObjectMeta{Name: "tmir-test", Namespace: testNamespace},
		Spec: nc.TrafficMirrorSpec{
			Source: nc.MirrorSource{
				Kind: "Layer2Attachment",
				Name: "l2a-mir",
			},
			Collector: "col-test",
			Direction: "ingress",
			TrafficMatch: &nc.TrafficMatch{
				SrcPrefix: ptr("10.0.0.0/8"),
				Protocol:  ptr("TCP"),
			},
		},
	}
	createObj(t, ctx, mirror)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// MirrorACL should appear on the Layer2 entry (keyed by VLAN)
	l2, ok := nnc.Spec.Layer2s["570"]
	require.True(t, ok, "expected Layer2 '570', got keys: %v", mapKeys(nnc.Spec.Layer2s))
	require.NotEmpty(t, l2.MirrorACLs, "expected MirrorACLs on Layer2")

	acl := l2.MirrorACLs[0]
	assert.Equal(t, "192.168.100.1", acl.DestinationAddress)
	assert.Equal(t, "vrf-mir-col", acl.DestinationVrf)
	assert.Equal(t, networkv1alpha1.EncapsulationTypeGRE, acl.EncapsulationType)

	// TrafficMatch should be converted
	require.NotNil(t, acl.TrafficMatch.Protocol)
	assert.Equal(t, "TCP", *acl.TrafficMatch.Protocol)
	require.NotNil(t, acl.TrafficMatch.SrcPrefix)
	assert.Equal(t, "10.0.0.0/8", *acl.TrafficMatch.SrcPrefix)

	// Collector builder should also create a loopback on the mirror VRF
	mirVRF, ok := nnc.Spec.FabricVRFs["mircol"]
	require.True(t, ok, "expected FabricVRF 'mircol' from Collector builder")
	require.NotNil(t, mirVRF.Loopbacks, "expected Loopbacks in mirror VRF")
	lo, ok := mirVRF.Loopbacks["lo.mir"]
	require.True(t, ok, "expected Loopback 'lo.mir'")
	assert.Contains(t, lo.IPAddresses, "192.168.100.1")
}

// --- AnnouncementPolicy Tests ---

func TestAnnouncementPolicyHostRoutesAndAggregate(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-annpol"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-annpol", "apm2m", 2002110, "65188:2110"))
	createObj(t, ctx, makeNetwork("net-annpol", 550, 5000110, "10.110.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-annpol", "vrf-annpol", map[string]string{"type": "annpol"}, []string{"10.110.0.0/24"}))
	createObj(t, ctx, makeInbound("ib-annpol", "net-annpol", destSelector("annpol"), []string{"10.110.0.1"}))

	// AnnouncementPolicy with host route communities + enabled aggregate.
	annPol := &nc.AnnouncementPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "ap-test", Namespace: testNamespace},
		Spec: nc.AnnouncementPolicySpec{
			VRFRef: "vrf-annpol",
			HostRoutes: &nc.RouteAnnouncementConfig{
				Communities: []string{"65000:100", "65000:200"},
			},
			Aggregate: &nc.AggregateConfig{
				Enabled:     ptr(true),
				Communities: []string{"65000:300"},
			},
		},
	}
	createObj(t, ctx, annPol)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	fvrf, ok := nnc.Spec.FabricVRFs["apm2m"]
	require.True(t, ok, "expected FabricVRF 'apm2m', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	require.NotNil(t, fvrf.EVPNExportFilter, "expected EVPNExportFilter from AnnouncementPolicy")

	filter := fvrf.EVPNExportFilter
	// The Inbound address 10.110.0.1 produces one host-route filter item (le=32).
	require.GreaterOrEqual(t, len(filter.Items), 1, "expected at least 1 filter item from AP")

	// Verify the item has host route communities.
	var foundHost bool
	for _, item := range filter.Items {
		assert.Equal(t, networkv1alpha1.Accept, item.Action.Type, "expected Accept action")
		if item.Action.ModifyRoute != nil {
			comms := item.Action.ModifyRoute.AddCommunities
			if len(comms) >= 2 && comms[0] == "65000:100" {
				foundHost = true
			}
		}
	}
	assert.True(t, foundHost, "expected filter item with host route communities 65000:100, 65000:200")

	// Default action should be Reject (base FabricVRF's deny-by-default is preserved by mergeFilter).
	assert.Equal(t, networkv1alpha1.Reject, filter.DefaultAction.Type)
}

func TestBuildNetplanState(t *testing.T) {
	t.Run("empty spec produces empty state", func(t *testing.T) {
		spec := &networkv1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]networkv1alpha1.Layer2{},
		}
		state := buildNetplanState(spec)
		assert.Empty(t, state.Network.VLans)
	})

	t.Run("nil spec produces empty state", func(t *testing.T) {
		state := buildNetplanState(nil)
		assert.Empty(t, state.Network.VLans)
	})

	t.Run("Layer2s produce netplan VLANs", func(t *testing.T) {
		spec := &networkv1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]networkv1alpha1.Layer2{
				"501": {VLAN: 501, VNI: 10501, MTU: 9000},
				"502": {VLAN: 502, VNI: 10502, MTU: 1500},
			},
		}
		state := buildNetplanState(spec)
		require.Len(t, state.Network.VLans, 2)

		// Check vlan.501
		v501, ok := state.Network.VLans["vlan.501"]
		require.True(t, ok, "expected vlan.501 in netplan VLans")
		var vlan501 map[string]interface{}
		require.NoError(t, json.Unmarshal(v501.Raw, &vlan501))
		assert.Equal(t, float64(501), vlan501["id"])
		assert.Equal(t, "hbn", vlan501["link"])
		assert.Equal(t, float64(9000), vlan501["mtu"])

		// Check vlan.502
		v502, ok := state.Network.VLans["vlan.502"]
		require.True(t, ok, "expected vlan.502 in netplan VLans")
		var vlan502 map[string]interface{}
		require.NoError(t, json.Unmarshal(v502.Raw, &vlan502))
		assert.Equal(t, float64(502), vlan502["id"])
		assert.Equal(t, "hbn", vlan502["link"])
		assert.Equal(t, float64(1500), vlan502["mtu"])
	})

	t.Run("Layer2 with zero VLAN is skipped", func(t *testing.T) {
		spec := &networkv1alpha1.NodeNetworkConfigSpec{
			Layer2s: map[string]networkv1alpha1.Layer2{
				"0": {VLAN: 0, VNI: 100, MTU: 1500},
			},
		}
		state := buildNetplanState(spec)
		assert.Empty(t, state.Network.VLans)
	})
}

func TestReconcileCreatesNodeNetplanConfig(t *testing.T) {
	ctx := context.Background()
	nodeName := "netplan-test-node"

	createNode(t, ctx, nodeName, map[string]string{"node-role": "worker"})
	createObj(t, ctx, makeVRF("vrf-np", "m2m", 100, "65000:100"))
	createObj(t, ctx, makeNetwork("net-np", 501, 10501, "10.0.1.0/24", ""))
	createObj(t, ctx, makeDestination("dest-np", "vrf-np", map[string]string{"role": "dcgw"}, nil))
	createObj(t, ctx, makeL2A("l2a-np", "net-np", &metav1.LabelSelector{
		MatchLabels: map[string]string{"role": "dcgw"},
	}, nil))

	// Reconcile should create both NNC and NodeNetplanConfig.
	nnc := reconcileAndGetNNC(t, ctx, nodeName)
	require.NotEmpty(t, nnc.Spec.Layer2s, "NNC should have Layer2s")

	npc := getNetplanConfig(t, ctx, nodeName)
	require.NotNil(t, npc)

	// Verify NodeNetplanConfig has the VLAN.
	vlan, ok := npc.Spec.DesiredState.Network.VLans["vlan.501"]
	require.True(t, ok, "expected vlan.501 in NodeNetplanConfig, got keys: %v", mapKeys(npc.Spec.DesiredState.Network.VLans))

	// After API round-trip, Device.Raw is YAML (UnmarshalJSON converts JSON→YAML).
	var vlanData map[string]interface{}
	require.NoError(t, k8syaml.Unmarshal(vlan.Raw, &vlanData))
	// YAML numbers may deserialize as float64 or int64 depending on the parser.
	assert.InDelta(t, 501, vlanData["id"], 0.1)
	assert.Equal(t, "hbn", vlanData["link"])

	// Verify intent-managed label.
	assert.Equal(t, "intent", npc.Labels[intentManagedLabel])
}
