package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	mock_frr "github.com/telekom/das-schiff-network-operator/pkg/frr/mock"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	fakeNCRJSON = `{
		"apiVersion": "v1",
		"items": [
			{
				"apiVersion": "network.schiff.telekom.de/v1alpha1",
				"kind": "NetworkConfigRevision",
				"metadata": {
					"creationTimestamp": "2024-07-11T15:16:00Z",
					"generation": 1,
					"name": "19dad916c7",
					"resourceVersion": "91836",
					"uid": "797e11da-1d60-4263-b2ad-fe0a73d761b7"
				},
				"spec": {
					"config": {
						"layer2": [
							{
								"id": 1,
								"mtu": 1500,
								"nodeSelector": {
									"matchLabels": {
										"worker": "true"
									}
								},
								"vni": 1
							}
						],
						"revision": "",
						"routingTable": [],
						"vrf": []
					},
					"revision": "19dad916c701bc0aeebd14f66bae591f402cabd31cd9b150b87bca710abe3b33"
				},
				"status": {
					"isInvalid": false
				}
			}
		],
		"kind": "List",
		"metadata": {
			"resourceVersion": ""
		}
	}`

	fakeNodesJSON = `{"items":[
		{
			"apiVersion": "v1",
			"kind": "Node",
			"metadata": {
				"name": "kind-worker"
			},
			"status": {
				"conditions": [
					{
						"status": "True",
						"type": "Ready"
					}
				]
			}
		}
	]}`

	fakeNNCJSON = `
	{
		"apiVersion": "v1",
		"items": [
			{
				"apiVersion": "network.schiff.telekom.de/v1alpha1",
				"kind": "NodeNetworkConfig",
				"metadata": {
					"creationTimestamp": "2024-07-11T15:14:32Z",
					"generation": 4,
					"name": "test-node",
					"ownerReferences": [
						{
							"apiVersion": "v1",
							"kind": "Node",
							"name": "test-node",
							"uid": "a616532b-e188-41d7-a0f3-6f17cdfa50b8"
						}
					],
					"resourceVersion": "97276",
					"uid": "b80f17a1-d68e-4e6d-b0cb-e2fdc97b0363"
				},
				"spec": {
					"layer2": [
						{
							"id": 1,
							"mtu": 1500,
							"vni": 1
						}
					],
					"revision": "19dad916c701bc0aeebd14f66bae591f402cabd31cd9b150b87bca710abe3b33",
					"routingTable": [],
					"vrf": []
				},
				"status": {
					"configStatus": "provisioned"
				}
			}
		],
		"kind": "List",
		"metadata": {
			"resourceVersion": ""
		}
	}
`

	mockctrl   *gomock.Controller
	tmpDir     string
	testConfig string
)

const (
	operatorConfigEnv = "OPERATOR_CONFIG"
	dummy             = "dummy"
)

var _ = BeforeSuite(func() {
	var err error
	tmpDir, err = os.MkdirTemp(".", "testdata")
	Expect(err).ToNot(HaveOccurred())
	testConfig = tmpDir + "/config.yaml"

	config := config.Config{
		SkipVRFConfig: []string{dummy},
	}

	configData, err := yaml.Marshal(config)
	Expect(err).ToNot(HaveOccurred())

	err = os.WriteFile(testConfig, configData, 0o600)
	Expect(err).ToNot(HaveOccurred())
	err = os.Setenv(operatorConfigEnv, testConfig)
	Expect(err).ToNot(HaveOccurred())
	err = os.Setenv(healthcheck.NodenameEnv, "test-node")
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	err := os.RemoveAll(tmpDir)
	Expect(err).ToNot(HaveOccurred())
	err = os.Unsetenv(operatorConfigEnv)
	Expect(err).ToNot(HaveOccurred())
	err = os.Unsetenv(healthcheck.NodenameEnv)
	Expect(err).ToNot(HaveOccurred())
})

func TestReconciler(t *testing.T) {
	RegisterFailHandler(Fail)
	mockctrl = gomock.NewController(t)
	defer mockctrl.Finish()
	RunSpecs(t,
		"Reconciler Suite")
}

