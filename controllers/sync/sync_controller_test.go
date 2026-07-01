package sync

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(nc.AddToScheme(s))
	return s
}

const (
	testClusterNamespace         = "test-cluster"
	testClusterKubeconfigSecret  = testClusterNamespace + "-kubeconfig"
	testManagementNamespace      = "test-ns"
	testRemoteNamespace          = "default"
	testVRFName                  = "vrf-m2m"
	testVRFValue                 = "m2m"
	testHelmManager              = "Helm"
	testSourceReleaseName        = "source-release"
	testSourceReleaseNamespace   = "t-caas-controllers"
	testRemoteReleaseName        = "remote-release"
	testRemoteReleaseNamespace   = "tenant-system"
	testSharedReleaseName        = "shared-release"
	testSharedReleaseNamespace   = "shared-namespace"
	testNetworkName              = "net-vlan501"
	testOrphanedClusterNamespace = "orphaned-cluster"
	testPendingClusterNamespace  = "pending-cluster"
	testRemoteClientNamespace    = "ns1"
	testRemoteClientName         = "c1"
	testBGPAuthSecretName        = "bgp-auth" // #nosec G101 -- test Secret object name, not a credential value.
	testScopeLabel               = "networking.telekom.com/scope"
	testStorageScopeValue        = "storage"
	testIntentAnnotation         = "networking.telekom.com/intent"
	testSANIntentValue           = "san"
	testForeignVRFValue          = "foreign"
	testStaleMetadataKey         = "networking.telekom.com/stale"
	testStaleMetadataValue       = "remove-me"
	testOwnershipManagedByLabel  = "app.kubernetes.io/managed-by"
	testOwnershipFluxHelmName    = "helm.toolkit.fluxcd.io/name"
	testOwnershipFluxHelmNS      = "helm.toolkit.fluxcd.io/namespace"
	testOwnershipHelmReleaseName = "meta.helm.sh/release-name"
	testOwnershipHelmReleaseNS   = "meta.helm.sh/release-namespace"
	testBGPPasswordKey           = "password"
)

var testOwnershipLabelKeys = []string{
	testOwnershipManagedByLabel,
	testOwnershipFluxHelmName,
	testOwnershipFluxHelmNS,
}

var testOwnershipAnnotationKeys = []string{
	testOwnershipHelmReleaseName,
	testOwnershipHelmReleaseNS,
}

func newFakeSyncController(mgmtObjs, remoteObjs []client.Object) (*Controller, client.Client) {
	s := testScheme()

	mgmtClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(mgmtObjs...).
		WithStatusSubresource(&nc.Inbound{}, &nc.Outbound{}).
		Build()

	remoteClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(remoteObjs...).
		Build()

	remotes := NewRemoteClientManager(s, RemoteClientConfig{})
	remotes.clients[types.NamespacedName{Namespace: testClusterNamespace, Name: testClusterNamespace}] = remoteClient

	return &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}, remoteClient
}

// TestSyncCreatesRemoteObject verifies that a VRF in the mgmt namespace
// gets created on the remote cluster.
func TestSyncCreatesRemoteObject(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, nil)
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check remote cluster has the VRF.
	remoteVRF := &nc.VRF{}
	err = remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace,
		Name:      testVRFName,
	}, remoteVRF)
	if err != nil {
		t.Fatalf("Remote VRF not found: %v", err)
	}

	if remoteVRF.Spec.VRF != testVRFValue {
		t.Errorf("Expected VRF name 'm2m', got %q", remoteVRF.Spec.VRF)
	}
	if remoteVRF.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("Expected managed-by label, got %v", remoteVRF.Labels)
	}
	if remoteVRF.Annotations[annotationSourceNS] != testClusterNamespace {
		t.Errorf("Expected source-namespace annotation, got %v", remoteVRF.Annotations)
	}
}

