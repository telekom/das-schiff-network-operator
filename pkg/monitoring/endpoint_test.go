package monitoring

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	monmock "github.com/telekom/das-schiff-network-operator/pkg/monitoring/mock"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testSvcName      = "svcName"
	testSvcNamespace = "svcNamespace"
)

var (
	fakePodsJSON = `{
		"items": [
			{
				"apiVersion": "v1",
				"kind": "Pod",
				"metadata": {
					"labels": {
						"app.kubernetes.io/component": "worker",
						"app.kubernetes.io/name": "network-operator"
					},
					"name": "network-operator-worker-1",
					"namespace": "test-namespace"
				},
				"status": {
					"hostIP": "127.0.0.1",
					"podIP": "127.0.0.1",
					"podIPs": [
						{
							"ip": "127.0.0.1"
						}
					]
				}
			},
			{
				"apiVersion": "v1",
				"kind": "Pod",
				"metadata": {
					"labels": {
						"app.kubernetes.io/component": "worker",
						"app.kubernetes.io/name": "network-operator"
					},
					"name": "network-operator-worker-2",
					"namespace": "test-namespace"
				},
				"status": {
					"hostIP": "127.0.0.1",
					"podIP": "127.0.0.1",
					"podIPs": [
						{
							"ip": "127.0.0.1"
						}
					]
				}
			}
		]
	}`

	fakeServicesJSON = `{
		"items": [
			{
				"apiVersion": "v1",
				"kind": "Service",
				"metadata": {
					"name": "test-service",
					"namespace": "test-namespace",
					"uid": "ca97f774-7b91-47fd-a333-5fa7ee87f940"
				}
				
			},
			{
				"apiVersion": "v1",
				"kind": "Service",
				"metadata": {
					"name": "test-service-no-endpoints",
					"namespace": "test-namespace",
					"uid": "ca97f774-7b91-47fd-a333-5fa7ee87f941"
				},
				"spec": {
					"selector": {
						"app.kubernetes.io/component": "bad-selector",
						"app.kubernetes.io/name": "bad-selector"
					}
				}
			}
		]
	}`

	fakePods     *corev1.PodList
	fakeServices *corev1.ServiceList
	mockCtrl     *gomock.Controller
)

var _ = BeforeSuite(func() {
	fakePods = &corev1.PodList{}
	err := json.Unmarshal([]byte(fakePodsJSON), fakePods)
	Expect(err).ShouldNot(HaveOccurred())
	fakeServices = &corev1.ServiceList{}
	err = json.Unmarshal([]byte(fakeServicesJSON), fakeServices)
	Expect(err).ShouldNot(HaveOccurred())
})

func TestHealthCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	mockCtrl = gomock.NewController(t)
	defer mockCtrl.Finish()
	t.Setenv(StatusSvcNameEnv, testSvcName)
	t.Setenv(StatusSvcNamespaceEnv, testSvcNamespace)
	RunSpecs(t,
		"Endpoint Suite")
}

var _ = Describe("Endpoint", func() {
	fcm := monmock.NewMockFRRClient(mockCtrl)
	c := fake.NewClientBuilder().Build()
	e := NewEndpoint(c, fcm, "test-service", "test-namespace")
	e.CreateMux()

	Context("ShowRoute()", func() {
		It("returns no error", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns error if protocol is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if input CIDR is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if longer_prefixes value is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=notABool", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if VRF is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=true&vrf=invalid$vrf", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns no error", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=true", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns no error and add node name to the response if "+healthcheck.NodenameEnv+" env is set", func() {
			testNodename := "test-nodename"
			err := os.Setenv(healthcheck.NodenameEnv, testNodename)
			Expect(err).ToNot(HaveOccurred())
			defer os.Unsetenv(healthcheck.NodenameEnv)
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=true", http.NoBody)
			resp := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowRoute(resp, req)
			Expect(resp.Code).To(Equal(http.StatusOK))
			data, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			m := map[string]json.RawMessage{}
			err = json.Unmarshal(data, &m)
			Expect(err).ToNot(HaveOccurred())
			_, exists := m[testNodename]
			Expect(exists).To(BeTrue())
		})
	})

	Context("ShowBGP()", func() {
		It("returns no error if type is not specified (default)", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns no error if type is summary", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?type=summary", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns error if type is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?type=ivalidType", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if protocol is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if input CIDR is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv4&input=192.168.1.1/42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if longer_prefixes value is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv4&input=192.168.1.1/32&longer_prefixes=notABool", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if VRF is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?vrf=invalid$VRF", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Context("ShowEVPN()", func() {
		It("returns no error if type is not specified (default)", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns no error if type is rmac", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=rmac", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns no error if type is mac", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=mac", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns no error if type is next-hops", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=next-hops", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("returns error if type is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=invalidType", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if VNI is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=rmac&vni=invalidVNI", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error if VNI value is bigger than 24bit uint", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=rmac&vni=96777215", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("returns error no error if VNI is valid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=rmac&vni=42", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
	})
	Context("PassRequest()", func() {
		It("returns error if there are no instances to query", func() {
			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service-no-endpoints", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, "/all/show/route", http.NoBody)
			res := httptest.NewRecorder()
			e.QueryAll(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})

		It("returns error if cannot get data from the endpoint", func() {
			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, "/all/show/route", http.NoBody)
			res := httptest.NewRecorder()
			e.QueryAll(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})
		It("returns error if request was properly passed to the endpoint but the response is malformed", func() {
			svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "invalidJson")
			}))
			defer svr.Close()

			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, svr.URL, http.NoBody)
			res := httptest.NewRecorder()

			e.QueryAll(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})
		It("returns no error if request was properly passed to the endpoint", func() {
			svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "{}")
			}))
			defer svr.Close()

			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, svr.URL+"?service=test-service&namespace=test-namespace", http.NoBody)
			res := httptest.NewRecorder()

			e.QueryAll(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
	})
	Context("GetStatusServiceConfig()", func() {
		It("returns no error if envs are set", func() {
			name, namespace, err := GetStatusServiceConfig()
			Expect(err).ToNot(HaveOccurred())
			Expect(name).To(Equal(testSvcName))
			Expect(namespace).To(Equal(testSvcNamespace))
		})
	})
})
