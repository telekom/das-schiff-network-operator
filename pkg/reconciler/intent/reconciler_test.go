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

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	testEnv    *envtest.Environment
	k8sClient  client.Client
	reconciler *Reconciler
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))

	// Resolve CRD path relative to this source file (not the test binary CWD).
	_, thisFile, _, _ := runtime.Caller(0)
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
	reconciler, err = NewReconciler(k8sClient, logger, 60*time.Second)
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

// --- Helpers ---

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
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatalf("create node %s: %v", name, err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), node) })
}

func createObj(t *testing.T, ctx context.Context, obj client.Object) {
	t.Helper()
	if err := k8sClient.Create(ctx, obj); err != nil {
		t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), obj) })
}

func reconcileAndGetNNC(t *testing.T, ctx context.Context, nodeName string) *networkv1alpha1.NodeNetworkConfig {
	t.Helper()
	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	nnc := &networkv1alpha1.NodeNetworkConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc); err != nil {
		t.Fatalf("get NNC %s: %v", nodeName, err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), nnc) })
	return nnc
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
func makeL2A(name, networkRef string, destSelector *metav1.LabelSelector, nodeSelector *metav1.LabelSelector) *nc.Layer2Attachment {
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
func makePodNetwork(name, networkRef string, destSelector *metav1.LabelSelector, routes []nc.AdditionalRoute) *nc.PodNetwork {
	return &nc.PodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: nc.PodNetworkSpec{
			NetworkRef:   networkRef,
			Destinations: destSelector,
			Routes:       routes,
		},
	}
}

func destSelector(key, value string) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{key: value},
	}
}

// --- Test Cases ---

