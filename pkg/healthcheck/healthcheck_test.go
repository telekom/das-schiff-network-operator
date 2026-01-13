package healthcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	mock_healthcheck "github.com/telekom/das-schiff-network-operator/pkg/healthcheck/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testHostname = "worker"

const testTaint = "node.cloudprovider.kubernetes.io/uninitialized"
const testTaint2 = "node.kubernetes.io/not-ready"

var (
	fakeNodesJSON = `{"items":[{"metadata":{"name":"` + testHostname + `"},"spec":{"taints":[{"effect":"NoSchedule",
					"key":"` + testTaint + `"},{"effect":"NoSchedule","key":"` + testTaint2 + `"}]}}]}`
	fakeNodes *corev1.NodeList
	ctrl      *gomock.Controller
)

var _ = BeforeSuite(func() {
	fakeNodes = &corev1.NodeList{}
	err := json.Unmarshal([]byte(fakeNodesJSON), fakeNodes)
	Expect(err).ShouldNot(HaveOccurred())
})

func TestHealthCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	t.Setenv(NodenameEnv, testHostname)
	_ = t.TempDir()
	ctrl = gomock.NewController(t)
	defer ctrl.Finish()
	RunSpecs(t,
		"HealthCheck Suite")
}

var _ = Describe("LoadConfig()", func() {
	It("returns error if config is mandatory but does not exist", func() {
		oldValue := os.Getenv(configEnv)
		Expect(os.Setenv(configEnv, "/some/invalid/path")).To(Succeed())
		_, err := LoadConfig("")
		Expect(err).To(HaveOccurred())
		Expect(os.Setenv(configEnv, oldValue)).To(Succeed())
	})
	It("returns error if config is invalid", func() {
		_, err := LoadConfig("./testdata/invalidconfig.yaml")
		Expect(err).To(HaveOccurred())
	})
	It("returns no error if valid config exists", func() {
		_, err := LoadConfig("./testdata/simpleconfig.yaml")
		Expect(err).ToNot(HaveOccurred())
	})
	It("returns no error if config does not exist but is not mandatory", func() {
		conf, err := LoadConfig("/some/invalid/path")
		Expect(err).ToNot(HaveOccurred())
		Expect(conf).ToNot(BeNil())
	})
})
var _ = Describe("RemoveTaints()", func() {
	It("returns error about no nodes", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.RemoveTaints(context.Background())
		Expect(err).To(HaveOccurred())
		Expect(hc.TaintsRemoved()).To(BeFalse())
	})
	It("returns error when trying to remove taint (update node)", func() {
		c := &updateErrorClient{}
		nc := &NetHealthcheckConfig{Taints: []string{testTaint, testTaint2}}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.RemoveTaints(context.Background())
		Expect(err).To(HaveOccurred())
		Expect(hc.TaintsRemoved()).To(BeFalse())

		// Verify the node still has both taints since update failed
		node := &corev1.Node{}
		err = c.Get(context.Background(), types.NamespacedName{Name: testHostname}, node)
		Expect(err).ToNot(HaveOccurred())
		Expect(node.Spec.Taints).To(HaveLen(2))
		Expect(node.Spec.Taints[0].Key).To(Equal(testTaint))
		Expect(node.Spec.Taints[1].Key).To(Equal(testTaint2))
	})
	It("remove taint and set isInitialized true", func() {
		c := fake.NewClientBuilder().WithRuntimeObjects(fakeNodes).Build()
		nc := &NetHealthcheckConfig{Taints: []string{testTaint, testTaint2}}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.RemoveTaints(context.Background())
		Expect(err).ToNot(HaveOccurred())
		Expect(hc.TaintsRemoved()).To(BeTrue())

		// Verify the actual node object
		node := &corev1.Node{}
		err = c.Get(context.Background(), types.NamespacedName{Name: testHostname}, node)
		Expect(err).ToNot(HaveOccurred())
		Expect(node.Spec.Taints).To(BeEmpty())
	})
})
var _ = Describe("UpdateReadinessCondition()", func() {
	It("creates readiness condition when absent", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testHostname}}
		c := fake.NewClientBuilder().WithRuntimeObjects(node).WithStatusSubresource(&corev1.Node{}).Build()
		nc := &NetHealthcheckConfig{}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		err = hc.UpdateReadinessCondition(context.Background(), corev1.ConditionTrue, ReasonHealthChecksPassed, "all good")
		Expect(err).ToNot(HaveOccurred())
		updated := &corev1.Node{}
		Expect(c.Get(context.Background(), types.NamespacedName{Name: testHostname}, updated)).To(Succeed())
		var cond *corev1.NodeCondition
		for i := range updated.Status.Conditions {
			if updated.Status.Conditions[i].Type == NetworkOperatorReadyConditionType {
				cond = &updated.Status.Conditions[i]
				break
			}
		}
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(corev1.ConditionTrue))
		Expect(cond.Reason).To(Equal(ReasonHealthChecksPassed))
	})
	It("updates existing readiness condition without adding duplicate", func() {
		now := metav1.Now()
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testHostname}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type:               NetworkOperatorReadyConditionType,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  now,
			LastTransitionTime: now,
			Reason:             ReasonInterfaceCheckFailed,
			Message:            "iface down",
		}}}}
		c := fake.NewClientBuilder().WithRuntimeObjects(node).WithStatusSubresource(&corev1.Node{}).Build()
		nc := &NetHealthcheckConfig{}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		err = hc.UpdateReadinessCondition(context.Background(), corev1.ConditionTrue, ReasonHealthChecksPassed, "all good")
		Expect(err).ToNot(HaveOccurred())
		updated := &corev1.Node{}
		Expect(c.Get(context.Background(), types.NamespacedName{Name: testHostname}, updated)).To(Succeed())
		count := 0
		for i := range updated.Status.Conditions {
			if updated.Status.Conditions[i].Type == NetworkOperatorReadyConditionType {
				count++
				Expect(updated.Status.Conditions[i].Status).To(Equal(corev1.ConditionTrue))
				Expect(updated.Status.Conditions[i].Reason).To(Equal(ReasonHealthChecksPassed))
			}
		}
		Expect(count).To(Equal(1))
	})
	It("returns error when node is not found", func() {
		// No node in the fake client
		c := fake.NewClientBuilder().WithStatusSubresource(&corev1.Node{}).Build()
		nc := &NetHealthcheckConfig{}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		err = hc.UpdateReadinessCondition(context.Background(), corev1.ConditionTrue, ReasonHealthChecksPassed, "all good")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("error retrieving node"))
	})
	It("does not change LastTransitionTime when status stays the same", func() {
		// Use a fixed time in the past to avoid precision issues
		fixedTime := metav1.NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testHostname}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type:               NetworkOperatorReadyConditionType,
			Status:             corev1.ConditionTrue,
			LastHeartbeatTime:  fixedTime,
			LastTransitionTime: fixedTime,
			Reason:             ReasonHealthChecksPassed,
			Message:            "previous message",
		}}}}
		c := fake.NewClientBuilder().WithRuntimeObjects(node).WithStatusSubresource(&corev1.Node{}).Build()
		nc := &NetHealthcheckConfig{}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		// Update with same status but different message
		err = hc.UpdateReadinessCondition(context.Background(), corev1.ConditionTrue, ReasonHealthChecksPassed, "updated message")
		Expect(err).ToNot(HaveOccurred())
		updated := &corev1.Node{}
		Expect(c.Get(context.Background(), types.NamespacedName{Name: testHostname}, updated)).To(Succeed())
		var cond *corev1.NodeCondition
		for i := range updated.Status.Conditions {
			if updated.Status.Conditions[i].Type == NetworkOperatorReadyConditionType {
				cond = &updated.Status.Conditions[i]
				break
			}
		}
		Expect(cond).ToNot(BeNil())
		// LastTransitionTime should NOT change when status stays the same (compare Unix timestamp)
		Expect(cond.LastTransitionTime.Unix()).To(Equal(fixedTime.Unix()))
		// But message should be updated
		Expect(cond.Message).To(Equal("updated message"))
	})
	It("changes LastTransitionTime when status changes", func() {
		oldTime := metav1.NewTime(metav1.Now().Add(-1 * time.Hour))
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testHostname}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type:               NetworkOperatorReadyConditionType,
			Status:             corev1.ConditionFalse,
			LastHeartbeatTime:  oldTime,
			LastTransitionTime: oldTime,
			Reason:             ReasonInterfaceCheckFailed,
			Message:            "interface was down",
		}}}}
		c := fake.NewClientBuilder().WithRuntimeObjects(node).WithStatusSubresource(&corev1.Node{}).Build()
		nc := &NetHealthcheckConfig{}
		hc, err := NewHealthChecker(c, nil, nc)
		Expect(err).ToNot(HaveOccurred())
		// Update to new status
		err = hc.UpdateReadinessCondition(context.Background(), corev1.ConditionTrue, ReasonHealthChecksPassed, "now healthy")
		Expect(err).ToNot(HaveOccurred())
		updated := &corev1.Node{}
		Expect(c.Get(context.Background(), types.NamespacedName{Name: testHostname}, updated)).To(Succeed())
		var cond *corev1.NodeCondition
		for i := range updated.Status.Conditions {
			if updated.Status.Conditions[i].Type == NetworkOperatorReadyConditionType {
				cond = &updated.Status.Conditions[i]
				break
			}
		}
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(corev1.ConditionTrue))
		// LastTransitionTime SHOULD change when status changes
		Expect(cond.LastTransitionTime.Time).ToNot(Equal(oldTime.Time))
	})
	It("handles all different failure reasons", func() {
		failReasons := []struct {
			reason  string
			message string
		}{
			{ReasonInterfaceCheckFailed, "eth0 is down"},
			{ReasonReachabilityFailed, "cannot ping 10.0.0.1"},
			{ReasonAPIServerFailed, "api server timeout"},
		}
		for _, r := range failReasons {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testHostname}}
			c := fake.NewClientBuilder().WithRuntimeObjects(node).WithStatusSubresource(&corev1.Node{}).Build()
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, nil, nc)
			Expect(err).ToNot(HaveOccurred())
			err = hc.UpdateReadinessCondition(context.Background(), corev1.ConditionFalse, r.reason, r.message)
			Expect(err).ToNot(HaveOccurred())
			updated := &corev1.Node{}
			Expect(c.Get(context.Background(), types.NamespacedName{Name: testHostname}, updated)).To(Succeed())
			var cond *corev1.NodeCondition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == NetworkOperatorReadyConditionType {
					cond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(cond).ToNot(BeNil(), "condition should exist for reason: "+r.reason)
			Expect(cond.Status).To(Equal(corev1.ConditionFalse))
			Expect(cond.Reason).To(Equal(r.reason))
			Expect(cond.Message).To(Equal(r.message))
		}
	})
})
var _ = Describe("CheckInterfaces()", func() {
	It("returns error if interface is not present", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{Interfaces: []string{"A", "B"}}
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeErrorGetByName, &net.Dialer{Timeout: time.Duration(3)}), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckInterfaces()
		Expect(err).To(HaveOccurred())
		Expect(hc.TaintsRemoved()).To(BeFalse())
	})
	It("returns error if interface is not up", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{Interfaces: []string{"A", "B"}}
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeDownGetByName, &net.Dialer{Timeout: time.Duration(3)}), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckInterfaces()
		Expect(err).To(HaveOccurred())
		Expect(hc.TaintsRemoved()).To(BeFalse())
	})
	It("returns error if all links are up", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{Interfaces: []string{"A", "B"}}
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, NewTCPDialer("")), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckInterfaces()
		Expect(err).ToNot(HaveOccurred())
		Expect(hc.TaintsRemoved()).To(BeFalse())
	})
})
var _ = Describe("NewTcpDialer()", func() {
	It("should use dialer with 3s timeout", func() {
		c := fake.NewClientBuilder().Build()
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, NewTCPDialer("")), &NetHealthcheckConfig{})
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		d := hc.toolkit.tcpDialer.(*net.Dialer)
		Expect(d.Timeout).To(Equal(time.Second * 3))
	})
	It("should use dialer with 5s timeout", func() {
		c := fake.NewClientBuilder().Build()
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, NewTCPDialer("5")), &NetHealthcheckConfig{})
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		d := hc.toolkit.tcpDialer.(*net.Dialer)
		Expect(d.Timeout).To(Equal(time.Second * 5))
	})
	It("should use dialer with 500ms timeout", func() {
		c := fake.NewClientBuilder().Build()
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, NewTCPDialer("500ms")), &NetHealthcheckConfig{})
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		d := hc.toolkit.tcpDialer.(*net.Dialer)
		Expect(d.Timeout).To(Equal(time.Millisecond * 500))
	})
})
var _ = Describe("CheckReachability()", func() {
	dialerMock := mock_healthcheck.NewMockTCPDialerInterface(ctrl)

	It("should return error if cannot reach host", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{
			Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
		}
		dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(nil, errors.New("fake error")).Times(defaultRetries)
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, dialerMock), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckReachability()
		Expect(err).To(HaveOccurred())
	})
	It("should return no error if can reach host", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{
			Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
		}
		dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(&fakeConn{}, nil).Times(1)
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, dialerMock), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckReachability()
		Expect(err).ToNot(HaveOccurred())
	})
	It("should return no error if can reach host but connection was refused", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{
			Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
		}
		dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(nil, errors.New("connect: connection refused")).Times(1)
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, dialerMock), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckReachability()
		Expect(err).ToNot(HaveOccurred())
	})
	It("should return error if cannot close connection - with just 1 try", func() {
		c := fake.NewClientBuilder().Build()
		nc := &NetHealthcheckConfig{
			Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
			Retries:      1,
		}
		dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(&fakeConnCloseError{}, nil).Times(1)
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(fakeUpGetByName, dialerMock), nc)
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		Expect(hc.TaintsRemoved()).To(BeFalse())
		err = hc.CheckReachability()
		Expect(err).To(HaveOccurred())
	})
})
var _ = Describe("CheckAPIServer()", func() {
	It("should return no error", func() {
		c := fake.NewClientBuilder().Build()
		hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, nil), &NetHealthcheckConfig{})
		Expect(err).ToNot(HaveOccurred())
		Expect(hc).ToNot(BeNil())
		err = hc.CheckAPIServer(context.TODO())
		Expect(err).ToNot(HaveOccurred())
	})
})