// TestSyncUpdatesRemoteObject verifies drift correction.
func TestSyncUpdatesRemoteObject(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}

	// Remote has stale data.
	staleRemote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(9999), // Drifted VNI
			RouteTarget: ptrString("65188:2026"),
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{staleRemote})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	remoteVRF := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, remoteVRF); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if remoteVRF.Spec.VNI == nil || *remoteVRF.Spec.VNI != 2002026 {
		t.Errorf("Expected VNI 2002026, got %v (drift not corrected)", remoteVRF.Spec.VNI)
	}
	if remoteVRF.Annotations[annotationSourceNS] != testClusterNamespace {
		t.Errorf("Expected source-namespace annotation, got %v", remoteVRF.Annotations)
	}
}

func TestSyncDoesNotCopySourceOwnershipMetadata(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
			Labels: map[string]string{
				testOwnershipManagedByLabel: testHelmManager,
				testOwnershipFluxHelmName:   testSourceReleaseName,
				testOwnershipFluxHelmNS:     testSourceReleaseNamespace,
				testScopeLabel:              testStorageScopeValue,
			},
			Annotations: map[string]string{
				testOwnershipHelmReleaseName: testSourceReleaseName,
				testOwnershipHelmReleaseNS:   testSourceReleaseNamespace,
				lastAppliedConfigurationAnn:  "{}",
				testIntentAnnotation:         testSANIntentValue,
			},
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, nil)
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	remoteVRF := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, remoteVRF); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}

	assertNoOwnershipMetadata(t, remoteVRF)
	if remoteVRF.Labels[testScopeLabel] != testStorageScopeValue {
		t.Errorf("Expected non-ownership label to be copied, got %v", remoteVRF.Labels)
	}
	if remoteVRF.Annotations[testIntentAnnotation] != testSANIntentValue {
		t.Errorf("Expected non-ownership annotation to be copied, got %v", remoteVRF.Annotations)
	}
	if _, ok := remoteVRF.Annotations[lastAppliedConfigurationAnn]; ok {
		t.Errorf("Expected last-applied annotation to be removed, got %v", remoteVRF.Annotations)
	}
}

func TestSyncPreservesRemoteOwnershipMetadata(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
			Labels: map[string]string{
				testOwnershipManagedByLabel: testHelmManager,
				testOwnershipFluxHelmName:   testSourceReleaseName,
				testOwnershipFluxHelmNS:     testSourceReleaseNamespace,
				testScopeLabel:              testStorageScopeValue,
			},
			Annotations: map[string]string{
				testOwnershipHelmReleaseName: testSourceReleaseName,
				testOwnershipHelmReleaseNS:   testSourceReleaseNamespace,
				testIntentAnnotation:         testSANIntentValue,
			},
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}
	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy:              labelManagedByValue,
				testOwnershipManagedByLabel: testHelmManager,
				testOwnershipFluxHelmName:   testRemoteReleaseName,
				testOwnershipFluxHelmNS:     testRemoteReleaseNamespace,
				testStaleMetadataKey:        testStaleMetadataValue,
			},
			Annotations: map[string]string{
				testOwnershipHelmReleaseName: testRemoteReleaseName,
				testOwnershipHelmReleaseNS:   testRemoteReleaseNamespace,
				testStaleMetadataKey:         testStaleMetadataValue,
			},
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(9999),
			RouteTarget: ptrString("65188:2026"),
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteVRF})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if got.Labels[testOwnershipManagedByLabel] != testHelmManager {
		t.Errorf("Expected remote Helm managed-by label to be preserved, got %v", got.Labels)
	}
	if got.Labels[testOwnershipFluxHelmName] != testRemoteReleaseName {
		t.Errorf("Expected remote Helm label to be preserved, got %v", got.Labels)
	}
	if got.Labels[testOwnershipFluxHelmNS] != testRemoteReleaseNamespace {
		t.Errorf("Expected remote Helm namespace label to be preserved, got %v", got.Labels)
	}
	if got.Annotations[testOwnershipHelmReleaseName] != testRemoteReleaseName {
		t.Errorf("Expected remote Helm annotation to be preserved, got %v", got.Annotations)
	}
	if got.Annotations[testOwnershipHelmReleaseNS] != testRemoteReleaseNamespace {
		t.Errorf("Expected remote Helm namespace annotation to be preserved, got %v", got.Annotations)
	}
	if got.Labels[testScopeLabel] != testStorageScopeValue {
		t.Errorf("Expected desired non-ownership label to be applied, got %v", got.Labels)
	}
	if got.Annotations[testIntentAnnotation] != testSANIntentValue {
		t.Errorf("Expected desired non-ownership annotation to be applied, got %v", got.Annotations)
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("Expected sync ownership label to be retained, got %v", got.Labels)
	}
	if _, ok := got.Labels[testStaleMetadataKey]; ok {
		t.Errorf("Expected non-ownership remote label drift to be removed, got %v", got.Labels)
	}
	if _, ok := got.Annotations[testStaleMetadataKey]; ok {
		t.Errorf("Expected non-ownership remote annotation drift to be removed, got %v", got.Annotations)
	}
	if got.Spec.VNI == nil || *got.Spec.VNI != 2002026 {
		t.Errorf("Expected spec drift to still be corrected, got %v", got.Spec.VNI)
	}
}

