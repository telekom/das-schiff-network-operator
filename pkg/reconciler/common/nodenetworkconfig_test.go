package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	mock_healthcheck "github.com/telekom/das-schiff-network-operator/pkg/healthcheck/mock"
	mock_common "github.com/telekom/das-schiff-network-operator/pkg/reconciler/common/mock"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testNodeName = "test-node"

var (
	logger  logr.Logger
	tmpPath string
	scheme  *runtime.Scheme
)

func TestCommon(t *testing.T) {
	RegisterFailHandler(Fail)
	logger = ctrl.Log.WithName("common-test")
	tmpPath = t.TempDir()

	scheme = runtime.NewScheme()
	Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

	RunSpecs(t, "Common Reconciler Suite")
}

var _ = BeforeSuite(func() {
	Expect(os.Setenv("NODE_NAME", testNodeName)).To(Succeed())
})

var _ = AfterSuite(func() {
	Expect(os.Unsetenv("NODE_NAME")).To(Succeed())
})

func createTestNodeNetworkConfig(revision string) *v1alpha1.NodeNetworkConfig {
	return &v1alpha1.NodeNetworkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNodeName,
		},
		Spec: v1alpha1.NodeNetworkConfigSpec{
			Revision: revision,
		},
		Status: v1alpha1.NodeNetworkConfigStatus{},
	}
}

func saveNodeNetworkConfigToFile(cfg *v1alpha1.NodeNetworkConfig, path string) error {
	data, err := json.MarshalIndent(cfg, "", " ")
	if err != nil {
		return fmt.Errorf("error marshalling config: %w", err)
	}
	if err := os.WriteFile(path, data, NodeNetworkConfigFilePerm); err != nil {
		return fmt.Errorf("error writing config file: %w", err)
	}
	return nil
}

// mockReconciler creates a reconciler with mocked dependencies for testing.
// It bypasses the healthcheck initialization since we can't easily mock it.
type mockReconciler struct {
	*NodeNetworkConfigReconciler
	mockApplier       *mock_common.MockConfigApplier
	mockHealthChecker *mock_healthcheck.MockHealthCheckerInterface
}

func newMockReconciler(
	mockCtrl *gomock.Controller,
	c client.Client,
	configPath string,
	opts ReconcilerOptions,
) *mockReconciler {
	mockApplier := mock_common.NewMockConfigApplier(mockCtrl)
	mockHealthChecker := mock_healthcheck.NewMockHealthCheckerInterface(mockCtrl)

	reconciler := &NodeNetworkConfigReconciler{
		client:                    c,
		logger:                    logger,
		configApplier:             mockApplier,
		healthChecker:             mockHealthChecker,
		NodeNetworkConfigPath:     configPath,
		restoreOnReconcileFailure: opts.RestoreOnReconcileFailure,
	}

	return &mockReconciler{
		NodeNetworkConfigReconciler: reconciler,
		mockApplier:                 mockApplier,
		mockHealthChecker:           mockHealthChecker,
	}
}

// setupHealthyHealthCheck sets up mock expectations for a successful health check.
func (m *mockReconciler) setupHealthyHealthCheck() {
	m.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
	m.mockHealthChecker.EXPECT().CheckReachability().Return(nil)
	m.mockHealthChecker.EXPECT().CheckAPIServer(gomock.Any()).Return(nil)
	m.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, gomock.Any()).Return(nil)
	m.mockHealthChecker.EXPECT().TaintsRemoved().Return(true)
}

// setupHealthyHealthCheckWithTaintRemoval sets up mock expectations for health check with taint removal.
func (m *mockReconciler) setupHealthyHealthCheckWithTaintRemoval() {
	m.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
	m.mockHealthChecker.EXPECT().CheckReachability().Return(nil)
	m.mockHealthChecker.EXPECT().CheckAPIServer(gomock.Any()).Return(nil)
	m.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, gomock.Any()).Return(nil)
	m.mockHealthChecker.EXPECT().TaintsRemoved().Return(false)
	m.mockHealthChecker.EXPECT().RemoveTaints(gomock.Any()).Return(nil)
}

