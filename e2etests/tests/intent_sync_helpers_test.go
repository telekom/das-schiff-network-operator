package tests

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type objectClientStub struct {
	client.Client
	getErr    error
	deleteErr error
	deleteHit bool
}

func (s *objectClientStub) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	// Once the object has been deleted, report it gone so the post-delete
	// wait in deleteObject terminates.
	if s.deleteHit {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "vrfs"}, "vrf-sync-m2m")
	}
	return s.getErr
}

func (s *objectClientStub) Delete(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
	s.deleteHit = true
	return s.deleteErr
}

func TestDeleteObjectPropagatesUnexpectedGetError(t *testing.T) {
	stub := &objectClientStub{
		getErr: errors.New("boom"),
	}

	err := deleteObject(context.Background(), stub, "vrfs", remoteNS, "vrf-sync-m2m")
	if err == nil {
		t.Fatal("Expected deleteObject to fail when Get fails unexpectedly")
	}
	if !strings.Contains(err.Error(), "before delete") {
		t.Fatalf("Expected contextual error, got %v", err)
	}
	if stub.deleteHit {
		t.Fatal("Delete must not be called after unexpected Get failure")
	}
}

func TestDeleteObjectIgnoresNotFoundAndNoMatch(t *testing.T) {
	tests := []struct {
		name   string
		getErr error
	}{
		{
			name: "not found",
			getErr: apierrors.NewNotFound(
				schema.GroupResource{Group: "network-connector.sylvaproject.org", Resource: "vrfs"},
				"vrf-sync-m2m",
			),
		},
		{
			name: "no match",
			getErr: &meta.NoKindMatchError{
				GroupKind: schema.GroupKind{Group: "network-connector.sylvaproject.org", Kind: "VRF"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &objectClientStub{getErr: tc.getErr}
			if err := deleteObject(context.Background(), stub, "vrfs", remoteNS, "vrf-sync-m2m"); err != nil {
				t.Fatalf("Expected nil, got %v", err)
			}
			if stub.deleteHit {
				t.Fatal("Delete must not be called when object is already gone")
			}
		})
	}
}

func TestDeleteObjectDeletesWhenObjectExists(t *testing.T) {
	stub := &objectClientStub{}
	if err := deleteObject(context.Background(), stub, "vrfs", remoteNS, "vrf-sync-m2m"); err != nil {
		t.Fatalf("Expected deleteObject to succeed, got %v", err)
	}
	if !stub.deleteHit {
		t.Fatal("Expected Delete to be called when object exists")
	}
}