func TestSyncPreservesRemoteOwnershipMetadataEvenWhenItMatchesSource(t *testing.T) {
	sourceLabels := map[string]string{
		testOwnershipManagedByLabel: testHelmManager,
		testOwnershipFluxHelmName:   testSharedReleaseName,
		testOwnershipFluxHelmNS:     testSharedReleaseNamespace,
	}
	sourceAnnotations := map[string]string{
		testOwnershipHelmReleaseName: testSharedReleaseName,
		testOwnershipHelmReleaseNS:   testSharedReleaseNamespace,
	}
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testVRFName,
			Namespace:   testClusterNamespace,
			Labels:      sourceLabels,
			Annotations: sourceAnnotations,
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(2002026),
			RouteTarget: ptrString("65188:2026"),
		},
	}
	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testVRFName,
			Namespace:   testRemoteNamespace,
			Labels:      copyStringMap(sourceLabels),
			Annotations: copyStringMap(sourceAnnotations),
		},
		Spec: nc.VRFSpec{
			VRF:         testVRFValue,
			VNI:         ptrInt32(9999),
			RouteTarget: ptrString("65188:2026"),
		},
	}
	remoteVRF.Labels[labelManagedBy] = labelManagedByValue
	remoteVRF.Annotations[annotationSourceNS] = testClusterNamespace

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteVRF})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if got.Labels[testOwnershipManagedByLabel] != testHelmManager {
		t.Errorf("Expected matching remote Helm managed-by label to be preserved, got %v", got.Labels)
	}
	if got.Labels[testOwnershipFluxHelmName] != testSharedReleaseName {
		t.Errorf("Expected matching remote Flux Helm name label to be preserved, got %v", got.Labels)
	}
	if got.Labels[testOwnershipFluxHelmNS] != testSharedReleaseNamespace {
		t.Errorf("Expected matching remote Flux Helm namespace label to be preserved, got %v", got.Labels)
	}
	if got.Annotations[testOwnershipHelmReleaseName] != testSharedReleaseName {
		t.Errorf("Expected matching remote Helm release annotation to be preserved, got %v", got.Annotations)
	}
	if got.Annotations[testOwnershipHelmReleaseNS] != testSharedReleaseNamespace {
		t.Errorf("Expected matching remote Helm namespace annotation to be preserved, got %v", got.Annotations)
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("Expected sync ownership label to remain, got %v", got.Labels)
	}
	if got.Annotations[annotationSourceNS] != testClusterNamespace {
		t.Errorf("Expected source namespace annotation to remain, got %v", got.Annotations)
	}
	if got.Spec.VNI == nil || *got.Spec.VNI != 2002026 {
		t.Errorf("Expected spec drift to still be corrected, got %v", got.Spec.VNI)
	}
}

