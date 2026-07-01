package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

const (
	fluxVersion         = "v2.9.0"
	fluxSystemNamespace = "flux-system"

	fluxFixtureChartName    = "network-sync-fixture"
	fluxFixtureChartVersion = "0.1.0"
	fluxFixtureChartArchive = fluxFixtureChartName + "-" + fluxFixtureChartVersion + ".tgz"
	fluxFixtureRepoName     = "network-sync-fixtures"
	fluxFixtureServerName   = "network-sync-flux-chart-repo"
	fluxFixtureServerImage  = "busybox:1.37.0"
	fluxFixtureHTTPPortName = "http"

	fluxReconcileRequestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"
)

type fluxCluster struct {
	name       string
	client     client.Client
	kubeClient kubernetes.Interface
	apply      func(context.Context, []byte) error
}

//go:embed testdata/flux/install-v2.9.0.yaml
var fluxInstallManifest []byte

var (
	fluxFixtureChartOnce  sync.Once
	fluxFixtureChartBytes []byte
	fluxFixtureIndexYAML  string
	fluxFixtureChartErr   error
)

func managementFluxCluster(f *framework.Framework) fluxCluster {
	return fluxCluster{
		name:       "management",
		client:     f.Client,
		kubeClient: f.KubeClient,
		apply:      f.ApplyManifest,
	}
}

func workloadFluxCluster(f *framework.Framework) fluxCluster {
	return fluxCluster{
		name:       "workload",
		client:     f.Cluster2Client(),
		kubeClient: f.Cluster2KubeClient,
		apply:      f.ApplyManifestToCluster2,
	}
}

func ensureFluxInstalled(ctx context.Context, cluster fluxCluster) error {
	ready, err := fluxControllersReady(ctx, cluster.kubeClient)
	if err != nil {
		return fmt.Errorf("checking existing Flux install on %s cluster: %w", cluster.name, err)
	}
	if ready {
		return nil
	}

	manifest, err := getFluxInstallManifest(ctx)
	if err != nil {
		return err
	}
	if err := cluster.apply(ctx, manifest); err != nil {
		return fmt.Errorf("applying Flux %s to %s cluster: %w", fluxVersion, cluster.name, err)
	}
	for _, name := range []string{"source-controller", "helm-controller"} {
		if err := waitForDeploymentReady(ctx, cluster.kubeClient, fluxSystemNamespace, name, 5*time.Minute); err != nil {
			return fmt.Errorf("waiting for Flux %s on %s cluster: %w", name, cluster.name, err)
		}
	}
	return nil
}

func fluxControllersReady(ctx context.Context, kube kubernetes.Interface) (bool, error) {
	for _, name := range []string{"source-controller", "helm-controller"} {
		deploy, err := kube.AppsV1().Deployments(fluxSystemNamespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if deploy.Spec.Replicas == nil ||
			deploy.Status.AvailableReplicas != *deploy.Spec.Replicas ||
			deploy.Status.UpdatedReplicas != *deploy.Spec.Replicas {
			return false, nil
		}
	}
	return true, nil
}

func getFluxInstallManifest(_ context.Context) ([]byte, error) {
	if len(fluxInstallManifest) == 0 {
		return nil, fmt.Errorf("embedded Flux %s install manifest is empty", fluxVersion)
	}
	return fluxInstallManifest, nil
}

func ensureFluxChartRepository(ctx context.Context, cluster fluxCluster) error {
	chart, indexYAML, err := getFluxFixtureChart()
	if err != nil {
		return err
	}

	if err := ensureNamespace(ctx, cluster.client, fluxSystemNamespace); err != nil {
		return fmt.Errorf("ensuring %s namespace on %s cluster: %w", fluxSystemNamespace, cluster.name, err)
	}

	if err := upsertObject(ctx, cluster.client, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fluxFixtureServerName,
			Namespace: fluxSystemNamespace,
		},
		Data: map[string]string{
			"index.yaml": indexYAML,
		},
		BinaryData: map[string][]byte{
			fluxFixtureChartArchive: chart,
		},
	}); err != nil {
		return fmt.Errorf("creating Flux fixture chart ConfigMap on %s cluster: %w", cluster.name, err)
	}

	labels := map[string]string{"app.kubernetes.io/name": fluxFixtureServerName}
	replicas := int32(1)
	if err := upsertObject(ctx, cluster.client, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fluxFixtureServerName,
			Namespace: fluxSystemNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    fluxFixtureHTTPPortName,
						Image:   fluxFixtureServerImage,
						Command: []string{"httpd", "-f", "-p", "8080", "-h", "/charts"},
						Ports: []corev1.ContainerPort{{
							Name:          fluxFixtureHTTPPortName,
							ContainerPort: 8080,
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "charts",
							MountPath: "/charts",
							ReadOnly:  true,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "charts",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: fluxFixtureServerName},
							},
						},
					}},
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("creating Flux fixture chart Deployment on %s cluster: %w", cluster.name, err)
	}

	if err := upsertObject(ctx, cluster.client, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fluxFixtureServerName,
			Namespace: fluxSystemNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       fluxFixtureHTTPPortName,
				Port:       8080,
				TargetPort: intstr.FromString(fluxFixtureHTTPPortName),
			}},
		},
	}); err != nil {
		return fmt.Errorf("creating Flux fixture chart Service on %s cluster: %w", cluster.name, err)
	}

	return waitForDeploymentReady(ctx, cluster.kubeClient, fluxSystemNamespace, fluxFixtureServerName, 2*time.Minute)
}

