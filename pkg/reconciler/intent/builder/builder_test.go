package builder

import "testing"

func TestNewNodeContribution_Initialized(t *testing.T) {
	nc := NewNodeContribution()
	if nc.Layer2s == nil {
		t.Fatal("expected Layer2s map to be initialized")
	}
	if nc.FabricVRFs == nil {
		t.Fatal("expected FabricVRFs map to be initialized")
	}
	if nc.LocalVRFs == nil {
		t.Fatal("expected LocalVRFs map to be initialized")
	}
	if nc.Origins == nil {
		t.Fatal("expected Origins map to be initialized")
	}
	if nc.ClusterVRF != nil {
		t.Fatal("expected ClusterVRF to be nil")
	}
}

func TestSetOrigin(t *testing.T) {
	nc := NewNodeContribution()
	nc.SetOrigin("layer2s/vlan100", "Layer2Attachment/my-l2a")

	if got := nc.Origins["layer2s/vlan100"]; got != "Layer2Attachment/my-l2a" {
		t.Fatalf("expected origin 'Layer2Attachment/my-l2a', got %q", got)
	}
}

func TestSetOrigin_NilMap(t *testing.T) {
	nc := &NodeContribution{} // Origins is nil
	nc.SetOrigin("key", "source")

	if nc.Origins == nil {
		t.Fatal("expected Origins to be lazily initialized")
	}
	if got := nc.Origins["key"]; got != "source" {
		t.Fatalf("expected origin 'source', got %q", got)
	}
}

func TestSetOrigin_Overwrite(t *testing.T) {
	nc := NewNodeContribution()
	nc.SetOrigin("key", "old")
	nc.SetOrigin("key", "new")

	if got := nc.Origins["key"]; got != "new" {
		t.Fatalf("expected overwritten origin 'new', got %q", got)
	}
}