// TestSyncDeletion verifies that deleting a mgmt object removes it from remote.
func TestSyncDeletion(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testVRFName,
			Namespace:         testClusterNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testVRFName,
			Namespace:   testRemoteNamespace,
			Labels:      map[string]string{labelManagedBy: labelManagedByValue},
			Annotations: map[string]string{annotationSourceNS: testClusterNamespace},
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteVRF})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Remote object should be deleted.
	err = remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, &nc.VRF{})
	if err == nil {
		t.Error("Expected remote VRF to be deleted, but it still exists")
	}

	// Mgmt object should be gone (fake client GCs when last finalizer removed + DeletionTimestamp set).
	mgmtVRF := &nc.VRF{}
	err = sc.Client.Get(ctx, types.NamespacedName{Namespace: testClusterNamespace, Name: testVRFName}, mgmtVRF)
	if err == nil {
		// If it still exists, check that finalizer was removed.
		for _, f := range mgmtVRF.Finalizers {
			if f == finalizerName {
				t.Error("Finalizer should have been removed after remote deletion")
			}
		}
	}
	// err != nil (not found) is the expected case — object was GC'd.
}

func TestSyncDeletionRemovesLegacyManagedRemoteObject(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testVRFName,
			Namespace:         testClusterNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels:    map[string]string{labelManagedBy: labelManagedByValue},
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteVRF})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, &nc.VRF{})
	if err == nil {
		t.Error("Expected legacy managed remote VRF to be deleted, but it still exists")
	}
}

func TestSyncSweepsOrphanedRemoteIntentObject(t *testing.T) {
	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-orphan",
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
			Annotations: map[string]string{
				annotationSourceNS: testClusterNamespace,
			},
		},
		Spec: nc.VRFSpec{
			VRF:         "orphan",
			VNI:         ptrInt32(2002999),
			RouteTarget: ptrString("65188:2999"),
		},
	}

	sc, remoteClient := newFakeSyncController(nil, []client.Object{remoteVRF})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "vrf-orphan"}, &nc.VRF{})
	if err == nil {
		t.Fatal("Expected orphaned remote VRF to be deleted, but it still exists")
	}
}

func TestSyncSweepKeepsOtherSourceNamespace(t *testing.T) {
	remoteVRF := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-other-source",
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
			Annotations: map[string]string{
				annotationSourceNS: "other-cluster",
			},
		},
		Spec: nc.VRFSpec{
			VRF:         "other",
			VNI:         ptrInt32(2002888),
			RouteTarget: ptrString("65188:2888"),
		},
	}

	sc, remoteClient := newFakeSyncController(nil, []client.Object{remoteVRF})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace,
		Name:      "vrf-other-source",
	}, got); err != nil {
		t.Fatalf("Expected remote VRF from other source namespace to remain: %v", err)
	}
}

// TestSyncIPAMPromotion verifies that status.addresses on Inbound gets
// promoted to spec.addresses on the remote object.
func TestSyncIPAMPromotion(t *testing.T) {
	count := int32(2)
	inbound := &nc.Inbound{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ib-test",
			Namespace: testClusterNamespace,
		},
		Spec: nc.InboundSpec{
			NetworkRef:    testNetworkName,
			Count:         &count,
			Advertisement: nc.AdvertisementConfig{Type: "bgp"},
		},
		Status: nc.InboundStatus{
			Addresses: &nc.AddressAllocation{
				IPv4: []string{"10.250.0.2", "10.250.0.3"},
				IPv6: []string{"fd94:685b:30cf:501::2", "fd94:685b:30cf:501::3"},
			},
		},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{inbound}, nil)
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	remoteInbound := &nc.Inbound{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: "ib-test"}, remoteInbound); err != nil {
		t.Fatalf("Remote Inbound not found: %v", err)
	}

	if remoteInbound.Spec.Addresses == nil {
		t.Fatal("Remote Inbound spec.addresses should be promoted from status")
	}
	if len(remoteInbound.Spec.Addresses.IPv4) != 2 {
		t.Errorf("Expected 2 IPv4 addresses, got %d", len(remoteInbound.Spec.Addresses.IPv4))
	}
	if remoteInbound.Spec.Count != nil {
		t.Error("Remote Inbound spec.count should be nil after IPAM promotion")
	}
}

// TestSyncNoRemoteClient verifies requeue when no remote client exists.
func TestSyncNoRemoteClient(t *testing.T) {
	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).Build()
	remotes := NewRemoteClientManager(s, RemoteClientConfig{})

	sc := &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}

	result, err := sc.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "unknown", Name: syncRequestName},
	})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("Expected requeue when no remote client")
	}
}

