package operator

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("ConfigReconciler", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = ctrl.Log.WithName("test")
	})

	Describe("shouldSkip", func() {
		var cr *ConfigReconciler

		BeforeEach(func() {
			cr = &ConfigReconciler{logger: logger}
		})

		It("should skip when new revision equals last known revision", func() {
			existing := makeRevision("abc123", false, time.Now())
			revisions := &v1alpha1.NetworkConfigRevisionList{
				Items: []v1alpha1.NetworkConfigRevision{existing},
			}
			newRevision := makeRevision("abc123", false, time.Now())
			Expect(cr.shouldSkip(revisions, &newRevision)).To(BeTrue())
		})

		It("should not skip when new revision differs from last known revision", func() {
			existing := makeRevision("abc123", false, time.Now())
			revisions := &v1alpha1.NetworkConfigRevisionList{
				Items: []v1alpha1.NetworkConfigRevision{existing},
			}
			newRevision := makeRevision("def456", false, time.Now())
			Expect(cr.shouldSkip(revisions, &newRevision)).To(BeFalse())
		})

		It("should skip when new revision equals the last known valid revision", func() {
			// First item is invalid, second is the last valid one
			invalid := makeRevision("bad001", true, time.Now())
			valid := makeRevision("abc123", false, time.Now().Add(-time.Minute))
			revisions := &v1alpha1.NetworkConfigRevisionList{
				Items: []v1alpha1.NetworkConfigRevision{invalid, valid},
			}
			newRevision := makeRevision("abc123", false, time.Now())
			Expect(cr.shouldSkip(revisions, &newRevision)).To(BeTrue())
		})

		It("should skip when new revision equals a known invalid revision", func() {
			invalid := makeRevision("abc123", true, time.Now())
			other := makeRevision("def456", false, time.Now().Add(-time.Minute))
			revisions := &v1alpha1.NetworkConfigRevisionList{
				Items: []v1alpha1.NetworkConfigRevision{invalid, other},
			}
			newRevision := makeRevision("abc123", false, time.Now())
			Expect(cr.shouldSkip(revisions, &newRevision)).To(BeTrue())
		})

		It("should skip when invalid item is not first and a valid item precedes it", func() {
			// Items[0] is a valid revision with a different hash — the fast-path does
			// not match and loop 2 breaks at the first valid item. Loop 3 must then
			// find the invalid entry at Items[1] and return true.
			valid := makeRevision("xyz999", false, time.Now())
			invalid := makeRevision("abc123", true, time.Now().Add(-time.Minute))
			revisions := &v1alpha1.NetworkConfigRevisionList{
				Items: []v1alpha1.NetworkConfigRevision{valid, invalid},
			}
			newRevision := makeRevision("abc123", false, time.Now())
			Expect(cr.shouldSkip(revisions, &newRevision)).To(BeTrue())
		})

		It("should not skip when revisions list is empty", func() {
			revisions := &v1alpha1.NetworkConfigRevisionList{}
			newRevision := makeRevision("abc123", false, time.Now())
			Expect(cr.shouldSkip(revisions, &newRevision)).To(BeFalse())
		})
	})

	Describe("sort comparators", func() {
		DescribeTable("lessLayer2",
			func(idA, idB int, expectedResult int) {
				a := v1alpha1.Layer2Revision{
					Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{ID: idA},
				}
				b := v1alpha1.Layer2Revision{
					Layer2NetworkConfigurationSpec: v1alpha1.Layer2NetworkConfigurationSpec{ID: idB},
				}
				Expect(lessLayer2(a, b)).To(Equal(expectedResult))
			},
			Entry("a < b", 1, 2, -1),
			Entry("a > b", 2, 1, 1),
			Entry("a == b", 1, 1, 0),
		)

		DescribeTable("lessLayer3",
			func(nameA, nameB string, seqA, seqB int, expectedResult int) {
				a := v1alpha1.VRFRevision{
					Name:                      nameA,
					VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{Seq: seqA},
				}
				b := v1alpha1.VRFRevision{
					Name:                      nameB,
					VRFRouteConfigurationSpec: v1alpha1.VRFRouteConfigurationSpec{Seq: seqB},
				}
				Expect(lessLayer3(a, b)).To(Equal(expectedResult))
			},
			Entry("name a < b", "a", "b", 1, 1, -1),
			Entry("name a > b", "b", "a", 1, 1, 1),
			Entry("same name, seq a < b", "a", "a", 1, 2, -1),
			Entry("same name, seq a > b", "a", "a", 2, 1, 1),
			Entry("same name, same seq", "a", "a", 1, 1, 0),
		)

		DescribeTable("lessBgp",
			func(nameA, nameB string, expectedResult int) {
				a := v1alpha1.BGPRevision{Name: nameA}
				b := v1alpha1.BGPRevision{Name: nameB}
				Expect(lessBgp(a, b)).To(Equal(expectedResult))
			},
			Entry("a < b", "aaa", "bbb", -1),
			Entry("a > b", "bbb", "aaa", 1),
			Entry("a == b", "aaa", "aaa", 0),
		)

		It("lessRevision should sort newest first", func() {
			t1 := time.Now().Add(-2 * time.Minute)
			t2 := time.Now().Add(-1 * time.Minute)
			older := makeRevision("old", false, t1)
			newer := makeRevision("new", false, t2)

			// lessRevision returns b.Compare(a): positive when b is newer than a,
			// meaning slices.SortFunc places b before a → newest items come first.
			result := lessRevision(older, newer)
			Expect(result).To(BeNumerically(">", 0))

			// Symmetric: when newer is a and older is b, result is negative
			result2 := lessRevision(newer, older)
			Expect(result2).To(BeNumerically("<", 0))
		})
	})

	Describe("fetchLayer2", func() {
		It("should return Layer2Revision items from the cluster", func() {
			l2a := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "l2-100"},
				Spec: v1alpha1.Layer2NetworkConfigurationSpec{
					ID:  100,
					VNI: 1000,
					MTU: 1500,
				},
			}
			l2b := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "l2-200"},
				Spec: v1alpha1.Layer2NetworkConfigurationSpec{
					ID:  200,
					VNI: 2000,
					MTU: 1500,
				},
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(l2a, l2b).
				Build()

			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			result, err := r.fetchLayer2(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(2))
			// sorted by ID ascending
			Expect(result[0].ID).To(Equal(100))
			Expect(result[1].ID).To(Equal(200))
		})

		It("should return error if duplicate VNI found", func() {
			l2a := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "l2-a"},
				Spec:       v1alpha1.Layer2NetworkConfigurationSpec{ID: 100, VNI: 1000, MTU: 1500},
			}
			l2b := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "l2-b"},
				Spec:       v1alpha1.Layer2NetworkConfigurationSpec{ID: 200, VNI: 1000, MTU: 1500},
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(l2a, l2b).
				Build()

			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			_, err := r.fetchLayer2(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("VNI"))
		})
	})

	Describe("fetchLayer3", func() {
		It("should return VRFRevision items from the cluster", func() {
			vrf1 := &v1alpha1.VRFRouteConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "vrf-a"},
				Spec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:    "tenant-a",
					Seq:    10,
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(vrf1).
				Build()

			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			result, err := r.fetchLayer3(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[0].Name).To(Equal("vrf-a"))
		})
	})

	Describe("fetchBgp", func() {
		It("should return BGPRevision items from the cluster", func() {
			bgp := &v1alpha1.BGPPeering{
				ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer"},
				Spec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(bgp).
				Build()

			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			result, err := r.fetchBgp(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[0].Name).To(Equal("bgp-peer"))
		})
	})

	Describe("fetchConfigData", func() {
		It("should return all three resource types", func() {
			l2 := &v1alpha1.Layer2NetworkConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "l2-100"},
				Spec:       v1alpha1.Layer2NetworkConfigurationSpec{ID: 100, VNI: 1000, MTU: 1500},
			}
			vrf := &v1alpha1.VRFRouteConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "vrf-a"},
				Spec: v1alpha1.VRFRouteConfigurationSpec{
					VRF:    "tenant-a",
					Seq:    10,
					Import: []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Export: []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			bgp := &v1alpha1.BGPPeering{
				ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer"},
				Spec: v1alpha1.BGPPeeringSpec{
					RemoteASN: 65000,
					Import:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
					Export:    []v1alpha1.VrfRouteConfigurationPrefixItem{},
				},
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(l2, vrf, bgp).
				Build()

			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			l2vnis, l3vnis, bgps, err := r.fetchConfigData(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(l2vnis).To(HaveLen(1))
			Expect(l3vnis).To(HaveLen(1))
			Expect(bgps).To(HaveLen(1))
		})
	})

	Describe("createRevision", func() {
		It("should create a new revision successfully", func() {
			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			revision := makeRevision("rev001", false, time.Now())

			err := r.createRevision(context.Background(), &revision)
			Expect(err).ToNot(HaveOccurred())

			// Verify the object actually exists in the fake client
			created := &v1alpha1.NetworkConfigRevision{}
			getErr := fakeClient.Get(context.Background(), types.NamespacedName{Name: revision.Name}, created)
			Expect(getErr).ToNot(HaveOccurred())
			Expect(created.Spec.Revision).To(Equal(revision.Spec.Revision))
		})

		It("should delete and recreate when revision already exists", func() {
			existing := makeRevision("rev001", false, time.Now())
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(&existing).
				Build()

			cr := &ConfigReconciler{logger: logger, client: fakeClient}
			r := &reconcileConfig{ConfigReconciler: cr, Logger: logger}

			newRevision := makeRevision("rev001", false, time.Now())

			err := r.createRevision(context.Background(), &newRevision)
			Expect(err).ToNot(HaveOccurred())

			// Verify the object still exists after delete+recreate
			recreated := &v1alpha1.NetworkConfigRevision{}
			getErr := fakeClient.Get(context.Background(), types.NamespacedName{Name: newRevision.Name}, recreated)
			Expect(getErr).ToNot(HaveOccurred())
			Expect(recreated.Spec.Revision).To(Equal(newRevision.Spec.Revision))
		})
	})
})

// makeRevision creates a NetworkConfigRevision for testing.
func makeRevision(revisionHash string, isInvalid bool, createdAt time.Time) v1alpha1.NetworkConfigRevision {
	return v1alpha1.NetworkConfigRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:              revisionHash[:min(10, len(revisionHash))],
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: v1alpha1.NetworkConfigRevisionSpec{
			Revision: revisionHash,
		},
		Status: v1alpha1.NetworkConfigRevisionStatus{
			IsInvalid: isInvalid,
		},
	}
}
