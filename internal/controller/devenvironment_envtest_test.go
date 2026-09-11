/*
Copyright 2026.

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