// TestSyncDrainsFinalizerWhenRemoteGone verifies that when no remote client
// exists for the namespace (workload cluster deleted), an intent CR being
// deleted has our finalizer removed so it can complete deletion. Without this,
// deleting a CAPI Cluster wedges every intent CR in Terminating forever.
func TestSyncDrainsFinalizerWhenRemoteGone(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "vrf-stuck",
			Namespace:         testOrphanedClusterNamespace,
			Finalizers:        []string{finalizerName},
			DeletionTimestamp: &now,
		},
		Spec: nc.VRFSpec{VRF: "stuck", VNI: ptrInt32(2002099), RouteTarget: ptrString("65188:99")},
	}

	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vrf).Build()
	remotes := NewRemoteClientManager(s, RemoteClientConfig{})

	sc := &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}

	if _, err := sc.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testOrphanedClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// VRF should now be gone (fake client GCs once last finalizer is removed).
	got := &nc.VRF{}
	err := mgmtClient.Get(context.Background(), types.NamespacedName{Namespace: testOrphanedClusterNamespace, Name: "vrf-stuck"}, got)
	if err == nil {
		if len(got.Finalizers) != 0 {
			t.Errorf("Expected finalizer to be drained, still present: %v", got.Finalizers)
		}
	}
}

// TestSyncNoRemoteClientLeavesActiveCRsAlone verifies that when no remote
// client exists, intent CRs that are NOT being deleted are left untouched
// (no finalizer added, no error).
func TestSyncNoRemoteClientLeavesActiveCRsAlone(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vrf-alive",
			Namespace: testPendingClusterNamespace,
		},
		Spec: nc.VRFSpec{VRF: "alive", VNI: ptrInt32(2002098), RouteTarget: ptrString("65188:98")},
	}

	s := testScheme()
	mgmtClient := fake.NewClientBuilder().WithScheme(s).WithObjects(vrf).Build()
	remotes := NewRemoteClientManager(s, RemoteClientConfig{})

	sc := &Controller{
		Client:  mgmtClient,
		Scheme:  s,
		Log:     zap.New(zap.UseDevMode(true)),
		Remotes: remotes,
	}

	if _, err := sc.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testPendingClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	got := &nc.VRF{}
	if err := mgmtClient.Get(context.Background(), types.NamespacedName{Namespace: testPendingClusterNamespace, Name: "vrf-alive"}, got); err != nil {
		t.Fatalf("VRF should still exist: %v", err)
	}
	if len(got.Finalizers) != 0 {
		t.Errorf("Expected no finalizer added without remote client, got: %v", got.Finalizers)
	}
}

// TestSyncRefusesUnmanagedObject verifies we don't overwrite objects we don't own.
func TestSyncRefusesUnmanagedObject(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	// Remote object exists WITHOUT our managed-by label.
	unmanagedRemote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, _ := newFakeSyncController([]client.Object{vrf}, []client.Object{unmanagedRemote})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err == nil {
		t.Fatal("Expected error when remote object is not managed by us")
	}
}

func TestSyncRefusesManagedObjectFromOtherSourceNamespace(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	remoteFromOtherSource := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
			Annotations: map[string]string{
				annotationSourceNS: testOrphanedClusterNamespace,
			},
		},
		Spec: nc.VRFSpec{VRF: testForeignVRFValue, VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteFromOtherSource})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err == nil {
		t.Fatal("Expected error when remote object is managed by another source namespace")
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if got.Spec.VRF != testForeignVRFValue {
		t.Errorf("Expected other-source object to be left unchanged, got spec %v", got.Spec)
	}
	if got.Annotations[annotationSourceNS] != testOrphanedClusterNamespace {
		t.Errorf("Expected other source namespace annotation to be preserved, got %v", got.Annotations)
	}
}