var _ = Describe("NodeNetworkConfigReconciler", func() {
	var (
		mockCtrl   *gomock.Controller
		fakeClient client.Client
		configPath string
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		configPath = filepath.Join(tmpPath, "config.yaml")
		// Clean up config file before each test
		_ = os.Remove(configPath)
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("ReadNodeNetworkConfig", func() {
		It("should read a valid config file", func() {
			cfg := createTestNodeNetworkConfig("1")
			Expect(saveNodeNetworkConfigToFile(cfg, configPath)).To(Succeed())

			readCfg, err := ReadNodeNetworkConfig(configPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(readCfg.Name).To(Equal(testNodeName))
			Expect(readCfg.Spec.Revision).To(Equal("1"))
		})

		It("should return error for non-existent file", func() {
			_, err := ReadNodeNetworkConfig("/non/existent/path")
			Expect(err).To(HaveOccurred())
		})

		It("should return error for invalid JSON", func() {
			invalidPath := filepath.Join(tmpPath, "invalid.yaml")
			Expect(os.WriteFile(invalidPath, []byte("invalid json"), NodeNetworkConfigFilePerm)).To(Succeed())

			_, err := ReadNodeNetworkConfig(invalidPath)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("SetStatus", func() {
		It("should update status to provisioning", func() {
			cfg := createTestNodeNetworkConfig("1")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				WithStatusSubresource(cfg).
				Build()

			err := SetStatus(context.Background(), fakeClient, cfg, operator.StatusProvisioning, logger)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.Status.ConfigStatus).To(Equal(operator.StatusProvisioning))
		})

		It("should update status to provisioned and set LastAppliedRevision", func() {
			cfg := createTestNodeNetworkConfig("5")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				WithStatusSubresource(cfg).
				Build()

			err := SetStatus(context.Background(), fakeClient, cfg, operator.StatusProvisioned, logger)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.Status.ConfigStatus).To(Equal(operator.StatusProvisioned))
			Expect(cfg.Status.LastAppliedRevision).To(Equal("5"))
		})

		It("should update status to invalid and set LastAppliedRevision", func() {
			cfg := createTestNodeNetworkConfig("3")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				WithStatusSubresource(cfg).
				Build()

			err := SetStatus(context.Background(), fakeClient, cfg, operator.StatusInvalid, logger)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
			Expect(cfg.Status.LastAppliedRevision).To(Equal("3"))
		})
	})

	Context("storeConfig", func() {
		It("should store config to file and update in-memory config", func() {
			cfg := createTestNodeNetworkConfig("1")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

			err := r.storeConfig(cfg, configPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(r.NodeNetworkConfig).To(Equal(cfg))

			// Verify file was written
			readCfg, err := ReadNodeNetworkConfig(configPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(readCfg.Spec.Revision).To(Equal("1"))
		})
	})

	Context("doReconciliation", func() {
		It("should call ApplyConfig on the config applier", func() {
			cfg := createTestNodeNetworkConfig("1")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

			r.mockApplier.EXPECT().
				ApplyConfig(gomock.Any(), cfg).
				Return(nil)

			err := r.doReconciliation(context.Background(), cfg)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return error when ApplyConfig fails", func() {
			cfg := createTestNodeNetworkConfig("1")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

			expectedErr := errors.New("apply config failed")
			r.mockApplier.EXPECT().
				ApplyConfig(gomock.Any(), cfg).
				Return(expectedErr)

			err := r.doReconciliation(context.Background(), cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("apply config failed"))
		})
	})

	Context("checkHealth", func() {
		It("should pass when all health checks succeed", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.setupHealthyHealthCheck()

			err := r.checkHealth(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})

		It("should remove taints when TaintsRemoved returns false", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.setupHealthyHealthCheckWithTaintRemoval()

			err := r.checkHealth(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return error when CheckInterfaces fails", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.mockHealthChecker.EXPECT().CheckInterfaces().Return(errors.New("interface check failed"))
			r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, gomock.Any()).Return(nil)

			err := r.checkHealth(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("interface check failed"))
		})

		It("should return error when CheckReachability fails", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
			r.mockHealthChecker.EXPECT().CheckReachability().Return(errors.New("reachability check failed"))
			r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, gomock.Any()).Return(nil)

			err := r.checkHealth(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reachability check failed"))
		})

		It("should return error when CheckAPIServer fails", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
			r.mockHealthChecker.EXPECT().CheckReachability().Return(nil)
			r.mockHealthChecker.EXPECT().CheckAPIServer(gomock.Any()).Return(errors.New("api server check failed"))
			r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonAPIServerFailed, gomock.Any()).Return(nil)

			err := r.checkHealth(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("api server check failed"))
		})

		It("should return error when RemoveTaints fails", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
			r.mockHealthChecker.EXPECT().CheckReachability().Return(nil)
			r.mockHealthChecker.EXPECT().CheckAPIServer(gomock.Any()).Return(nil)
			r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, gomock.Any()).Return(nil)
			r.mockHealthChecker.EXPECT().TaintsRemoved().Return(false)
			r.mockHealthChecker.EXPECT().RemoveTaints(gomock.Any()).Return(errors.New("remove taints failed"))

			err := r.checkHealth(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("remove taints failed"))
		})
	})

	Context("restoreNodeNetworkConfig", func() {
		It("should do nothing if NodeNetworkConfig is nil", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.NodeNetworkConfig = nil

			err := r.restoreNodeNetworkConfig(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})

		It("should call ApplyConfig with stored config", func() {
			storedCfg := createTestNodeNetworkConfig("1")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.NodeNetworkConfig = storedCfg

			r.mockApplier.EXPECT().
				ApplyConfig(gomock.Any(), storedCfg).
				Return(nil)

			err := r.restoreNodeNetworkConfig(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("invalidateAndRestore", func() {
		It("should invalidate config and restore previous one", func() {
			currentCfg := createTestNodeNetworkConfig("2")
			storedCfg := createTestNodeNetworkConfig("1")

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(currentCfg).
				WithStatusSubresource(currentCfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.NodeNetworkConfig = storedCfg

			// Expect restore to be called with stored config
			r.mockApplier.EXPECT().
				ApplyConfig(gomock.Any(), storedCfg).
				Return(nil)

			err := r.invalidateAndRestore(context.Background(), currentCfg, "test reason")
			Expect(err).ToNot(HaveOccurred())
			Expect(currentCfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
		})
	})

	Context("processConfig", func() {
		Context("successful reconciliation", func() {
			It("should complete successfully with healthy node", func() {
				cfg := createTestNodeNetworkConfig("1")
				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(cfg).
					WithStatusSubresource(cfg).
					Build()

				r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

				// Expect ApplyConfig to succeed
				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), cfg).
					Return(nil)

				// Expect health checks to pass
				r.setupHealthyHealthCheck()

				err := r.processConfig(context.Background(), cfg)
				Expect(err).ToNot(HaveOccurred())
				Expect(cfg.Status.ConfigStatus).To(Equal(operator.StatusProvisioned))
			})

			It("should remove taints on first successful reconciliation", func() {
				cfg := createTestNodeNetworkConfig("1")
				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(cfg).
					WithStatusSubresource(cfg).
					Build()

				r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), cfg).
					Return(nil)

				r.setupHealthyHealthCheckWithTaintRemoval()

				err := r.processConfig(context.Background(), cfg)
				Expect(err).ToNot(HaveOccurred())
				Expect(cfg.Status.ConfigStatus).To(Equal(operator.StatusProvisioned))
			})
		})

		Context("when health check fails after successful reconciliation", func() {
			It("should invalidate and restore previous config", func() {
				currentCfg := createTestNodeNetworkConfig("2")
				storedCfg := createTestNodeNetworkConfig("1")

				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(currentCfg).
					WithStatusSubresource(currentCfg).
					Build()

				r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{
					RestoreOnReconcileFailure: true,
				})
				r.NodeNetworkConfig = storedCfg

				// ApplyConfig succeeds for new config
				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), currentCfg).
					Return(nil)

				// Health check fails
				r.mockHealthChecker.EXPECT().CheckInterfaces().Return(errors.New("interface down"))
				r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, gomock.Any()).Return(nil)

				// Restore previous config
				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), storedCfg).
					Return(nil)

				err := r.processConfig(context.Background(), currentCfg)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("healthcheck error"))
				Expect(currentCfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
			})

			It("should invalidate and restore when reachability check fails", func() {
				currentCfg := createTestNodeNetworkConfig("2")
				storedCfg := createTestNodeNetworkConfig("1")

				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(currentCfg).
					WithStatusSubresource(currentCfg).
					Build()

				r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
				r.NodeNetworkConfig = storedCfg

				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), currentCfg).
					Return(nil)

				r.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
				r.mockHealthChecker.EXPECT().CheckReachability().Return(errors.New("cannot reach gateway"))
				r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonReachabilityFailed, gomock.Any()).Return(nil)

				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), storedCfg).
					Return(nil)

				err := r.processConfig(context.Background(), currentCfg)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("healthcheck error"))
				Expect(currentCfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
			})

			It("should invalidate and restore when API server check fails", func() {
				currentCfg := createTestNodeNetworkConfig("2")
				storedCfg := createTestNodeNetworkConfig("1")

				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(currentCfg).
					WithStatusSubresource(currentCfg).
					Build()

				r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
				r.NodeNetworkConfig = storedCfg

				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), currentCfg).
					Return(nil)

				r.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
				r.mockHealthChecker.EXPECT().CheckReachability().Return(nil)
				r.mockHealthChecker.EXPECT().CheckAPIServer(gomock.Any()).Return(errors.New("api server unreachable"))
				r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonAPIServerFailed, gomock.Any()).Return(nil)

				r.mockApplier.EXPECT().
					ApplyConfig(gomock.Any(), storedCfg).
					Return(nil)

				err := r.processConfig(context.Background(), currentCfg)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("healthcheck error"))
				Expect(currentCfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
			})
		})

		Context("RestoreOnReconcileFailure behavior", func() {
			Context("when RestoreOnReconcileFailure is true (FRR-like behavior)", func() {
				It("should restore previous config when reconciliation fails", func() {
					currentCfg := createTestNodeNetworkConfig("2")
					storedCfg := createTestNodeNetworkConfig("1")

					fakeClient = fake.NewClientBuilder().
						WithScheme(scheme).
						WithRuntimeObjects(currentCfg).
						WithStatusSubresource(currentCfg).
						Build()

					r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{
						RestoreOnReconcileFailure: true,
					})
					r.NodeNetworkConfig = storedCfg

					// First call fails (applying new config)
					r.mockApplier.EXPECT().
						ApplyConfig(gomock.Any(), currentCfg).
						Return(errors.New("reconciliation failed"))

					// Second call succeeds (restoring previous config)
					r.mockApplier.EXPECT().
						ApplyConfig(gomock.Any(), storedCfg).
						Return(nil)

					err := r.processConfig(context.Background(), currentCfg)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("reconciler error"))
					Expect(currentCfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
				})
			})

			Context("when RestoreOnReconcileFailure is false (VSR-like behavior)", func() {
				It("should only invalidate without restoring when reconciliation fails", func() {
					currentCfg := createTestNodeNetworkConfig("2")
					storedCfg := createTestNodeNetworkConfig("1")

					fakeClient = fake.NewClientBuilder().
						WithScheme(scheme).
						WithRuntimeObjects(currentCfg).
						WithStatusSubresource(currentCfg).
						Build()

					r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{
						RestoreOnReconcileFailure: false,
					})
					r.NodeNetworkConfig = storedCfg

					// Only one call - the failing one. No restore should happen.
					r.mockApplier.EXPECT().
						ApplyConfig(gomock.Any(), currentCfg).
						Return(errors.New("reconciliation failed"))

					err := r.processConfig(context.Background(), currentCfg)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("reconciler error"))
					Expect(currentCfg.Status.ConfigStatus).To(Equal(operator.StatusInvalid))
				})
			})
		})
	})

	Context("fetchNodeConfig", func() {
		It("should fetch config from API server", func() {
			cfg := createTestNodeNetworkConfig("1")
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

			fetchedCfg, err := r.fetchNodeConfig(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(fetchedCfg.Name).To(Equal(testNodeName))
			Expect(fetchedCfg.Spec.Revision).To(Equal("1"))
		})

		It("should return error when config not found", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})

			_, err := r.fetchNodeConfig(context.Background())
			Expect(err).To(HaveOccurred())
		})
	})

	Context("UpdateReadinessCondition edge cases", func() {
		It("should continue processing even if UpdateReadinessCondition fails on success path", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.mockHealthChecker.EXPECT().CheckInterfaces().Return(nil)
			r.mockHealthChecker.EXPECT().CheckReachability().Return(nil)
			r.mockHealthChecker.EXPECT().CheckAPIServer(gomock.Any()).Return(nil)
			// UpdateReadinessCondition fails, but should just log the error
			r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionTrue, healthcheck.ReasonHealthChecksPassed, gomock.Any()).Return(errors.New("update condition failed"))
			r.mockHealthChecker.EXPECT().TaintsRemoved().Return(true)

			err := r.checkHealth(context.Background())
			// Should still succeed even though updating condition failed
			Expect(err).ToNot(HaveOccurred())
		})

		It("should still return error when health check fails even if UpdateReadinessCondition also fails", func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.mockHealthChecker.EXPECT().CheckInterfaces().Return(errors.New("interface down"))
			// UpdateReadinessCondition also fails
			r.mockHealthChecker.EXPECT().UpdateReadinessCondition(gomock.Any(), corev1.ConditionFalse, healthcheck.ReasonInterfaceCheckFailed, gomock.Any()).Return(errors.New("update condition failed"))

			err := r.checkHealth(context.Background())
			// Should still return the original health check error
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("interface down"))
		})
	})

	Context("Reconcile edge cases", func() {
		It("should skip invalid NodeNetworkConfig with same revision", func() {
			cfg := createTestNodeNetworkConfig("1")
			cfg.Status.ConfigStatus = operator.StatusInvalid
			cfg.Status.LastAppliedRevision = "1"

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				WithStatusSubresource(cfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.NodeNetworkConfig = nil // No stored config

			err := r.Reconcile(context.Background())
			Expect(err).ToNot(HaveOccurred())
			// Should not have called any mocks since it skipped the invalid config
		})

		It("should process new revision even if previous was invalid", func() {
			// Config on API server with new revision
			cfg := createTestNodeNetworkConfig("2")
			cfg.Status.ConfigStatus = operator.StatusInvalid
			cfg.Status.LastAppliedRevision = "1" // Different from current revision

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(cfg).
				WithStatusSubresource(cfg).
				Build()

			r := newMockReconciler(mockCtrl, fakeClient, configPath, ReconcilerOptions{})
			r.NodeNetworkConfig = nil

			// Should process the config since revision is different
			r.mockApplier.EXPECT().ApplyConfig(gomock.Any(), gomock.Any()).Return(nil)
			r.setupHealthyHealthCheck()

			err := r.Reconcile(context.Background())
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