func getFluxFixtureChart() ([]byte, string, error) {
	fluxFixtureChartOnce.Do(func() {
		fluxFixtureChartBytes, fluxFixtureChartErr = buildFluxFixtureChart()
		if fluxFixtureChartErr != nil {
			return
		}
		sum := sha256.Sum256(fluxFixtureChartBytes)
		fluxFixtureIndexYAML = fmt.Sprintf(`apiVersion: v1
entries:
  %s:
    - apiVersion: v2
      appVersion: "%s"
      created: "2026-07-01T00:00:00Z"
      description: Network sync Flux ownership e2e fixture
      digest: "%x"
      name: %s
      type: application
      urls:
        - %s
      version: "%s"
generated: "2026-07-01T00:00:00Z"
`, fluxFixtureChartName, fluxFixtureChartVersion, sum, fluxFixtureChartName, fluxFixtureChartArchive, fluxFixtureChartVersion)
	})
	return fluxFixtureChartBytes, fluxFixtureIndexYAML, fluxFixtureChartErr
}

func buildFluxFixtureChart() ([]byte, error) {
	files := map[string]string{
		"Chart.yaml": fmt.Sprintf(`apiVersion: v2
name: %s
description: Network sync Flux ownership e2e fixture
type: application
version: %s
appVersion: %s
`, fluxFixtureChartName, fluxFixtureChartVersion, fluxFixtureChartVersion),
		"values.yaml": `name: vrf-sync-flux-helm-ownership
vrf: ownmeta
vni: 2002040
routeTarget: "65188:2040"
syncManaged: false
revision: initial
`,
		"templates/vrf.yaml": `apiVersion: network-connector.sylvaproject.org/v1alpha1
kind: VRF
metadata:
  name: {{ .Values.name | quote }}
  labels:
    networking.telekom.com/fixture: flux-helm
{{ if .Values.syncManaged }}
    network-sync.telekom.com/managed-by: network-sync
{{ end }}
  annotations:
    networking.telekom.com/source: flux-helm
    networking.telekom.com/fixture-revision: {{ .Values.revision | quote }}
spec:
  vrf: {{ .Values.vrf | quote }}
  vni: {{ .Values.vni }}
  routeTarget: {{ .Values.routeTarget | quote }}
`,
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	modTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		content := []byte(files[name])
		header := &tar.Header{
			Name:    fluxFixtureChartName + "/" + name,
			Mode:    0o644,
			Size:    int64(len(content)),
			ModTime: modTime,
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

//nolint:unparam // Keeping vrfName explicit makes the fixture call sites readable.
func fluxHelmReleaseYAML(releaseName, targetNamespace, vrfName string, vni int, syncManaged bool, revision string) string {
	routeTarget := fmt.Sprintf("65188:%d", vni-2000000)
	return fmt.Sprintf(`apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: %s
  namespace: %s
spec:
  interval: 10s
  url: http://%s.%s.svc.cluster.local:8080
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: %s
  namespace: %s
spec:
  interval: 10s
  releaseName: %s
  targetNamespace: %s
  timeout: 2m
  install:
    createNamespace: true
    remediation:
      retries: 3
  upgrade:
    remediation:
      retries: 3
  chart:
    spec:
      chart: %s
      version: "%s"
      sourceRef:
        kind: HelmRepository
        name: %s
        namespace: %s
      interval: 10s
  values:
    name: %s
    vrf: ownmeta
    vni: %d
    routeTarget: %q
    syncManaged: %t
    revision: %q
`, fluxFixtureRepoName, fluxSystemNamespace, fluxFixtureServerName, fluxSystemNamespace,
		releaseName, fluxSystemNamespace, releaseName, targetNamespace,
		fluxFixtureChartName, fluxFixtureChartVersion, fluxFixtureRepoName, fluxSystemNamespace,
		vrfName, vni, routeTarget, syncManaged, revision)
}

func requestFluxHelmReleaseReconcile(ctx context.Context, c client.Client, name string) (string, error) {
	requestedAt := time.Now().UTC().Format(time.RFC3339Nano)
	key := client.ObjectKey{Namespace: fluxSystemNamespace, Name: name}
	for attempt := 0; attempt < 10; attempt++ {
		obj := fluxHelmReleaseObject(name)
		if err := c.Get(ctx, key, obj); err != nil {
			return "", err
		}
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[fluxReconcileRequestedAtAnnotation] = requestedAt
		obj.SetAnnotations(annotations)
		if err := c.Update(ctx, obj); err != nil {
			if apierrors.IsConflict(err) {
				time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				continue
			}
			return "", err
		}
		return requestedAt, nil
	}
	return "", fmt.Errorf("failed to request Flux HelmRelease reconcile for %s/%s after retries", fluxSystemNamespace, name)
}

func waitForFluxHelmReleaseReconcile(ctx context.Context, c client.Client, name, requestedAt string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	return framework.Poll(waitCtx, 5*time.Second, func() (bool, error) {
		obj := fluxHelmReleaseObject(name)
		if err := c.Get(waitCtx, client.ObjectKey{Namespace: fluxSystemNamespace, Name: name}, obj); err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				return false, nil
			}
			return false, err
		}
		lastHandled, found, err := unstructured.NestedString(obj.Object, "status", "lastHandledReconcileAt")
		if err != nil {
			return false, err
		}
		return found && lastHandled == requestedAt, nil
	})
}

func deleteFluxHelmRelease(ctx context.Context, c client.Client, name string) error {
	obj := fluxHelmReleaseObject(name)
	if err := c.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}
	return waitForObjectDeleted(ctx, c, obj, 2*time.Minute)
}