var _ = Describe("ConfigReconciler", func() {
	Context("NewConfigReconciler() should", func() {
		It("return new config reconciler", func() {
			c := createFullClient()
			r, err := NewConfigReconciler(c, logr.New(nil), time.Millisecond*100)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("ReconcileDebounced() should", func() {
		It("return no error", func() {
			c := createFullClient()
			r, err := NewConfigReconciler(c, logr.New(nil), time.Millisecond*100)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
			err = r.ReconcileDebounced(context.TODO())
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

var _ = Describe("NodeConfigReconciler", func() {
	Context("NewNodeConfigReconciler() should", func() {
		It("return new node config reconciler", func() {
			c := createClient()
			r, err := NewNodeConfigReconciler(c, logr.New(nil), time.Millisecond*100, runtime.NewScheme(), 1)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("reconcileDebaunced() should", func() {
		It("return no error if there is nothing to deploy", func() {
			c := createClient()
			r, err := NewNodeConfigReconciler(c, logr.New(nil), time.Millisecond*100, runtime.NewScheme(), 1)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
			err = r.reconcileDebounced(context.TODO())
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error if cannot set revision isInvalid status to false", func() {
			fakeNCR := &v1alpha1.NetworkConfigRevisionList{}
			err := json.Unmarshal([]byte(fakeNCRJSON), fakeNCR)
			Expect(err).ShouldNot(HaveOccurred())
			c := createClient(fakeNCR)
			r, err := NewNodeConfigReconciler(c, logr.New(nil), time.Millisecond*100, runtime.NewScheme(), 1)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
			err = r.reconcileDebounced(context.TODO())
			Expect(err).To(HaveOccurred())
		})
		It("no error if NodeConfigRevision deployed successfully", func() {
			c := createFullClient()
			r, err := NewNodeConfigReconciler(c, logr.New(nil), time.Millisecond*100, runtime.NewScheme(), 1)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
			err = r.reconcileDebounced(context.TODO())
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error on context timeout", func() {
			fakeNCR := &v1alpha1.NetworkConfigRevisionList{}
			err := json.Unmarshal([]byte(fakeNCRJSON), fakeNCR)
			Expect(err).ShouldNot(HaveOccurred())
			fakeNodes := &corev1.NodeList{}
			err = json.Unmarshal([]byte(fakeNodesJSON), fakeNodes)
			Expect(err).ToNot(HaveOccurred())
			fakeNNC := &v1alpha1.NodeNetworkConfigList{}
			err = json.Unmarshal([]byte(fakeNNCJSON), fakeNNC)
			Expect(err).ShouldNot(HaveOccurred())

			c := createClientWithStatus(&fakeNCR.Items[0], &fakeNNC.Items[0], fakeNCR, fakeNNC, fakeNodes)
			r, err := NewNodeConfigReconciler(c, logr.New(nil), time.Millisecond*100, runtime.NewScheme(), 1)
			Expect(r).ToNot(BeNil())
			Expect(err).ToNot(HaveOccurred())
			err = r.reconcileDebounced(context.TODO())
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("NodeNetworkConfigReconciler", func() {
	Context("NewNodeNetworkConfigReconciler() should", func() {
		It("return error if cannot init FRR Manager", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			c := createClient()
			frrManagerMock.EXPECT().Init(gomock.Any()).Return(fmt.Errorf("init error"))
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(mock_nl.NewMockToolkitInterface(mockctrl)))
			Expect(err).To(HaveOccurred())
			Expect(r).To(BeNil())
		})
		It("create new reconciler", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			c := createClient()
			frrManagerMock.EXPECT().Init(gomock.Any()).Return(nil)
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(mock_nl.NewMockToolkitInterface(mockctrl)))
			Expect(err).ToNot(HaveOccurred())
			Expect(r).ToNot(BeNil())
		})
	})
	Context("Reconcile() should", func() {
		It("return no error if there is no config to reconcile", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			c := createClient()
			frrManagerMock.EXPECT().Init(gomock.Any()).Return(nil)
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(mock_nl.NewMockToolkitInterface(mockctrl)))
			Expect(err).ToNot(HaveOccurred())
			Expect(r).ToNot(BeNil())
			err = r.Reconcile(context.TODO())
			Expect(err).ToNot(HaveOccurred())
		})
		It("return no error if there is no config to reconcile", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			c := createClient()
			frrManagerMock.EXPECT().Init(gomock.Any()).Return(nil)
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(mock_nl.NewMockToolkitInterface(mockctrl)))
			Expect(err).ToNot(HaveOccurred())
			Expect(r).ToNot(BeNil())
			err = r.Reconcile(context.TODO())
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error if cannot configure FRR", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
			netlinkMock.EXPECT().LinkList().Return([]netlink.Link{}, nil)

			c := createFullClient()

			frrManagerMock.EXPECT().Init(gomock.Any()).Return(nil)
			frrManagerMock.EXPECT().Configure(gomock.Any(),
				gomock.Any()).Return(false, fmt.Errorf("configuration error"))
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(netlinkMock))
			Expect(err).ToNot(HaveOccurred())
			Expect(r).ToNot(BeNil())
			err = r.Reconcile(context.TODO())
			Expect(err).To(HaveOccurred())
		})
		It("return error if failed to reload FRR", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
			netlinkMock.EXPECT().LinkList().Return([]netlink.Link{}, nil)

			c := createFullClient()
			frrManagerMock.EXPECT().Init(gomock.Any()).Return(nil)
			frrManagerMock.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(true, nil)
			frrManagerMock.EXPECT().ReloadFRR().Return(fmt.Errorf("error reloading FRR"))
			frrManagerMock.EXPECT().RestartFRR().Return(fmt.Errorf("error restarting FRR"))
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(netlinkMock))
			Expect(err).ToNot(HaveOccurred())
			Expect(r).ToNot(BeNil())
			err = r.Reconcile(context.TODO())
			Expect(err).To(HaveOccurred())
		})
		It("return error if cannot configure networking", func() {
			frrManagerMock := mock_frr.NewMockManagerInterface(mockctrl)
			netlinkMock := mock_nl.NewMockToolkitInterface(mockctrl)
			netlinkMock.EXPECT().LinkList().Return([]netlink.Link{}, nil).Times(3)
			netlinkMock.EXPECT().LinkAdd(gomock.Any()).Return(fmt.Errorf("link add error"))

			c := createFullClient()
			frrManagerMock.EXPECT().Init(gomock.Any()).Return(nil)
			frrManagerMock.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(true, nil)
			frrManagerMock.EXPECT().ReloadFRR().Return(nil)
			r, err := NewNodeNetworkConfigReconciler(c, nil, logr.New(nil), "",
				frrManagerMock, nl.NewManager(netlinkMock))
			Expect(err).ToNot(HaveOccurred())
			Expect(r).ToNot(BeNil())
			err = r.Reconcile(context.TODO())
			Expect(err).To(HaveOccurred())
		})
	})
})

func createClient(initObjs ...runtime.Object) client.Client {
	cb := clientBuilder(initObjs...)
	return cb.Build()
}

func createClientWithStatus(ncr, nnc client.Object, initObjs ...runtime.Object) client.Client {
	return clientBuilder(initObjs...).WithStatusSubresource(nnc, ncr).Build()
}

func clientBuilder(initObjs ...runtime.Object) *fake.ClientBuilder {
	s := runtime.NewScheme()
	err := corev1.AddToScheme(s)
	Expect(err).ToNot(HaveOccurred())
	err = v1alpha1.AddToScheme(s)
	Expect(err).ToNot(HaveOccurred())
	return fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(initObjs...)
}

func createFullClient() client.Client {
	fakeNNC := &v1alpha1.NodeNetworkConfigList{}
	err := json.Unmarshal([]byte(fakeNNCJSON), fakeNNC)
	Expect(err).ShouldNot(HaveOccurred())

	fakeNCR := &v1alpha1.NetworkConfigRevisionList{}
	err = json.Unmarshal([]byte(fakeNCRJSON), fakeNCR)
	Expect(err).ShouldNot(HaveOccurred())
	c := clientBuilder(fakeNNC, fakeNCR).WithStatusSubresource(&fakeNNC.Items[0], &fakeNCR.Items[0]).Build()

	return c
}
