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

package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1alpha1 "github.com/partofaplan/kado-operator/api/v1alpha1"
)

// These specs run against a real API server (envtest), so they exercise the
// generated CRD schema — defaults, validation and immutability — which the
// fake client in the unit tests cannot check.
//
// envtest runs no kube-controller-manager, so nothing ever becomes Ready and
// namespace deletion never completes. Readiness and teardown are covered by the
// unit tests and by the K3D integration suite instead.
var _ = Describe("DevEnvironment", func() {
	var (
		reconciler *DevEnvironmentReconciler
		counter    int
	)

	BeforeEach(func() {
		reconciler = &DevEnvironmentReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		counter++
	})

	// uniqueName keeps specs from colliding, since envtest namespaces cannot be
	// reclaimed between specs.
	uniqueName := func() string { return fmt.Sprintf("env-%d", counter) }

	newResource := func(name string) *devenvv1alpha1.DevEnvironment {
		return &devenvv1alpha1.DevEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: devenvv1alpha1.DevEnvironmentSpec{
				Owner: "platform",
				Services: []devenvv1alpha1.ServiceSpec{{
					Name:  "redis",
					Image: "redis:7-alpine",
					Port:  6379,
				}},
			},
		}
	}

	// Server-side defaulting is the whole reason this lives in envtest. The API
	// server fills in timeoutSeconds, periodSeconds, successThreshold and
	// failureThreshold on a probe — and path/scheme on an httpGet — none of
	// which the operator sends. If the operator compared what it built against
	// what came back, every reconcile would rewrite the Deployment and roll the
	// pods. The fake client cannot catch that: it applies no defaults and never
	// maintains Generation, so the equivalent unit test compares 0 to 0.
	//
	// resourceVersion is the assertion that actually bites: it changes on any
	// write, including one that stores identical content.
	DescribeTable("does not rewrite the Deployment on repeated reconciles",
		func(probe *corev1.Probe) {
			name := uniqueName()
			env := newResource(name)
			env.Spec.Services[0].ReadinessProbe = probe
			Expect(k8sClient.Create(ctx, env)).To(Succeed())

			key := types.NamespacedName{Name: name, Namespace: "default"}
			Eventually(func() error {
				_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
				return err
			}).Should(Succeed())

			deployKey := types.NamespacedName{Name: "redis", Namespace: name}
			var first appsv1.Deployment
			Expect(k8sClient.Get(ctx, deployKey, &first)).To(Succeed())
			rendered := first.Spec.Template.Spec.Containers[0].ReadinessProbe
			if probe == nil {
				Expect(rendered).To(BeNil(), "no probe requested, none should be rendered")
			} else {
				Expect(rendered).NotTo(BeNil())
				// Proves the API server really did default fields the operator
				// never sets — otherwise this spec would be testing nothing.
				Expect(rendered.PeriodSeconds).To(BeNumerically(">", 0))
				Expect(rendered.TimeoutSeconds).To(BeNumerically(">", 0))
			}

			for i := 0; i < 3; i++ {
				_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
			}

			var last appsv1.Deployment
			Expect(k8sClient.Get(ctx, deployKey, &last)).To(Succeed())
			Expect(last.ResourceVersion).To(Equal(first.ResourceVersion),
				"a reconcile that changes nothing must not write the Deployment")
			Expect(last.Generation).To(Equal(first.Generation),
				"a write to spec would roll the pods")
		},
		Entry("empty handler, defaulted to TCP", &corev1.Probe{}),
		Entry("explicit httpGet, where the API server also defaults path and scheme",
			&corev1.Probe{ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Port: intstrFromInt32(6379)},
			}}),
		Entry("no probe at all", nil),
	)

	// The registry pattern is enforced by the API server, not by the
	// reconciler, so it has to be exercised against a real one. A regex that
	// silently accepts a scheme would produce `https://ghcr.io/redis:7` as an
	// image reference and fail at pull time instead of at apply time.
	DescribeTable("rejects a malformed registry at admission",
		func(registry string) {
			// Asserted on both fields, because they carry the same pattern and
			// nothing but a test would notice if one of them lost it.
			env := newResource(uniqueName())
			env.Spec.Registry = registry
			// MatchError, not a bare NotTo(Succeed()): without it an entry that
			// failed for an unrelated reason — a name collision, some other
			// invalid field — would pass silently and assert nothing.
			Expect(k8sClient.Create(ctx, env)).To(MatchError(ContainSubstring("spec.registry")))

			svcEnv := newResource(uniqueName())
			svcEnv.Spec.Services[0].Registry = registry
			Expect(k8sClient.Create(ctx, svcEnv)).To(MatchError(ContainSubstring("registry")))
		},
		Entry("a scheme", "https://ghcr.io"),
		Entry("a trailing slash", "ghcr.io/myorg/"),
		Entry("uppercase", "GHCR.io"),
		Entry("a leading slash", "/ghcr.io"),
		Entry("a space", "ghcr.io /myorg"),
		Entry("an empty path segment", "ghcr.io//myorg"),
		// The pattern once bounded the port to five digits, which let these
		// through to fail at pull time instead (#21).
		Entry("port zero", "registry.internal:0"),
		Entry("a port above the range", "registry.internal:65536"),
		Entry("a five-digit port above the range", "ghcr.io:99999"),
		Entry("a zero-padded port", "registry.internal:0080"),
		Entry("an empty port", "registry.internal:"),
	)

	DescribeTable("accepts a well-formed registry",
		func(registry string) {
			env := newResource(uniqueName())
			env.Spec.Registry = registry
			env.Spec.Services[0].Registry = registry
			Expect(k8sClient.Create(ctx, env)).To(Succeed())
		},
		Entry("a bare host", "ghcr.io"),
		Entry("a host and namespace", "ghcr.io/myorg"),
		Entry("a host, port and path", "registry.internal:5000/mirror"),
		Entry("localhost with a port", "localhost:5000"),
		Entry("a deep path", "ghcr.io/myorg/team/sub"),
		Entry("the lowest valid port", "registry.internal:1"),
		Entry("the highest valid port", "registry.internal:65535"),
	)

	// Unstructured on purpose. An explicit empty string is how a templating
	// tool spells "unset" — `registry: {{ .Values.registry }}` with no value —
	// but the typed client cannot express it: `omitempty` drops the field
	// before it is serialised, so the API server never sees it and the pattern
	// never runs. Only a raw object reaches the validation this asserts.
	It("accepts an explicitly empty registry, as a templated manifest sends it", func() {
		name := uniqueName()
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": devenvv1alpha1.GroupVersion.String(),
			"kind":       "DevEnvironment",
			"metadata":   map[string]interface{}{"name": name, "namespace": "default"},
			"spec": map[string]interface{}{
				"owner":    "platform",
				"registry": "",
				"services": []interface{}{map[string]interface{}{
					"name": "redis", "image": "redis:7-alpine",
					"port": int64(6379), "registry": "",
				}},
			},
		}}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("provisions a namespace, deployment and service", func() {
		name := uniqueName()
		env := newResource(name)
		env.Spec.Storage = &devenvv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())

		key := types.NamespacedName{Name: name, Namespace: "default"}
		Eventually(func() error {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			return err
		}).Should(Succeed())

		var ns corev1.Namespace
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)).To(Succeed())
		Expect(ns.Labels).To(HaveKeyWithValue(labelManagedBy, managerName))

		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "redis", Namespace: name}, &deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal("redis:7-alpine"))

		var svc corev1.Service
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "redis", Namespace: name}, &svc)).To(Succeed())
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(6379)))

		var pvc corev1.PersistentVolumeClaim
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-workspace", Namespace: name}, &pvc)).To(Succeed())

		var got devenvv1alpha1.DevEnvironment
		Expect(k8sClient.Get(ctx, key, &got)).To(Succeed())
		Expect(got.Status.Namespace).To(Equal(name))
		Expect(got.Status.TotalServices).To(Equal(int32(1)))
	})

	It("defaults replicas to 1", func() {
		env := newResource(uniqueName())
		Expect(k8sClient.Create(ctx, env)).To(Succeed())

		var got devenvv1alpha1.DevEnvironment
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), &got)).To(Succeed())
		Expect(got.Spec.Services[0].Replicas).NotTo(BeNil())
		Expect(*got.Spec.Services[0].Replicas).To(Equal(int32(1)))
	})

	It("rejects a service name that is not a DNS label", func() {
		env := newResource(uniqueName())
		env.Spec.Services[0].Name = "Not A Label"
		Expect(k8sClient.Create(ctx, env)).NotTo(Succeed())
	})

	It("rejects a port outside the valid range", func() {
		env := newResource(uniqueName())
		env.Spec.Services[0].Port = 70000
		Expect(k8sClient.Create(ctx, env)).NotTo(Succeed())
	})

	It("rejects an empty image", func() {
		env := newResource(uniqueName())
		env.Spec.Services[0].Image = ""
		Expect(k8sClient.Create(ctx, env)).NotTo(Succeed())
	})

	It("accepts imagePullSecrets at both levels", func() {
		env := newResource(uniqueName())
		env.Spec.ImagePullSecrets = []string{"env-creds"}
		env.Spec.Services[0].ImagePullSecrets = []string{"svc.creds-1"}
		Expect(k8sClient.Create(ctx, env)).To(Succeed())
	})

	It("rejects an imagePullSecret name that is not a DNS subdomain", func() {
		// Caught at apply time rather than becoming an unresolvable reference
		// on the pod.
		env := newResource(uniqueName())
		env.Spec.ImagePullSecrets = []string{"Not A Name"}
		Expect(k8sClient.Create(ctx, env)).NotTo(Succeed())
	})

	It("rejects a per-service imagePullSecret name that is not a DNS subdomain", func() {
		env := newResource(uniqueName())
		env.Spec.Services[0].ImagePullSecrets = []string{"UPPER"}
		Expect(k8sClient.Create(ctx, env)).NotTo(Succeed())
	})

	It("refuses to change namespaceName after creation", func() {
		name := uniqueName()
		env := newResource(name)
		env.Spec.NamespaceName = name
		Expect(k8sClient.Create(ctx, env)).To(Succeed())

		var got devenvv1alpha1.DevEnvironment
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(env), &got)).To(Succeed())
		got.Spec.NamespaceName = "somewhere-else"
		err := k8sClient.Update(ctx, &got)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("namespaceName is immutable"))
	})

	It("rejects two services sharing a name", func() {
		env := newResource(uniqueName())
		env.Spec.Services = append(env.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name: "redis", Image: "redis:7-alpine", Port: 6379,
		})
		Expect(k8sClient.Create(ctx, env)).NotTo(Succeed())
	})
})
