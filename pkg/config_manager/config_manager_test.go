package configmanager

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	mock_config_map "github.com/telekom/das-schiff-network-operator/pkg/config_map/mock"
	"github.com/telekom/das-schiff-network-operator/pkg/nodeconfig"
	mock_nodeconfig "github.com/telekom/das-schiff-network-operator/pkg/nodeconfig/mock"
	mock_reconciler "github.com/telekom/das-schiff-network-operator/pkg/reconciler/mock"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	ctrl *gomock.Controller
)

func TestConfigMap(t *testing.T) {
	RegisterFailHandler(Fail)
	ctrl = gomock.NewController(t)
	defer ctrl.Finish()
	RunSpecs(t,
		"ConfigManager Suite")
}

var _ = Describe("ConfigManager", func() {
	nodeName := "testNode"
	Context("WatchConfigs() should", func() {
		It("return no error to errCh if context was cancelled", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithCancel(context.Background())
			err := runContextTest(ctx, cancel, cm.WatchConfigs)
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error to errCh if context is done for reason other cancelation", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
			defer cancel()

			err := runContextTest(ctx, nil, cm.WatchConfigs)
			Expect(err).To(HaveOccurred())
		})
		It("return error to errCh if cannot create config for node", func() {
			cm, crm, nrm := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			nrm.EXPECT().GetNodes().Return(map[string]*corev1.Node{nodeName: {}})
			crm.EXPECT().CreateConfigForNode(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("error getting config for node %s", nodeName))

			errCh := make(chan error)
			defer close(errCh)
			err := runTest(ctx, cm, errCh, nil)
			Expect(err).To(HaveOccurred())
		})
		It("return error to errCh if there was an error getting config for node from memory", func() {
			cm, crm, nrm := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi

			nrm.EXPECT().GetNodes().Return(map[string]*corev1.Node{nodeName: {ObjectMeta: metav1.ObjectMeta{Name: nodeName}}})
			crm.EXPECT().CreateConfigForNode(gomock.Any(), gomock.Any()).Return(&v1alpha1.NodeConfig{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}, nil)
			cmi.EXPECT().Get(gomock.Any()).Return(nil, fmt.Errorf("error gettting config %s", nodeName))

			errCh := make(chan error)
			defer close(errCh)
			err := runTest(ctx, cm, errCh, cancel)
			Expect(err).To(HaveOccurred())
		})
	})
	Context("updateConfigs() should", func() {
		It("return no error if config is being updated", func() {
			cm, cr, nr := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi
			cfg := mock_nodeconfig.NewMockConfigInterface(ctrl)

			nr.EXPECT().GetNodes().Return(map[string]*corev1.Node{nodeName: {}})
			cr.EXPECT().CreateConfigForNode(nodeName, gomock.Any()).Return(v1alpha1.NewEmptyConfig(nodeName), nil)
			cmi.EXPECT().Get(nodeName).Return(cfg, nil)
			cfg.EXPECT().UpdateNext(gomock.Any())

			err := cm.updateConfigs()
			Expect(err).ToNot(HaveOccurred())
		})
		It("return no error if config is being created", func() {
			cm, cr, nr := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi

			nr.EXPECT().GetNodes().Return(map[string]*corev1.Node{nodeName: {}})
			cr.EXPECT().CreateConfigForNode(nodeName, gomock.Any()).Return(v1alpha1.NewEmptyConfig(nodeName), nil)
			cmi.EXPECT().Get(nodeName).Return(nil, nil)
			cmi.EXPECT().Store(gomock.Any(), gomock.Any())

			err := cm.updateConfigs()
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("deployConfigs() should", func() {
		It("return error if cannot get config slice", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi

			cmi.EXPECT().GetSlice().Return(nil, fmt.Errorf("error getting config slice"))

			err := cm.deployConfigs(context.Background())
			Expect(err).To(HaveOccurred())
		})
		It("return error if new config is equal to known invalid config", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi
			cfg := mock_nodeconfig.NewMockConfigInterface(ctrl)

			cmi.EXPECT().GetSlice().Return([]nodeconfig.ConfigInterface{cfg}, nil)
			cfg.EXPECT().SetDeployed(false)
			cfg.EXPECT().GetActive().Return(true)
			cfg.EXPECT().GetNext().Return(v1alpha1.NewEmptyConfig(nodeName))
			cfg.EXPECT().GetInvalid().Return(v1alpha1.NewEmptyConfig(nodeName + nodeconfig.InvalidSuffix))
			cfg.EXPECT().GetName().Return(nodeName)

			err := cm.deployConfigs(context.Background())
			Expect(err).To(HaveOccurred())
		})
		It("return error if was unable to deploy and invalidate invalid config", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi
			cfg := mock_nodeconfig.NewMockConfigInterface(ctrl)

			cmi.EXPECT().GetSlice().Return([]nodeconfig.ConfigInterface{cfg}, nil)
			cfg.EXPECT().SetDeployed(false)
			cfg.EXPECT().GetActive().Return(true)
			next := v1alpha1.NewEmptyConfig(nodeName)
			next.Spec.RoutingTable = []v1alpha1.RoutingTableSpec{{TableID: 1}}
			cfg.EXPECT().GetNext().Return(next)
			cfg.EXPECT().GetInvalid().Return(v1alpha1.NewEmptyConfig(nodeName + nodeconfig.InvalidSuffix))
			cfg.EXPECT().GetActive().Return(true)
			cfg.EXPECT().SetCancelFunc(gomock.Any())
			cfg.EXPECT().GetName().Return(nodeName)
			cfg.EXPECT().Deploy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("error deploying config"))
			cfg.EXPECT().CrateInvalid(gomock.Any(), gomock.Any()).Return(fmt.Errorf("error creating invalid config"))

			err := cm.deployConfigs(context.Background())
			Expect(err).To(HaveOccurred())
		})
		It("return no error on successful deployment", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi
			cfg := mock_nodeconfig.NewMockConfigInterface(ctrl)

			cfg.EXPECT().DeleteInvalid(gomock.Any(), gomock.Any()).Return(nil)
			cmi.EXPECT().GetSlice().Return([]nodeconfig.ConfigInterface{cfg}, nil)
			cfg.EXPECT().SetDeployed(false)
			cfg.EXPECT().GetActive().Return(true)
			next := v1alpha1.NewEmptyConfig(nodeName)
			next.Spec.RoutingTable = []v1alpha1.RoutingTableSpec{{TableID: 1}}
			cfg.EXPECT().GetNext().Return(next)
			cfg.EXPECT().GetInvalid().Return(v1alpha1.NewEmptyConfig(nodeName + nodeconfig.InvalidSuffix))
			cfg.EXPECT().GetActive().Return(true)
			cfg.EXPECT().SetCancelFunc(gomock.Any())
			cfg.EXPECT().GetName().Return(nodeName)
			cfg.EXPECT().Deploy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			cfg.EXPECT().GetName().Return(nodeName)

			err := cm.deployConfigs(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("WatchNodes() should", func() {
		It("return no error to errCh if context was cancelled", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithCancel(context.Background())
			err := runContextTest(ctx, cancel, cm.WatchDeletedNodes)
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error to errCh if context is done for reason other cancelation", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
			defer cancel()

			err := runContextTest(ctx, nil, cm.WatchDeletedNodes)
			Expect(err).To(HaveOccurred())
		})
		It("return no error if successfully deleted nodes", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cm.configsMap.Store(nodeName, nodeconfig.NewEmpty(nodeName))

			errCh := make(chan error)

			go func() {
				cm.WatchDeletedNodes(ctx, errCh)
			}()

			cm.deletedNodes <- []string{nodeName}
			time.Sleep(time.Millisecond * 20)
			nodes, err := cm.configsMap.GetSlice()
			Expect(err).ToNot(HaveOccurred())
			Expect(nodes).To(BeEmpty())
			cancel()
			err = <-errCh
			Expect(err).ToNot(HaveOccurred())
		})
	})
	Context("DirtyStartup() should", func() {
		It("return no error if NodeConfigProcess object does not exist", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			err := cm.DirtyStartup(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})
		It("return no error if there is nothing to restore", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			err := cm.DirtyStartup(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})
		It("return error if cannot get config slice", func() {
			cm, _, _ := prepareObjects()
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi

			cmi.EXPECT().GetSlice().Return(nil, fmt.Errorf("cannot get config slice"))

			err := cm.DirtyStartup(context.Background())
			Expect(err).To(HaveOccurred())
		})
		It("return no error if restored configs successfully", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					UID:  "7a4eec39-15c5-4d77-b235-78f46740351",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			invalidConfig := v1alpha1.NewEmptyConfig(nodeName + nodeconfig.InvalidSuffix)
			invalidConfig.OwnerReferences = []metav1.OwnerReference{
				{
					Kind: "node",
					UID:  "7a4eec39-15c5-4d77-b235-78f46740351",
				},
			}
			backupConfig := v1alpha1.NewEmptyConfig(nodeName + nodeconfig.BackupSuffix)
			backupConfig.OwnerReferences = []metav1.OwnerReference{
				{
					Kind: "node",
					UID:  "7a4eec39-15c5-4d77-b235-78f46740351",
				},
			}
			currentConfig := v1alpha1.NewEmptyConfig(nodeName)
			currentConfig.OwnerReferences = []metav1.OwnerReference{
				{
					Kind: "node",
					UID:  "7a4eec39-15c5-4d77-b235-78f46740351",
				},
			}

			cm, _, _ := prepareObjects(node, invalidConfig, backupConfig, currentConfig)
			defer close(cm.changes)
			defer close(cm.deletedNodes)

			cmi := mock_config_map.NewMockInterface(ctrl)
			cm.configsMap = cmi

			configs := []nodeconfig.ConfigInterface{nodeconfig.New(nodeName, currentConfig, backupConfig, invalidConfig)}

			cmi.EXPECT().GetSlice().Return(configs, nil).Times(2)
			cmi.EXPECT().Store(gomock.Any(), gomock.Any())

			err := cm.DirtyStartup(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

func prepareObjects(objects ...runtime.Object) (*ConfigManager, *mock_reconciler.MockConfigReconcilerInterface, *mock_reconciler.MockNodeReconcilerInterface) {
	crm := mock_reconciler.NewMockConfigReconcilerInterface(ctrl)
	nrm := mock_reconciler.NewMockNodeReconcilerInterface(ctrl)

	s := runtime.NewScheme()
	err := corev1.AddToScheme(s)
	Expect(err).ToNot(HaveOccurred())
	err = v1alpha1.AddToScheme(s)
	Expect(err).ToNot(HaveOccurred())
	c := fake.NewClientBuilder().WithScheme(s).
		WithRuntimeObjects(objects...).Build()

	changes := make(chan bool)
	nodesDeleted := make(chan []string)
	cm := New(c, crm, nrm, logr.New(nil), time.Second*10, -1, changes, nodesDeleted)
	Expect(cm).ToNot(BeNil())
	return cm, crm, nrm
}

func runTest(ctx context.Context, cm *ConfigManager, errCh chan error, cancel context.CancelFunc) error {
	start := make(chan bool)
	defer close(start)
	go func() {
		start <- true
		cm.WatchConfigs(ctx, errCh)
	}()
	startVal := <-start
	Expect(startVal).To(BeTrue())

	time.Sleep(time.Millisecond * 100)
	cm.changes <- true
	time.Sleep(time.Millisecond * 100)
	if cancel != nil {
		cancel()
	}
	err := <-errCh
	return err
}

func runContextTest(ctx context.Context, cancel context.CancelFunc, f func(ctx context.Context, errCh chan error)) error {
	errCh := make(chan error)
	defer close(errCh)
	quit := make(chan bool)
	defer close(quit)
	go func() {
		f(ctx, errCh)
		quit <- true
	}()
	if cancel != nil {
		cancel()
	}
	err := <-errCh
	<-quit
	return err
}