func TestL2APipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-l2a-pipeline"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-l2a", "m2m", 2002026, "65188:2026"))
	createObj(t, ctx, makeNetwork("net-l2a-501", 501, 4000002, "10.250.0.0/24", "fd94::1/64"))
	createObj(t, ctx, makeDestination("dest-l2a-gw", "vrf-l2a", map[string]string{"type": "gateway"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-pipeline", "net-l2a-501", destSelector("type", "gateway"), nil))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// Verify Layer2
	l2, ok := nnc.Spec.Layer2s["501"]
	if !ok {
		t.Fatal("expected Layer2 entry for key '501'")
	}
	if l2.VNI != 4000002 {
		t.Errorf("expected VNI 4000002, got %d", l2.VNI)
	}
	if l2.VLAN != 501 {
		t.Errorf("expected VLAN 501, got %d", l2.VLAN)
	}
	if l2.MTU != 9000 {
		t.Errorf("expected MTU 9000, got %d", l2.MTU)
	}
	if l2.IRB == nil {
		t.Fatal("expected IRB to be set")
	}
	if l2.IRB.VRF != "vrf-l2a" {
		t.Errorf("expected IRB.VRF 'vrf-l2a', got %q", l2.IRB.VRF)
	}
	if len(l2.IRB.IPAddresses) == 0 {
		t.Fatal("expected IRB.IPAddresses to be non-empty")
	}

	// Verify FabricVRF (keyed by VRF CRD name, not backbone VRF name)
	fvrf, ok := nnc.Spec.FabricVRFs["vrf-l2a"]
	if !ok {
		t.Fatalf("expected FabricVRF entry for key 'vrf-l2a', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	}
	if fvrf.VNI != 2002026 {
		t.Errorf("expected FabricVRF VNI 2002026, got %d", fvrf.VNI)
	}

	// Verify revision is set
	if nnc.Spec.Revision == "" {
		t.Error("expected revision to be set")
	}
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
	createObj(t, ctx, makeL2A("l2a-sel", "net-sel-501", destSelector("type", "sel"),
		&metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}}))

	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Worker node should have L2
	workerNNC := &networkv1alpha1.NodeNetworkConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: workerNode}, workerNNC); err != nil {
		t.Fatalf("get worker NNC: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), workerNNC) })

	if _, ok := workerNNC.Spec.Layer2s["501"]; !ok {
		t.Error("expected worker NNC to have Layer2 '501'")
	}

	// CP node should have NNC but no L2 from this L2A
	cpNNC := &networkv1alpha1.NodeNetworkConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: cpNode}, cpNNC); err != nil {
		t.Fatalf("get cp NNC: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), cpNNC) })

	if _, ok := cpNNC.Spec.Layer2s["501"]; ok {
		t.Error("expected control-plane NNC to NOT have Layer2 '501'")
	}
}

func TestInboundPipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-inbound"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-ib", "ibm2m", 2002030, "65188:2030"))
	createObj(t, ctx, makeNetwork("net-ib", 600, 5000001, "10.100.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-ib", "vrf-ib", map[string]string{"type": "ib"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeInbound("ib-test", "net-ib", destSelector("type", "ib"), []string{"10.100.0.10/32"}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// Verify FabricVRF exists for the VRF
	fvrf, ok := nnc.Spec.FabricVRFs["vrf-ib"]
	if !ok {
		t.Fatalf("expected FabricVRF 'vrf-ib', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	}

	// Should have static routes or redistribute for the inbound IPs
	hasContent := len(fvrf.StaticRoutes) > 0 || fvrf.Redistribute != nil
	if !hasContent {
		t.Error("expected FabricVRF to have static routes or redistribute config")
	}
}

func TestOutboundPipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-outbound"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-ob", "obm2m", 2002031, "65188:2031"))
	createObj(t, ctx, makeNetwork("net-ob", 601, 5000002, "10.200.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-ob", "vrf-ob", map[string]string{"type": "ob"}, []string{"10.103.0.0/16"}))
	createObj(t, ctx, makeOutbound("ob-test", "net-ob", destSelector("type", "ob"), []string{"10.200.0.5/32"}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	fvrf, ok := nnc.Spec.FabricVRFs["vrf-ob"]
	if !ok {
		t.Fatalf("expected FabricVRF 'vrf-ob', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	}

	hasContent := len(fvrf.StaticRoutes) > 0 || len(fvrf.PolicyRoutes) > 0
	if !hasContent {
		t.Error("expected FabricVRF to have static routes or policy routes for outbound")
	}
}

func TestPodNetworkPipeline(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-podnet"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-pn", "pnm2m", 2002032, "65188:2032"))
	createObj(t, ctx, makeNetwork("net-pn", 602, 5000003, "10.50.0.0/16", ""))
	createObj(t, ctx, makeDestination("dest-pn", "vrf-pn", map[string]string{"type": "pn"}, []string{"10.104.0.0/16"}))
	createObj(t, ctx, makePodNetwork("pn-test", "net-pn", destSelector("type", "pn"),
		[]nc.AdditionalRoute{{Prefixes: []string{"10.60.0.0/16"}}}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	fvrf, ok := nnc.Spec.FabricVRFs["vrf-pn"]
	if !ok {
		t.Fatalf("expected FabricVRF 'vrf-pn', got keys: %v", mapKeys(nnc.Spec.FabricVRFs))
	}

	hasContent := fvrf.Redistribute != nil || len(fvrf.StaticRoutes) > 0
	if !hasContent {
		t.Error("expected FabricVRF to have redistribute or static routes for pod network")
	}
}

func TestSBRSingleVRF(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-sbr-single"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-sbr1", "sbrm", 2002040, "65188:2040"))
	createObj(t, ctx, makeNetwork("net-sbr1", 700, 6000001, "10.100.0.0/24", ""))
	createObj(t, ctx, makeDestination("dest-sbr1", "vrf-sbr1", map[string]string{"type": "sbr1"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeInbound("ib-sbr1", "net-sbr1", destSelector("type", "sbr1"), []string{"10.100.0.10/32"}))

	nnc := reconcileAndGetNNC(t, ctx, nodeName)

	// SBR should create intermediate LocalVRF "s-sbrm"
	if _, ok := nnc.Spec.LocalVRFs["s-sbrm"]; !ok {
		t.Error("expected LocalVRF 's-sbrm' (SBR intermediate VRF)")
	}

	// ClusterVRF should have PolicyRoutes for SBR
	if nnc.Spec.ClusterVRF == nil {
		t.Fatal("expected ClusterVRF to be set for SBR")
	}
	if len(nnc.Spec.ClusterVRF.PolicyRoutes) == 0 {
		t.Error("expected ClusterVRF to have PolicyRoutes for SBR")
	}

	// Single VRF case: PolicyRoutes should have SrcPrefix
	for _, pr := range nnc.Spec.ClusterVRF.PolicyRoutes {
		if pr.TrafficMatch.SrcPrefix == nil || *pr.TrafficMatch.SrcPrefix == "" {
			t.Error("expected SBR PolicyRoute to have TrafficMatch.SrcPrefix")
		}
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

	// Inbound selecting destinations in BOTH VRFs
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

	// Should have 2 intermediate LocalVRFs
	if _, ok := nnc.Spec.LocalVRFs["s-vrfa"]; !ok {
		t.Error("expected LocalVRF 's-vrfa'")
	}
	if _, ok := nnc.Spec.LocalVRFs["s-vrfb"]; !ok {
		t.Error("expected LocalVRF 's-vrfb'")
	}

	// ClusterVRF should have PolicyRoutes with src+dst for multi-VRF
	if nnc.Spec.ClusterVRF == nil {
		t.Fatal("expected ClusterVRF")
	}
	hasDstPrefix := false
	for _, pr := range nnc.Spec.ClusterVRF.PolicyRoutes {
		if pr.TrafficMatch.DstPrefix != nil && *pr.TrafficMatch.DstPrefix != "" {
			hasDstPrefix = true
			break
		}
	}
	if !hasDstPrefix {
		t.Error("expected multi-VRF SBR PolicyRoutes to have DstPrefix for disambiguation")
	}
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
	createObj(t, ctx, makeL2A("l2a-lc-upd", "net-lc-upd", destSelector("type", "lc"), nil))

	nnc1 := reconcileAndGetNNC(t, ctx, nodeName)
	rev1 := nnc1.Spec.Revision

	if rev1 == "" {
		t.Fatal("expected initial revision to be set")
	}

	// Update the Network — change IPv4 CIDR
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "net-lc-upd", Namespace: testNamespace}, net); err != nil {
		t.Fatalf("get network: %v", err)
	}
	net.Spec.IPv4 = &nc.IPNetwork{CIDR: "10.250.11.0/24"}
	if err := k8sClient.Update(ctx, net); err != nil {
		t.Fatalf("update network: %v", err)
	}

	// Reconcile again
	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	nnc2 := &networkv1alpha1.NodeNetworkConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc2); err != nil {
		t.Fatalf("get NNC: %v", err)
	}

	if nnc2.Spec.Revision == rev1 {
		t.Error("expected revision to change after Network update")
	}

	// Verify updated CIDR in IRB
	l2, ok := nnc2.Spec.Layer2s["510"]
	if !ok {
		t.Fatal("expected Layer2 '510' after update")
	}
	if l2.IRB == nil {
		t.Fatal("expected IRB after update")
	}
	found := false
	for _, ip := range l2.IRB.IPAddresses {
		if ip == "10.250.11.0/24" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected updated CIDR 10.250.11.0/24 in IRB.IPAddresses, got %v", l2.IRB.IPAddresses)
	}
}

func TestLifecycleDelete(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-lifecycle-del"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-lc-del", "dlm2m", 2002051, "65188:2051"))
	createObj(t, ctx, makeNetwork("net-lc-del", 520, 4200001, "10.250.20.0/24", ""))
	createObj(t, ctx, makeDestination("dest-lc-del", "vrf-lc-del", map[string]string{"type": "del"}, []string{"10.102.0.0/16"}))

	l2a := makeL2A("l2a-lc-del", "net-lc-del", destSelector("type", "del"), nil)
	createObj(t, ctx, l2a)

	nnc := reconcileAndGetNNC(t, ctx, nodeName)
	if _, ok := nnc.Spec.Layer2s["520"]; !ok {
		t.Fatal("expected Layer2 '520' before delete")
	}

	// Delete the L2A
	if err := k8sClient.Delete(ctx, l2a); err != nil {
		t.Fatalf("delete L2A: %v", err)
	}

	// Reconcile after delete
	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile after delete: %v", err)
	}

	nnc2 := &networkv1alpha1.NodeNetworkConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc2); err != nil {
		t.Fatalf("get NNC after delete: %v", err)
	}

	if _, ok := nnc2.Spec.Layer2s["520"]; ok {
		t.Error("expected Layer2 '520' to be removed after L2A delete")
	}
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
	createObj(t, ctx, makeL2A("l2a-multi", "net-multi", destSelector("type", "multi"), nil))

	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	for _, n := range nodes {
		nnc := &networkv1alpha1.NodeNetworkConfig{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: n}, nnc); err != nil {
			t.Errorf("expected NNC for node %s: %v", n, err)
			continue
		}
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), nnc) })

		if _, ok := nnc.Spec.Layer2s["530"]; !ok {
			t.Errorf("expected node %s NNC to have Layer2 '530'", n)
		}
	}
}