func fluxHelmReleaseObject(name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	obj.SetName(name)
	obj.SetNamespace(fluxSystemNamespace)
	return obj
}

func cleanupFluxChartRepository(ctx context.Context, cluster fluxCluster) error {
	objects := []client.Object{
		fluxHelmRepositoryObject(),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: fluxFixtureServerName, Namespace: fluxSystemNamespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: fluxFixtureServerName, Namespace: fluxSystemNamespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fluxFixtureServerName, Namespace: fluxSystemNamespace}},
	}
	for _, obj := range objects {
		if err := deleteClientObjectAndWait(ctx, cluster.client, obj, 2*time.Minute); err != nil {
			return fmt.Errorf("cleaning up Flux chart repository on %s cluster: %w", cluster.name, err)
		}
	}
	return nil
}

func fluxHelmRepositoryObject() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "source.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "HelmRepository",
	})
	obj.SetName(fluxFixtureRepoName)
	obj.SetNamespace(fluxSystemNamespace)
	return obj
}

func deleteClientObjectAndWait(ctx context.Context, c client.Client, obj client.Object, timeout time.Duration) error {
	if err := c.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}
	return waitForObjectDeleted(ctx, c, obj, timeout)
}

func waitForObjectDeleted(ctx context.Context, c client.Client, obj client.Object, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	key := client.ObjectKeyFromObject(obj)
	return framework.Poll(waitCtx, 5*time.Second, func() (bool, error) {
		fresh, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return false, fmt.Errorf("deep copy did not return client.Object for %T", obj)
		}
		if err := c.Get(waitCtx, key, fresh); err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
}

func ensureNamespace(ctx context.Context, c client.Client, name string) error {
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func upsertObject(ctx context.Context, c client.Client, desired client.Object) error {
	existing, ok := desired.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("deep copy did not return client.Object for %T", desired)
	}
	key := types.NamespacedName{Namespace: desired.GetNamespace(), Name: desired.GetName()}
	if err := c.Get(ctx, key, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return c.Create(ctx, desired)
		}
		return err
	}

	desired.SetResourceVersion(existing.GetResourceVersion())
	if desiredSvc, ok := desired.(*corev1.Service); ok {
		existingSvc, ok := existing.(*corev1.Service)
		if !ok {
			return fmt.Errorf("existing object for %s/%s is %T, expected *corev1.Service",
				desired.GetNamespace(), desired.GetName(), existing)
		}
		desiredSvc.Spec.ClusterIP = existingSvc.Spec.ClusterIP
		desiredSvc.Spec.ClusterIPs = existingSvc.Spec.ClusterIPs
		desiredSvc.Spec.IPFamilies = existingSvc.Spec.IPFamilies
		desiredSvc.Spec.IPFamilyPolicy = existingSvc.Spec.IPFamilyPolicy
		desiredSvc.Spec.InternalTrafficPolicy = existingSvc.Spec.InternalTrafficPolicy
	}
	return c.Update(ctx, desired)
}

func waitForDeploymentReady(ctx context.Context, kube kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return framework.Poll(waitCtx, 5*time.Second, func() (bool, error) {
		deploy, err := kube.AppsV1().Deployments(namespace).Get(waitCtx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if deploy.Spec.Replicas == nil {
			return false, nil
		}
		return deploy.Status.AvailableReplicas == *deploy.Spec.Replicas &&
			deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas, nil
	})
}
