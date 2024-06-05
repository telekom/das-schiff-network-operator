package nodeconfig

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	testConfigName = "testConfig"

	fakeProcessState = `
	{
		"apiVersion": "v1",
		"items": [
			{
				"apiVersion": "network.schiff.telekom.de/v1alpha1",
				"kind": "NodeConfigProcess",
				"metadata": {
					"creationTimestamp": "2024-04-15T11:19:06Z",
					"generation": 12,
					"name": "network-operator",
					"resourceVersion": "223252",
					"uid": "4ad359bb-bb7d-4a1d-bf43-551c04b592d5"
				},
				"spec": {
					"state": "provisioning"
				}
			}
		],
		"kind": "List",
		"metadata": {
			"resourceVersion": ""
		}
	}`

	emptyNodeConfig = `
	{
		"apiVersion": "v1",
		"items": [
			{
				"apiVersion": "network.schiff.telekom.de/v1alpha1",
				"kind": "NodeConfig",
				"metadata": {
					"creationTimestamp": "2024-04-15T11:22:08Z",
					"generation": 2,
					"name": "testConfig",
					"resourceVersion": "222987",
					"uid": "fc0376a2-7f6a-4388-8166-298b21cf2f89"
				},
				"spec": {
					"layer2": [],
					"routingTable": [],
					"vrf": []
				},
				"status": {
					"configStatus": "provisioned"
				}
			},
			{
				"apiVersion": "network.schiff.telekom.de/v1alpha1",
				"kind": "NodeConfig",
				"metadata": {
					"creationTimestamp": "2024-04-15T11:22:08Z",
					"generation": 3,
					"name": "testConfig-backup",
					"resourceVersion": "223106",
					"uid": "5b0ed728-47ed-46cb-a678-8e32dda826ee"
				},
				"spec": {
					"layer2": [],
					"routingTable": [],
					"vrf": []
				}
			}
		],
		"kind": "List",
		"metadata": {
			"resourceVersion": ""
		}
	}`

	fakeProcess    *v1alpha1.NodeConfigProcessList
	fakeNodeConfig *v1alpha1.NodeConfigList
)

var _ = BeforeSuite(func() {
	fakeProcess = &v1alpha1.NodeConfigProcessList{}
	err := json.Unmarshal([]byte(fakeProcessState), fakeProcess)
	Expect(err).ShouldNot(HaveOccurred())
	fakeNodeConfig = &v1alpha1.NodeConfigList{}
	err = json.Unmarshal([]byte(emptyNodeConfig), fakeNodeConfig)
	Expect(err).ShouldNot(HaveOccurred())
})

func TestConfigMap(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t,
		"NodeConfig Suite")
}

