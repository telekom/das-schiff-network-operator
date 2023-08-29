package healthcheck

import (
	"context"
	"encoding/json"
	"errors"
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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
)

const testHostname = "worker"

var (
	fakeNodesJSON = `{"items":[{"metadata":{"name":"` + testHostname + `"},"spec":{"taints":[{"effect":"NoSchedule",
					"key":"` + TaintKey + `"}]}}]}`
	fakeNodes *corev1.NodeList
	tmpPath   string
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
	tmpPath = t.TempDir()
	ctrl = gomock.NewController(t)
	defer ctrl.Finish()
	RunSpecsWithDefaultAndCustomReporters(t,
		"HealthCheck Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = Describe("HealthCheck", func() {
	Context("LoadConfig() should", func() {
		It("return error if config is mandatory but does not exist", func() {
			oldValue := os.Getenv(configEnv)
			Expect(os.Setenv(configEnv, "/some/invalid/path")).To(Succeed())
			_, err := LoadConfig("")
			Expect(err).To(HaveOccurred())
			Expect(os.Setenv(configEnv, oldValue)).To(Succeed())
		})
		It("return error if config is invalid", func() {
			_, err := LoadConfig("./testdata/invalidconfig.yaml")
			Expect(err).To(HaveOccurred())
		})
		It("return no error if valid config exists", func() {
			_, err := LoadConfig("./testdata/simpleconfig.yaml")
			Expect(err).ToNot(HaveOccurred())
		})
		It("return no error if config does not exist but is not mandatory", func() {
			conf, err := LoadConfig("/some/invalid/path")
			Expect(err).ToNot(HaveOccurred())
			Expect(conf).ToNot(BeNil())
		})
	})
	Context("IsFRRActive() should", func() {
		frm := mock_healthcheck.NewMockFRRInterface(ctrl)
		It("return error if FRR Manager returns error", func() {
			c := fake.NewClientBuilder().Build()
			frm.EXPECT().GetStatusFRR().Return("", "", errors.New("fake error"))
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, NewDefaultHealthcheckToolkit(frm, NewTCPDialer("")), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			result, err := hc.IsFRRActive()
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeFalse())
		})
		It("return error if FRR is inactive", func() {
			c := fake.NewClientBuilder().Build()
			frm.EXPECT().GetStatusFRR().Return("inactive", "stopped", nil)
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, NewDefaultHealthcheckToolkit(frm, NewTCPDialer("")), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			result, err := hc.IsFRRActive()
			Expect(err).To(HaveOccurred())
			Expect(result).To(BeFalse())
		})
		It("return no error if FRR is active", func() {
			c := fake.NewClientBuilder().Build()
			frm.EXPECT().GetStatusFRR().Return("active", "running", nil)
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, NewDefaultHealthcheckToolkit(frm, NewTCPDialer("")), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			result, err := hc.IsFRRActive()
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeTrue())
		})
	})
	Context("RemoveTaint() should", func() {
		It("return error about no nodes", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, nil, nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.RemoveTaint(context.Background(), TaintKey)
			Expect(err).To(HaveOccurred())
			Expect(hc.IsInitialized()).To(BeFalse())
		})
		It("return error when trying to remove taint (update node)", func() {
			c := &updateErrorClient{}
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, nil, nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.RemoveTaint(context.Background(), TaintKey)
			Expect(err).To(HaveOccurred())
			Expect(hc.IsInitialized()).To(BeFalse())
		})
		It("remove taint and set isInitialized true", func() {
			c := fake.NewClientBuilder().WithRuntimeObjects(fakeNodes).Build()
			nc := &NetHealthcheckConfig{}
			hc, err := NewHealthChecker(c, nil, nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.RemoveTaint(context.Background(), TaintKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc.IsInitialized()).To(BeTrue())
		})
	})
	Context("CheckInterfaces() should", func() {
		It("return error if interface is not present", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{Interfaces: []string{"A", "B"}}
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeErrorGetByName, &net.Dialer{Timeout: time.Duration(3)}), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.CheckInterfaces()
			Expect(err).To(HaveOccurred())
			Expect(hc.IsInitialized()).To(BeFalse())
		})
		It("return error if interface is not up", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{Interfaces: []string{"A", "B"}}
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeDownGetByName, &net.Dialer{Timeout: time.Duration(3)}), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.CheckInterfaces()
			Expect(err).To(HaveOccurred())
			Expect(hc.IsInitialized()).To(BeFalse())
		})
		It("return error if all links are up", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{Interfaces: []string{"A", "B"}}
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, NewTCPDialer("")), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.CheckInterfaces()
			Expect(err).ToNot(HaveOccurred())
			Expect(hc.IsInitialized()).To(BeFalse())
		})
	})
	Context("NewTcpDialer() should", func() {
		It("should use dialer with 3s timeout", func() {
			c := fake.NewClientBuilder().Build()
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, NewTCPDialer("")), &NetHealthcheckConfig{})
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			d := hc.toolkit.tcpDialer.(*net.Dialer)
			Expect(d.Timeout).To(Equal(time.Second * 3))
		})
		It("should use dialer with 5s timeout", func() {
			c := fake.NewClientBuilder().Build()
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, NewTCPDialer("5")), &NetHealthcheckConfig{})
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			d := hc.toolkit.tcpDialer.(*net.Dialer)
			Expect(d.Timeout).To(Equal(time.Second * 5))
		})
		It("should use dialer with 500ms timeout", func() {
			c := fake.NewClientBuilder().Build()
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, NewTCPDialer("500ms")), &NetHealthcheckConfig{})
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			d := hc.toolkit.tcpDialer.(*net.Dialer)
			Expect(d.Timeout).To(Equal(time.Millisecond * 500))
		})
	})
	Context("CheckReachability() should", func() {
		dialerMock := mock_healthcheck.NewMockTCPDialerInterface(ctrl)

		It("should return error if cannot reach host", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{
				Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
			}
			dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(nil, errors.New("fake error")).Times(defaultRetries)
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, dialerMock), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.CheckReachability()
			Expect(err).To(HaveOccurred())
		})
		It("should return no error if can reach host", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{
				Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
			}
			dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(&fakeConn{}, nil).Times(1)
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, dialerMock), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.CheckReachability()
			Expect(err).ToNot(HaveOccurred())
		})
		It("should return no error if can reach host but connection was refused", func() {
			c := fake.NewClientBuilder().Build()
			nc := &NetHealthcheckConfig{
				Reachability: []netReachabilityItem{{Host: "someHost", Port: 42}},
			}
			dialerMock.EXPECT().Dial("tcp", "someHost:42").Return(nil, errors.New("connect: connection refused")).Times(1)
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, dialerMock), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
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
			hc, err := NewHealthChecker(c, NewHealthCheckToolkit(nil, fakeUpGetByName, dialerMock), nc)
			Expect(err).ToNot(HaveOccurred())
			Expect(hc).ToNot(BeNil())
			Expect(hc.IsInitialized()).To(BeFalse())
			err = hc.CheckReachability()
			Expect(err).To(HaveOccurred())
		})
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

func (*updateErrorClient) Get(_ context.Context, _ types.NamespacedName, o client.Object) error {
	a := o.(*corev1.Node)
	a.Name = testHostname
	a.Spec.Taints = []corev1.Taint{{
		Key:    TaintKey,
		Effect: corev1.TaintEffectNoSchedule},
	}
	return nil
}