func TestSyncRefusesManagedObjectWithEmptySourceNamespace(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	remoteWithEmptySource := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
			Annotations: map[string]string{
				annotationSourceNS: "",
			},
		},
		Spec: nc.VRFSpec{VRF: testForeignVRFValue, VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteWithEmptySource})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err == nil {
		t.Fatal("Expected error when remote object has an explicit empty source namespace")
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if got.Spec.VRF != testForeignVRFValue {
		t.Errorf("Expected empty-source object to be left unchanged, got spec %v", got.Spec)
	}
	if got.Annotations[annotationSourceNS] != "" {
		t.Errorf("Expected empty source namespace annotation to be preserved, got %v", got.Annotations)
	}
}

func TestSyncRefusesHelmManagedObjectWithoutSyncOwnership(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testClusterNamespace,
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	helmManagedRemote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				testOwnershipManagedByLabel: testHelmManager,
				testOwnershipFluxHelmName:   testRemoteReleaseName,
				testOwnershipFluxHelmNS:     testRemoteReleaseNamespace,
			},
			Annotations: map[string]string{
				testOwnershipHelmReleaseName: testRemoteReleaseName,
				testOwnershipHelmReleaseNS:   testRemoteReleaseNamespace,
			},
		},
		Spec: nc.VRFSpec{VRF: testForeignVRFValue, VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{helmManagedRemote})
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err == nil {
		t.Fatal("Expected error when remote object has Helm ownership but no sync ownership")
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Get remote VRF: %v", err)
	}
	if got.Spec.VRF != testForeignVRFValue {
		t.Errorf("Expected unmanaged Helm object to be left unchanged, got spec %v", got.Spec)
	}
	if got.Labels[labelManagedBy] == labelManagedByValue {
		t.Errorf("Expected sync ownership label not to be added to unmanaged object, got %v", got.Labels)
	}
}

func TestSyncDeleteLeavesUnmanagedHelmObjectUntouched(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testVRFName,
			Namespace:         testClusterNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	helmManagedRemote := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				testOwnershipManagedByLabel: testHelmManager,
				testOwnershipFluxHelmName:   testRemoteReleaseName,
				testOwnershipFluxHelmNS:     testRemoteReleaseNamespace,
			},
			Annotations: map[string]string{
				testOwnershipHelmReleaseName: testRemoteReleaseName,
				testOwnershipHelmReleaseNS:   testRemoteReleaseNamespace,
			},
		},
		Spec: nc.VRFSpec{VRF: testForeignVRFValue, VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{helmManagedRemote})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Expected unmanaged Helm object to survive source deletion: %v", err)
	}
	if got.Spec.VRF != testForeignVRFValue {
		t.Errorf("Expected unmanaged Helm object to be left unchanged, got spec %v", got.Spec)
	}
	if got.Labels[labelManagedBy] == labelManagedByValue {
		t.Errorf("Expected sync ownership label not to be added to unmanaged object, got %v", got.Labels)
	}
}

func TestSyncDeleteKeepsRemoteObjectFromOtherSourceNamespace(t *testing.T) {
	now := metav1.Now()
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testVRFName,
			Namespace:         testClusterNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
		Spec: nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}

	remoteFromOtherSource := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testVRFName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy: labelManagedByValue,
			},
			Annotations: map[string]string{
				annotationSourceNS: testOrphanedClusterNamespace,
			},
		},
		Spec: nc.VRFSpec{VRF: testForeignVRFValue, VNI: ptrInt32(1), RouteTarget: ptrString("1:1")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf}, []client.Object{remoteFromOtherSource})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nc.VRF{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, got); err != nil {
		t.Fatalf("Expected remote VRF from other source namespace to survive source deletion: %v", err)
	}
	if got.Annotations[annotationSourceNS] != testOrphanedClusterNamespace {
		t.Errorf("Expected other source namespace annotation to be preserved, got %v", got.Annotations)
	}
}

// TestRemoteClientManager tests basic CRUD on the client manager.
func TestRemoteClientManager(t *testing.T) {
	s := testScheme()
	m := NewRemoteClientManager(s, RemoteClientConfig{})

	if m.Has(types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName}) {
		t.Error("Should not have ns1/c1 initially")
	}
	if m.Get(types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName}) != nil {
		t.Error("Get should return nil for unknown cluster")
	}

	// We can't test UpdateFromKubeconfig without a real cluster,
	// but we can test Has/Get/Remove with direct injection.
	m.clients[types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName}] = fake.NewClientBuilder().WithScheme(s).Build()

	if !m.Has(types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName}) {
		t.Error("Should have ns1/c1 after injection")
	}
	if m.Get(types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName}) == nil {
		t.Error("Get should return client for ns1/c1")
	}

	m.Remove(types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName})
	if m.Has(types.NamespacedName{Namespace: testRemoteClientNamespace, Name: testRemoteClientName}) {
		t.Error("Should not have ns1/c1 after removal")
	}
}

