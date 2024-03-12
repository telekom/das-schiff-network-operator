package monitoring

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	mock_monitoring "github.com/telekom/das-schiff-network-operator/pkg/monitoring/mock"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testHostname = "worker"

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
					"namespace": "kube-system"
				},
				"status": {
					"hostIP": "172.18.0.3",
					"podIP": "172.18.0.3",
					"podIPs": [
						{
							"ip": "172.18.0.3"
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
					"namespace": "kube-system"
				},
				"status": {
					"hostIP": "172.18.0.4",
					"podIP": "172.18.0.4",
					"podIPs": [
						{
							"ip": "172.18.0.4"
						}
					]
				}
			}
		]
	}`
	fakePods *corev1.PodList
	tmpPath  string
	mockCtrl *gomock.Controller
)

var _ = BeforeSuite(func() {
	fakePods = &corev1.PodList{}
	err := json.Unmarshal([]byte(fakePodsJSON), fakePods)
	Expect(err).ShouldNot(HaveOccurred())
})

func TestHealthCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	tmpPath = t.TempDir()
	mockCtrl = gomock.NewController(t)
	defer mockCtrl.Finish()
	RunSpecs(t,
		"HealthCheck Suite")
}

var _ = Describe("Endpoint", func() {
	fcm := mock_monitoring.NewMockFRRClient(mockCtrl)
	c := fake.NewClientBuilder().Build()
	e := NewEndpoint(c, fcm)
	e.SetHandlers()

	Context("ShowRoute() should", func() {
		It("return no error", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return error if protocol is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv42", nil)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if input CIDR is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/42", nil)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if longer_prefixes value is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=notABool", nil)
			res := httptest.NewRecorder()
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return no error", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/route?protocol=ipv6&input=192.168.1.1/32&longer_prefixes=true", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowRoute(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
	})

	Context("ShowBGP() should", func() {
		It("return no error if type is not specified (default)", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is summary", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?type=summary", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return error if type is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?type=ivalidType", nil)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if protocol is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv42", nil)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if input CIDR is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv4&input=192.168.1.1/42", nil)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
		It("return error if longer_prefixes value is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/bgp?protocol=ipv4&input=192.168.1.1/32&longer_prefixes=notABool", nil)
			res := httptest.NewRecorder()
			e.ShowBGP(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Context("ShowEVPN() should", func() {
		It("return no error if type is not specified (default)", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is rmac", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=rmac", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is mac", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=mac", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return no error if type is next-hops", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=next-hops", nil)
			res := httptest.NewRecorder()
			fcm.EXPECT().ExecuteWithJSON(gomock.Any()).Return([]byte{})
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusOK))
		})
		It("return error if type is invalid", func() {
			req := httptest.NewRequest(http.MethodGet, "/show/evpn?type=invalidType", nil)
			res := httptest.NewRecorder()
			e.ShowEVPN(res, req)
			Expect(res.Code).To(Equal(http.StatusBadRequest))
		})
	})
	Context("PassRequest() should", func() {
		It("return error if there are no instances to query", func() {
			req := httptest.NewRequest(http.MethodGet, "/all/show/route", nil)
			res := httptest.NewRecorder()
			e.PassRequest(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})
		It("return error if cannot get data from target pod", func() {
			c := fake.NewClientBuilder().WithRuntimeObjects(fakePods).Build()
			e := NewEndpoint(c, fcm)
			req := httptest.NewRequest(http.MethodGet, "/all/show/route", nil)
			res := httptest.NewRecorder()
			e.PassRequest(res, req)
			Expect(res.Code).To(Equal(http.StatusInternalServerError))
		})
	})
})
