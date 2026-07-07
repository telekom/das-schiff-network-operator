package sync

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	nc "github.com/telekom/das-schiff-network-operator/api/v1alpha1/network-connector"
)

// TestSyncCRDPredicate verifies the watch predicate reconciles on spec,
// label/annotation, and deletion changes, but NOT on status-only updates (the
// controller's own sync-back writes) — which would otherwise storm the
// reconciler.
func TestSyncCRDPredicate(t *testing.T) {
	p := syncCRDPredicate()

	base := func() *nc.Layer2Attachment {
		return &nc.Layer2Attachment{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "l2a",
				Namespace:  "ns",
				Generation: 1,
				Labels:     map[string]string{"a": "1"},
			},
			Spec: nc.Layer2AttachmentSpec{NetworkRef: "net"},
		}
	}

	t.Run("status-only update is ignored", func(t *testing.T) {
		old := base()
		updated := base()
		updated.Status.VRFs = []string{"vrf-a"} // status write, generation unchanged
		if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
			t.Error("status-only update should be filtered (would cause a reconcile storm)")
		}
	})

	t.Run("spec change triggers", func(t *testing.T) {
		old := base()
		updated := base()
		updated.Generation = 2
		if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
			t.Error("generation change should trigger a reconcile")
		}
	})

	t.Run("label change triggers", func(t *testing.T) {
		old := base()
		updated := base()
		updated.Labels = map[string]string{"a": "2"}
		if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
			t.Error("label change should trigger a reconcile")
		}
	})

	t.Run("entering deletion triggers", func(t *testing.T) {
		old := base()
		updated := base()
		now := metav1.Now()
		updated.DeletionTimestamp = &now
		if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}) {
			t.Error("deletion should trigger a reconcile so the remote copy is cleaned up")
		}
	})
}