func fakeErrorGetByName(_ string) (netlink.Link, error) {
	return nil, errors.New("Link not found")
}

func fakeDownGetByName(_ string) (netlink.Link, error) {
	return fakeDownLink{}, nil
}

func fakeUpGetByName(_ string) (netlink.Link, error) {
	return fakeUpLink{}, nil
}

type fakeUpLink struct{}

func (fakeUpLink) Attrs() *netlink.LinkAttrs {
	return &netlink.LinkAttrs{
		OperState: netlink.OperUp,
	}
}

func (fakeUpLink) Type() string {
	return ""
}

type fakeDownLink struct{}

func (fakeDownLink) Attrs() *netlink.LinkAttrs {
	return &netlink.LinkAttrs{
		OperState: netlink.OperDown,
	}
}

func (fakeDownLink) Type() string {
	return ""
}

type fakeConn struct {
}

func (*fakeConn) Read(_ []byte) (int, error) {
	return 0, nil
}
func (*fakeConn) Write(_ []byte) (int, error) {
	return 0, nil
}
func (*fakeConn) Close() error {
	return nil
}
func (*fakeConn) LocalAddr() net.Addr {
	return nil
}
func (*fakeConn) RemoteAddr() net.Addr {
	return nil
}
func (*fakeConn) SetDeadline(_ time.Time) error {
	return nil
}
func (*fakeConn) SetReadDeadline(_ time.Time) error {
	return nil
}
func (*fakeConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

type fakeConnCloseError struct {
	fakeConn
}

func (*fakeConnCloseError) Close() error {
	return errors.New("fake error")
}

type updateErrorClient struct {
	client.Client
}

func (*updateErrorClient) Update(
	_ context.Context,
	_ client.Object, _ ...client.UpdateOption) error {
	return errors.New("fake error")
}

func (*updateErrorClient) Get(_ context.Context, _ types.NamespacedName, o client.Object, _ ...client.GetOption) error {
	node, ok := o.(*corev1.Node)
	if !ok {
		return fmt.Errorf("error casting object %v as corev1.Node", o)
	}
	node.Name = testHostname
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    testTaint,
		Effect: corev1.TaintEffectNoSchedule,
	}, corev1.Taint{
		Key:    testTaint2,
		Effect: corev1.TaintEffectNoSchedule,
	})
	return nil
}
