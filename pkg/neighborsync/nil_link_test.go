package neighborsync

import (
	"testing"

	mock_nl "github.com/telekom/das-schiff-network-operator/pkg/nl/mock"
	"github.com/vishvananda/netlink"
	"go.uber.org/mock/gomock"
)

func TestSuppressionRejectsNilLink(t *testing.T) {
	for _, disable := range []bool{false, true} {
		name := "enable"
		if disable {
			name = "disable"
		}
		t.Run(name, func(t *testing.T) {
			ops := mock_nl.NewMockToolkitInterface(gomock.NewController(t))
			n := newTestNeighborSync(ops)
			n.bpfAttachFn = func(netlink.Link) error { t.Fatal("must not attach nil link"); return nil }
			n.bpfDetachFn = func(netlink.Link) error { t.Fatal("must not detach nil link"); return nil }
			if disable {
				n.sendGratuitousNeighbor.Store(5, struct{}{})
				n.receiveNeighbors.Store(10, struct{}{})
			}
			ops.EXPECT().LinkByIndex(10).Return(nil, nil)
			operation := n.EnsureNeighborSuppression
			if disable {
				operation = n.DisableNeighborSuppression
			}
			if err := operation(5, 10); err == nil {
				t.Fatal("expected nil-link error")
			}
			_, bridge := n.sendGratuitousNeighbor.Load(5)
			_, veth := n.receiveNeighbors.Load(10)
			if bridge != disable || veth != disable {
				t.Fatal("nil link changed suppression state")
			}
		})
	}
}
