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
	RunSpecs(t,
		"HealthCheck Suite")
}

var _ = Describe("Endpoint", func() {
	fcm := monmock.NewMockFRRClient(mockCtrl)
	c := fake.NewClientBuilder().Build()
	e := NewEndpoint(c, fcm, "test-service", "test-namespace")
	e.SetHandlers()

	Context("ShowRoute() should", func() {
		It("return no error", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return error if protocol is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if input CIDR is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if longer_prefixes value is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=notABool", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return no error", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=true", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error and add node name to the response if "+healthcheck.NodenameEnv+" env is set", func() {
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

	Context("ShowBGP() should", func() {
		It("return no error if type is not specified (default)", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is summary", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?type=summary", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return error if type is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?type=ivalidType", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if protocol is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if input CIDR is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv4&input=192.168.1.1/42", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if longer_prefixes value is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv4&input=192.168.1.1/32&longer_prefixes=notABool", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Context("ShowEVPN() should", func() {
		It("return no error if type is not specified (default)", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is rmac", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=rmac", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is mac", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=mac", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is next-hops", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=next-hops", http.NoBody)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{'{', '}'})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return error if type is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=invalidType", http.NoBody)
			res := httptest.NewRecorder()
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
	})
	Context("PassRequest() should", func() {
		It("return error if there are no instances to query", func() {
			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service-no-endpoints", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, "/all/show/route", http.NoBody)
			res := httptest.NewRecorder()
			e.PassRequest(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})

		It("return error if cannot get data from the endpoint", func() {
			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, "/all/show/route", http.NoBody)
			res := httptest.NewRecorder()
			e.PassRequest(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})
		It("return error if request was properly passed to the endpoint but the response is malformed", func() {
			svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, "invalidJson")
			}))
			defer svr.Close()

			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, svr.URL, http.NoBody)
			res := httptest.NewRecorder()

			e.PassRequest(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})
		It("return no error if request was properly passed to the endpoint", func() {
			svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, "{}")
			}))
			defer svr.Close()

			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods, fakeServices).Build()
			e := NewEndpoint(c, fcm, "test-service", "test-namespace")
			req := httptest.NewRequest(http.MethodGet, svr.URL+"?service=test-service&namespace=test-namespace", http.NoBody)
			res := httptest.NewRecorder()

			e.PassRequest(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
	})
})
