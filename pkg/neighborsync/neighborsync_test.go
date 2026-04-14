package neighborsync

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/cilium/ebpf/ringbuf"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
)

var (
	mockCtrl  *gomock.Controller
	mockNlOps *mock_nl.MockToolkitInterface
)

func TestNeighborSync(t *testing.T) {
	RegisterFailHandler(Fail)
	mockCtrl = gomock.NewController(t)
	defer mockCtrl.Finish()
	RunSpecs(t, "NeighborSync Suite")
}

var _ = BeforeSuite(func() {
	mockNlOps = mock_nl.NewMockToolkitInterface(mockCtrl)
})

func noopSendNeighborRequest(_ int, _ net.HardwareAddr, _ netip.Addr) {}
func noopSendGratuitous(_ int, _ netip.Addr, _ net.HardwareAddr)      {}

var _ = Describe("neighSubscribeFn DI hook", func() {
	var ns *NeighborSync
	var subscribeCalled bool

	BeforeEach(func() {
		subscribeCalled = false
		ns = &NeighborSync{
			nlOps:                    mockNlOps,
			sendNeighborRequestFn:    noopSendNeighborRequest,
			sendGratuitousNeighborFn: noopSendGratuitous,
		}
	})

	It("calls neighSubscribeFn with ListExisting=true and correct channel directions", func() {
		ns.neighSubscribeFn = func(_ chan<- netlink.NeighUpdate, _ <-chan struct{}, opts netlink.NeighSubscribeOptions) error {
			subscribeCalled = true
			Expect(opts.ListExisting).To(BeTrue())
			// Return an error to stop the receiveUpdates loop after verifying options.
			return errors.New("stop")
		}

		done := make(chan struct{})
		go func() {
			ns.receiveUpdates()
			close(done)
		}()

		Eventually(func() bool { return subscribeCalled }, "2s").Should(BeTrue())
		Eventually(done, "2s").Should(BeClosed())
	})

	It("stops receiveUpdates loop when neighSubscribeFn returns an error", func() {
		callCount := 0
		ns.neighSubscribeFn = func(_ chan<- netlink.NeighUpdate, _ <-chan struct{}, _ netlink.NeighSubscribeOptions) error {
			callCount++
			return errors.New("subscribe failed")
		}

		done := make(chan struct{})
		go func() {
			ns.receiveUpdates()
			close(done)
		}()

		Eventually(done, "2s").Should(BeClosed())
		Expect(callCount).To(Equal(1))
	})
})

var _ = Describe("newRingbufReaderFn DI hook", func() {
	var ns *NeighborSync

	BeforeEach(func() {
		ns = &NeighborSync{
			nlOps:                    mockNlOps,
			sendNeighborRequestFn:    noopSendNeighborRequest,
			sendGratuitousNeighborFn: noopSendGratuitous,
		}
	})

	It("calls newRingbufReaderFn and exits runBpfNeighborSync when it returns an error", func() {
		ringbufCalled := false
		ns.newRingbufReaderFn = func() (*ringbuf.Reader, error) {
			ringbufCalled = true
			return nil, errors.New("ringbuf open failed")
		}

		done := make(chan struct{})
		go func() {
			ns.runBpfNeighborSync()
			close(done)
		}()

		Eventually(done, "2s").Should(BeClosed())
		Expect(ringbufCalled).To(BeTrue())
	})
})

var _ = Describe("EnsureNeighborSuppression", func() {
	var ns *NeighborSync
	var bpfAttachCallCount int
	var fakeLink netlink.Link

	BeforeEach(func() {
		bpfAttachCallCount = 0
		fakeLink = &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Index: 10, MasterIndex: 20}}

		ns = &NeighborSync{
			nlOps:                    mockNlOps,
			sendNeighborRequestFn:    noopSendNeighborRequest,
			sendGratuitousNeighborFn: noopSendGratuitous,
			bpfAttachFn: func(_ netlink.Link) error {
				bpfAttachCallCount++
				return nil
			},
			bpfDetachFn: func(_ netlink.Link) error { return nil },
		}
		ns.initOnce = sync.Once{}
	})

	It("calls bpfAttachFn once on first call", func() {
		mockNlOps.EXPECT().LinkByIndex(10).Return(fakeLink, nil).Times(1)
		mockNlOps.EXPECT().NeighList(20, gomock.Any()).Return(nil, nil).AnyTimes()

		err := ns.EnsureNeighborSuppression(20, 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(bpfAttachCallCount).To(Equal(1))
	})

	It("is idempotent: second call with same vethID skips bpfAttachFn", func() {
		mockNlOps.EXPECT().LinkByIndex(10).Return(fakeLink, nil).Times(1)
		mockNlOps.EXPECT().NeighList(20, gomock.Any()).Return(nil, nil).AnyTimes()

		Expect(ns.EnsureNeighborSuppression(20, 10)).To(Succeed())
		Expect(ns.EnsureNeighborSuppression(20, 10)).To(Succeed())

		Expect(bpfAttachCallCount).To(Equal(1))
	})

	It("returns error when LinkByIndex fails", func() {
		mockNlOps.EXPECT().LinkByIndex(10).Return(nil, errors.New("link not found")).Times(1)

		err := ns.EnsureNeighborSuppression(20, 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to get link by index"))
	})

	It("returns error when bpfAttachFn fails", func() {
		ns.bpfAttachFn = func(_ netlink.Link) error {
			return errors.New("bpf attach failed")
		}
		mockNlOps.EXPECT().LinkByIndex(10).Return(fakeLink, nil).Times(1)

		err := ns.EnsureNeighborSuppression(20, 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to attach BPF program"))
	})

	It("does not add vethID to tracked maps when bpfAttachFn fails", func() {
		ns.bpfAttachFn = func(_ netlink.Link) error {
			return errors.New("bpf attach failed")
		}
		mockNlOps.EXPECT().LinkByIndex(10).Return(fakeLink, nil).Times(1)

		_ = ns.EnsureNeighborSuppression(20, 10)

		_, tracked := ns.receiveNeighbors.Load(10)
		Expect(tracked).To(BeFalse())
		_, gratuitous := ns.sendGratuitousNeighbor.Load(20)
		Expect(gratuitous).To(BeFalse())
	})
})