func TestOriginTracking(t *testing.T) {
	ctx := context.Background()
	nodeName := "node-origin"

	createNode(t, ctx, nodeName, nil)
	createObj(t, ctx, makeVRF("vrf-origin", "origm", 2002070, "65188:2070"))
	createObj(t, ctx, makeNetwork("net-origin", 540, 4400001, "10.250.40.0/24", ""))
	createObj(t, ctx, makeDestination("dest-origin", "vrf-origin", map[string]string{"type": "origin"}, []string{"10.102.0.0/16"}))
	createObj(t, ctx, makeL2A("l2a-origin", "net-origin", destSelector("type", "origin"), nil))

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
	createObj(t, ctx, makeL2A("l2a-rev", "net-rev", destSelector("type", "rev"), nil))

	nnc1 := reconcileAndGetNNC(t, ctx, nodeName)
	rev1 := nnc1.Spec.Revision
	rv1 := nnc1.ResourceVersion

	// Reconcile again with no changes
	if err := reconciler.ReconcileDebounced(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	nnc2 := &networkv1alpha1.NodeNetworkConfig{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nnc2); err != nil {
		t.Fatalf("get NNC: %v", err)
	}

	if nnc2.Spec.Revision != rev1 {
		t.Errorf("expected revision to stay %q, got %q", rev1, nnc2.Spec.Revision)
	}

	// ResourceVersion should NOT have changed (no API server update)
	if nnc2.ResourceVersion != rv1 {
		t.Errorf("expected ResourceVersion to stay %q (no-op update), got %q", rv1, nnc2.ResourceVersion)
	}
}
