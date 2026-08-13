package tests

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/telekom/das-schiff-network-operator/e2etests/framework"
)

// readTestdata reads a file from the testdata directory.
// When running inside the tester container, testdata is at /e2etests/testdata/.
// When running locally via `go test ./e2etests/`, CWD is e2etests/ so testdata/ is relative.
func readTestdata(path string) ([]byte, error) {
	// Try container path first
	data, err := os.ReadFile("/e2etests/testdata/" + path)
	if err == nil {
		return data, nil
	}
	// Fall back to relative path (CWD = e2etests/ when running go test)
	return os.ReadFile("testdata/" + path)
}

// releaseIntentFixture deletes an intent fixture manifest and waits until it is
// really gone.
//
// Several intent fixtures claim the same VLANs under differently named
// Layer2Attachments -- net-vlan501 alone is claimed by l2a-base-vlan501,
// l2a-vlan501, l2a-l3-vlan501 and l2a-vrf-vlan501 -- and a VLAN has exactly one
// owning attachment per node. Ginkgo randomises the order of top-level
// containers per seed, so a fixture left behind silently decides that ownership
// for whichever spec runs next; the workload-CNI specs assert that
// l2a-base-vlan501 owns 501 and fail against any of the others. Every spec must
// therefore hand its VLANs back before the next one starts.
func releaseIntentFixture(ctx context.Context, f *framework.Framework, path string) {
	GinkgoHelper()

	manifest, err := readTestdata(path)
	Expect(err).NotTo(HaveOccurred())
	releaseIntentManifest(ctx, f, manifest)
}

// releaseIntentManifest is releaseIntentFixture for callers that already hold
// the manifest bytes.
func releaseIntentManifest(ctx context.Context, f *framework.Framework, manifest []byte) {
	GinkgoHelper()

	// Only the Layer2Attachments have to be gone before the next spec starts:
	// they are what claims a VLAN. Networks and Destinations in the same
	// fixture are held by in-use finalizers from the permanent base fixtures
	// and legitimately outlive it.
	Expect(f.DeleteManifestAndWaitForKinds(ctx, manifest,
		[]string{"Layer2Attachment"}, 2*time.Minute)).To(Succeed())
}