var _ = Describe("NodeConfig", func() {
	Context("New() should", func() {
		It("create new NodeConfig with given data", func() {
			current := &v1alpha1.NodeConfig{Status: v1alpha1.NodeConfigStatus{ConfigStatus: StatusProvisioned}}
			backup := &v1alpha1.NodeConfig{}
			invalid := &v1alpha1.NodeConfig{}
			config := New(testConfigName, current, backup, invalid)
			Expect(config).ToNot(BeNil())
			Expect(config.current).To(Equal(current))
			Expect(config.backup).To(Equal(backup))
			Expect(config.GetInvalid()).To(Equal(invalid))
			Expect(config.GetName()).To(Equal(testConfigName))
			Expect(config.GetCurrentConfigStatus()).To(Equal(StatusProvisioned))
		})
	})
	Context("SetCancel()/GetCancelFunc() should", func() {
		It("set and return cancel function", func() {
			config := NewEmpty(testConfigName)
			_, cancel := context.WithCancel(context.Background())
			config.SetCancelFunc(&cancel)
			setCancel := config.GetCancelFunc()
			Expect(setCancel).To(Equal(&cancel))
		})
	})
	Context("SetActive()/GetActive() should", func() {
		It("set and return active state", func() {
			config := NewEmpty(testConfigName)
			config.SetActive(true)
			Expect(config.GetActive()).To(BeTrue())
			config.SetActive(false)
			Expect(config.GetActive()).To(BeFalse())
		})
	})
	Context("SetDeployed()/GetDeployed() should", func() {
		It("set and return deployed state", func() {
			config := NewEmpty(testConfigName)
			config.SetDeployed(true)
			Expect(config.GetDeployed()).To(BeTrue())
			config.SetDeployed(false)
			Expect(config.GetDeployed()).To(BeFalse())
		})
	})
	Context("GetNext() should", func() {
		It("return next config to be deployed", func() {
			config := NewEmpty(testConfigName)
			cfg := v1alpha1.NewEmptyConfig(testConfigName)
			config.UpdateNext(cfg)
			Expect(config.GetNext().IsEqual(cfg)).To(BeTrue())
		})
	})
	Context("SetBackupAsNext() should", func() {
		It("copy values of backup config to next config", func() {
			config := NewEmpty(testConfigName)
			config.backup = v1alpha1.NewEmptyConfig(testConfigName + BackupSuffix)
			config.backup.Spec.RoutingTable = append(config.backup.Spec.RoutingTable, v1alpha1.RoutingTableSpec{TableID: 1})
			wasSet := config.SetBackupAsNext()
			Expect(wasSet).To(BeTrue())
			Expect(config.GetNext().IsEqual(config.backup)).To(BeTrue())
		})
	})
	Context("Deploy() should", func() {
		It("skip the deployment if it is the same as the existing one", func() {
			config := NewEmpty(testConfigName)
			ctx := context.TODO()
			c := createClient(fakeNodeConfig)
			err := config.Deploy(ctx, c, logr.New(nil), time.Millisecond*200)
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error if context deadline was exceeded when deploying config", func() {
			config := NewEmpty(testConfigName)
			parent := context.Background()
			ctx, cancel := context.WithTimeout(parent, time.Millisecond*200)
			defer cancel()
			childCtx := context.WithValue(ctx, ParentCtx, parent)

			fakeNodeConfig.Items[0].Spec.RoutingTable = []v1alpha1.RoutingTableSpec{{TableID: 1}}
			c := createClient(fakeNodeConfig)

			err := config.Deploy(childCtx, c, logr.New(nil), time.Millisecond*200)
			Expect(err).To(HaveOccurred())
		})
		It("return error if context deadline was exceeded when invalidating config", func() {
			config := NewEmpty(testConfigName)
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*200)
			defer cancel()
			childCtx := context.WithValue(ctx, ParentCtx, ctx)
			fakeNodeConfig.Items[0].Spec.RoutingTable = []v1alpha1.RoutingTableSpec{{TableID: 1}}
			c := createClient(fakeNodeConfig)

			err := config.Deploy(childCtx, c, logr.New(nil), time.Millisecond*200)
			Expect(err).To(HaveOccurred())
		})
		It("return no error if deployment was successful", func() {
			config := NewEmpty(testConfigName)
			config.active.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*300)
			defer cancel()
			childCtx := context.WithValue(ctx, ParentCtx, ctx)
			fakeNodeConfig.Items[0].Spec.RoutingTable = []v1alpha1.RoutingTableSpec{{TableID: 1}}
			c := createClient(fakeNodeConfig)

			quit := make(chan bool)
			var deployErr error
			go func() {
				deployErr = config.Deploy(childCtx, c, logr.New(nil), time.Millisecond*300)
				quit <- true
			}()

			time.Sleep(time.Millisecond * 100)
			err := config.updateStatus(ctx, c, config.current, StatusProvisioned)
			Expect(err).ToNot(HaveOccurred())

			<-quit
			Expect(deployErr).ToNot(HaveOccurred())
		})
	})
	Context("CreateBackup() should", func() {
		It("return no error if backup config was created", func() {
			config := NewEmpty(testConfigName)
			c := createClient(fakeNodeConfig)
			err := config.CreateBackup(context.Background(), c)
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("CreateInvalid() should", func() {
		It("return no error if invalid config was created", func() {
			config := NewEmpty(testConfigName)
			c := createClient(fakeNodeConfig)
			err := config.CrateInvalid(context.Background(), c)
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("Prune() should", func() {
		It("return no error if all configs were deleted", func() {
			config := New(testConfigName,
				v1alpha1.NewEmptyConfig(testConfigName),
				v1alpha1.NewEmptyConfig(testConfigName+BackupSuffix),
				v1alpha1.NewEmptyConfig(testConfigName+InvalidSuffix),
			)
			c := createClient(fakeNodeConfig)
			err := config.Prune(context.Background(), c)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

func createClient(nodeConfigs *v1alpha1.NodeConfigList) client.Client {
	s := runtime.NewScheme()
	err := corev1.AddToScheme(s)
	Expect(err).ToNot(HaveOccurred())
	err = v1alpha1.AddToScheme(s)
	Expect(err).ToNot(HaveOccurred())
	return fake.NewClientBuilder().WithScheme(s).
		WithRuntimeObjects(nodeConfigs, fakeProcess).
		WithStatusSubresource(&fakeNodeConfig.Items[0]).
		Build()
}