// TestSyncMultipleCRDTypes verifies that multiple CRD types are synced in one reconcile.
func TestSyncMultipleCRDTypes(t *testing.T) {
	vrf := &nc.VRF{
		ObjectMeta: metav1.ObjectMeta{Name: testVRFName, Namespace: testClusterNamespace},
		Spec:       nc.VRFSpec{VRF: testVRFValue, VNI: ptrInt32(2002026), RouteTarget: ptrString("65188:2026")},
	}
	network := &nc.Network{
		ObjectMeta: metav1.ObjectMeta{Name: testNetworkName, Namespace: testClusterNamespace},
		Spec:       nc.NetworkSpec{VLAN: ptrInt32(501)},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{vrf, network}, nil)
	ctx := context.Background()

	_, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Both should exist on remote.
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testVRFName}, &nc.VRF{}); err != nil {
		t.Errorf("Remote VRF not found: %v", err)
	}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: testRemoteNamespace, Name: testNetworkName}, &nc.Network{}); err != nil {
		t.Errorf("Remote Network not found: %v", err)
	}
}

func ptrInt32(v int32) *int32    { return &v }
func ptrString(v string) *string { return &v }

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func assertNoOwnershipMetadata(t *testing.T, obj client.Object) {
	t.Helper()
	for _, key := range testOwnershipLabelKeys {
		if _, ok := obj.GetLabels()[key]; ok {
			t.Errorf("Expected ownership label %q to be stripped, got %v", key, obj.GetLabels())
		}
	}
	for _, key := range testOwnershipAnnotationKeys {
		if _, ok := obj.GetAnnotations()[key]; ok {
			t.Errorf("Expected ownership annotation %q to be stripped, got %v", key, obj.GetAnnotations())
		}
	}
}

// TestSyncBGPSecretsMirrorsReferencedSecret verifies that a Secret referenced
// by a BGPPeering.spec.authSecretRef is copied into the remote namespace,
// stamped with our managed-by label, and contains the same Data.
func TestSyncBGPSecretsMirrorsReferencedSecret(t *testing.T) {
	bp := &nc.BGPPeering{
		ObjectMeta: metav1.ObjectMeta{Name: "lp", Namespace: testClusterNamespace},
		Spec: nc.BGPPeeringSpec{
			Mode:          nc.BGPPeeringModeLoopbackPeer,
			Ref:           nc.BGPPeeringRef{InboundRefs: []string{"x"}},
			AuthSecretRef: &corev1.LocalObjectReference{Name: testBGPAuthSecretName},
		},
	}
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testBGPAuthSecretName, Namespace: testClusterNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"password": []byte("s3cret")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{bp, src}, nil)
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &corev1.Secret{}
	if err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace, Name: testBGPAuthSecretName,
	}, got); err != nil {
		t.Fatalf("Remote Secret not found: %v", err)
	}
	if string(got.Data["password"]) != "s3cret" {
		t.Errorf("Expected password 's3cret', got %q", string(got.Data["password"]))
	}
	if got.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("Expected managed-by label, got %v", got.Labels)
	}
	if got.Annotations[annotationSourceNS] != testClusterNamespace {
		t.Errorf("Expected source-namespace annotation, got %v", got.Annotations)
	}
}

