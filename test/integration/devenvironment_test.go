//go:build integration

/*
Copyright 2026 Zach Perkins.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package integration drives the reconciler against a real cluster (K3D by
// default). Unlike envtest, a real cluster runs kube-controller-manager and a
// scheduler, so these tests can assert on things that only happen there:
// pods actually becoming ready, PVCs binding, and namespace teardown finishing.
//
// Build-tagged so `go test ./...` stays cluster-free. Run with:
//
//	make test-integration
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	devenvv1alpha1 "github.com/partofaplan/kado-operator/api/v1alpha1"
	"github.com/partofaplan/kado-operator/internal/controller"
)

var k8sClient client.Client

// TestMain starts the manager in-process against the cluster in the ambient
// kubeconfig, so the suite exercises the same reconciler that ships in the
// image without needing the image to be deployed first.
func TestMain(m *testing.M) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "no cluster available (is the K3D cluster running?): %v\n", err)
		os.Exit(1)
	}

	scheme := clientgoscheme.Scheme
	if err := devenvv1alpha1.AddToScheme(scheme); err != nil {
		fmt.Fprintf(os.Stderr, "registering scheme: %v\n", err)
		os.Exit(1)
	}

	mgr, err := manager.New(cfg, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating manager: %v\n", err)
		os.Exit(1)
	}

	reconciler := &controller.DevEnvironmentReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		// Must match cmd/main.go. Without it reader() falls back to the cached
		// client, which starts a cluster-wide pod informer inside Reconcile —
		// the exact thing the uncached read exists to avoid, and it would mean
		// the one suite that runs against a real cluster proved the wrong path.
		APIReader: mgr.GetAPIReader(),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "wiring controller: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "manager stopped: %v\n", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		fmt.Fprintln(os.Stderr, "cache never synced")
		cancel()
		os.Exit(1)
	}
	k8sClient = mgr.GetClient()

	code := m.Run()
	cancel()
	os.Exit(code)
}

// eventually polls until check passes or the deadline expires. The real
// scheduler and kubelet make timings unpredictable, so every assertion about
// cluster state has to be retried rather than read once.
func eventually(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = check(); last == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s: %v", timeout, what, last)
}

func TestEnvironmentLifecycle(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("itest-%d", time.Now().Unix())

	env := &devenvv1alpha1.DevEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: devenvv1alpha1.DevEnvironmentSpec{
			Owner:   "integration",
			Config:  map[string]string{"LOG_LEVEL": "debug"},
			Storage: &devenvv1alpha1.StorageSpec{Size: resource.MustParse("128Mi")},
			Services: []devenvv1alpha1.ServiceSpec{{
				Name:      "redis",
				Image:     "redis:7-alpine",
				Port:      6379,
				MountPath: "/data",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("25m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
				},
			}},
		},
	}

	require(t, k8sClient.Create(ctx, env), "creating DevEnvironment")
	t.Cleanup(func() {
		var leftover devenvv1alpha1.DevEnvironment
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(env), &leftover); err == nil {
			_ = k8sClient.Delete(ctx, &leftover)
		}
	})

	t.Run("provisions the namespace and its resources", func(t *testing.T) {
		eventually(t, 60*time.Second, "namespace to appear", func() error {
			var ns corev1.Namespace
			return k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)
		})
		eventually(t, 60*time.Second, "config map to appear", func() error {
			var cm corev1.ConfigMap
			return k8sClient.Get(ctx, types.NamespacedName{Name: name + "-config", Namespace: name}, &cm)
		})
		eventually(t, 60*time.Second, "deployment to appear", func() error {
			var d appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{Name: "redis", Namespace: name}, &d)
		})
	})

	t.Run("binds the shared volume", func(t *testing.T) {
		eventually(t, 120*time.Second, "pvc to bind", func() error {
			var pvc corev1.PersistentVolumeClaim
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name + "-workspace", Namespace: name}, &pvc); err != nil {
				return err
			}
			if pvc.Status.Phase != corev1.ClaimBound {
				return fmt.Errorf("pvc phase is %s", pvc.Status.Phase)
			}
			return nil
		})
	})

	t.Run("reaches Ready once pods start", func(t *testing.T) {
		eventually(t, 180*time.Second, "environment to become Ready", func() error {
			var got devenvv1alpha1.DevEnvironment
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(env), &got); err != nil {
				return err
			}
			if got.Status.Phase != devenvv1alpha1.PhaseReady {
				return fmt.Errorf("phase is %q, %d/%d services ready",
					got.Status.Phase, got.Status.ReadyServices, got.Status.TotalServices)
			}
			return nil
		})
	})

	t.Run("cleans up the namespace on delete", func(t *testing.T) {
		var got devenvv1alpha1.DevEnvironment
		require(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(env), &got), "re-reading environment")
		require(t, k8sClient.Delete(ctx, &got), "deleting environment")

		eventually(t, 180*time.Second, "namespace to be reclaimed", func() error {
			var ns corev1.Namespace
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("namespace still present (phase %s)", ns.Status.Phase)
		})

		eventually(t, 60*time.Second, "finalizer to be released", func() error {
			var leftover devenvv1alpha1.DevEnvironment
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(env), &leftover)
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("DevEnvironment still present with finalizers %v", leftover.Finalizers)
		})
	})
}

func require(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