func TestSyncBGPSecretsPreservesRemoteOwnershipMetadataOnUpdate(t *testing.T) {
	bp := &nc.BGPPeering{
		ObjectMeta: metav1.ObjectMeta{Name: "lp", Namespace: testClusterNamespace},
		Spec: nc.BGPPeeringSpec{
			Mode:          nc.BGPPeeringModeLoopbackPeer,
			Ref:           nc.BGPPeeringRef{InboundRefs: []string{"x"}},
			AuthSecretRef: &corev1.LocalObjectReference{Name: testBGPAuthSecretName},
		},
	}
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testBGPAuthSecretName, Namespace: testClusterNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{testBGPPasswordKey: []byte("new-secret")},
	}
	remoteSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBGPAuthSecretName,
			Namespace: testRemoteNamespace,
			Labels: map[string]string{
				labelManagedBy:              labelManagedByValue,
				testOwnershipManagedByLabel: testHelmManager,
				testOwnershipFluxHelmName:   testRemoteReleaseName,
				testOwnershipFluxHelmNS:     testRemoteReleaseNamespace,
				testStaleMetadataKey:        testStaleMetadataValue,
			},
			Annotations: map[string]string{
				annotationSourceNS:           testClusterNamespace,
				testOwnershipHelmReleaseName: testRemoteReleaseName,
				testOwnershipHelmReleaseNS:   testRemoteReleaseNamespace,
				testStaleMetadataKey:         testStaleMetadataValue,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{testBGPPasswordKey: []byte("old-secret")},
	}

	sc, remoteClient := newFakeSyncController([]client.Object{bp, src}, []client.Object{remoteSecret})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &corev1.Secret{}
	if err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace, Name: testBGPAuthSecretName,
	}, got); err != nil {
		t.Fatalf("Remote Secret not found: %v", err)
	}
	if string(got.Data[testBGPPasswordKey]) != "new-secret" {
		t.Errorf("Expected password to be updated, got %q", string(got.Data[testBGPPasswordKey]))
	}
	if got.Labels[testOwnershipManagedByLabel] != testHelmManager {
		t.Errorf("Expected remote Helm managed-by label to be preserved, got %v", got.Labels)
	}
	if got.Labels[testOwnershipFluxHelmName] != testRemoteReleaseName {
		t.Errorf("Expected remote Flux Helm name label to be preserved, got %v", got.Labels)
	}
	if got.Labels[testOwnershipFluxHelmNS] != testRemoteReleaseNamespace {
		t.Errorf("Expected remote Flux Helm namespace label to be preserved, got %v", got.Labels)
	}
	if got.Annotations[testOwnershipHelmReleaseName] != testRemoteReleaseName {
		t.Errorf("Expected remote Helm release annotation to be preserved, got %v", got.Annotations)
	}
	if got.Annotations[testOwnershipHelmReleaseNS] != testRemoteReleaseNamespace {
		t.Errorf("Expected remote Helm release namespace annotation to be preserved, got %v", got.Annotations)
	}
	if _, ok := got.Labels[testStaleMetadataKey]; ok {
		t.Errorf("Expected stale non-ownership label drift to be removed, got %v", got.Labels)
	}
	if _, ok := got.Annotations[testStaleMetadataKey]; ok {
		t.Errorf("Expected stale non-ownership annotation drift to be removed, got %v", got.Annotations)
	}
}

// TestSyncBGPSecretsSweepsOrphan verifies that a previously-synced Secret
// (managed-by label + source-namespace annotation) is removed from the
// remote namespace once no live BGPPeering references it any more.
func TestSyncBGPSecretsSweepsOrphan(t *testing.T) {
	orphan := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stale-auth",
			Namespace: testRemoteNamespace,
			Labels:    map[string]string{labelManagedBy: labelManagedByValue},
			Annotations: map[string]string{
				annotationSourceNS: testClusterNamespace,
			},
		},
		Data: map[string][]byte{"password": []byte("old")},
	}

	sc, remoteClient := newFakeSyncController(nil, []client.Object{orphan})
	ctx := context.Background()

	if _, err := sc.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testClusterNamespace, Name: syncRequestName},
	}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &corev1.Secret{}
	err := remoteClient.Get(ctx, types.NamespacedName{
		Namespace: testRemoteNamespace, Name: "stale-auth",
	}, got)
	if err == nil {
		t.Fatalf("Expected orphan Secret to be deleted, but it still exists")
	}
}

// Ensure corev1 import is used (for scheme registration).
var _ = &corev1.Secret{}
