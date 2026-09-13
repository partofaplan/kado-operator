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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	devenvv1alpha1 "github.com/partofaplan/kado-operator/api/v1alpha1"
)

const (
	envName  = "team-alpha"
	envNS    = "default"
	targetNS = "team-alpha"
	// Shared by the storage specs below; goconst flags the repeated literal.
	testMountPath = "/data"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, devenvv1alpha1.AddToScheme(s))
	return s
}

// newReconciler wires a reconciler over a fake client seeded with objs. The
// DevEnvironment status subresource is registered so Status().Update() behaves
// as it does against a real API server.
func newReconciler(t *testing.T, objs ...client.Object) (*DevEnvironmentReconciler, client.Client) {
	t.Helper()
	s := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&devenvv1alpha1.DevEnvironment{}).
		Build()
	// APIReader is wired so the nil-fallback in reader() is not the only path
	// the tests ever exercise; production passes mgr.GetAPIReader().
	return &DevEnvironmentReconciler{Client: c, Scheme: s, APIReader: c}, c
}

func newEnv(mutators ...func(*devenvv1alpha1.DevEnvironment)) *devenvv1alpha1.DevEnvironment {
	env := &devenvv1alpha1.DevEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: envNS},
		Spec: devenvv1alpha1.DevEnvironmentSpec{
			Owner:  "team-alpha",
			Config: map[string]string{"LOG_LEVEL": "debug"},
			Services: []devenvv1alpha1.ServiceSpec{{
				Name:  "redis",
				Image: "redis:7-alpine",
				Port:  6379,
			}},
		},
	}
	for _, m := range mutators {
		m(env)
	}
	return env
}

func request() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: envName, Namespace: envNS}}
}

// reconcile runs a single successful reconcile pass.
func reconcile(t *testing.T, r *DevEnvironmentReconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)
}

func TestReconcileAddsFinalizer(t *testing.T) {
	r, c := newReconciler(t, newEnv())
	reconcile(t, r)

	// Without the finalizer, deleting the record would orphan the namespace.
	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
	assert.True(t, controllerutil.ContainsFinalizer(&env, finalizerName))
}

func TestReconcileProvisionsEnvironment(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Storage = &devenvv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")}
		e.Spec.Services[0].MountPath = testMountPath
	}))
	reconcile(t, r)
	ctx := context.Background()

	var ns corev1.Namespace
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: targetNS}, &ns))
	assert.Equal(t, managerName, ns.Labels[labelManagedBy])
	assert.Equal(t, envName, ns.Labels[labelEnvironment])
	assert.Equal(t, envNS, ns.Labels[labelOwnerNamespace])
	assert.Equal(t, "team-alpha", ns.Labels[labelOwner])

	var cm corev1.ConfigMap
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: envName + "-config", Namespace: targetNS}, &cm))
	assert.Equal(t, "debug", cm.Data["LOG_LEVEL"])

	var pvc corev1.PersistentVolumeClaim
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: envName + "-workspace", Namespace: targetNS}, &pvc))
	assert.Equal(t, resource.MustParse("1Gi"), pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, pvc.Spec.AccessModes,
		"access modes should default to ReadWriteOnce")

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	container := deploy.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "redis:7-alpine", container.Image)
	assert.Equal(t, int32(6379), container.Ports[0].ContainerPort)
	require.Len(t, container.EnvFrom, 1)
	assert.Equal(t, envName+"-config", container.EnvFrom[0].ConfigMapRef.Name)
	require.Len(t, container.VolumeMounts, 1)
	assert.Equal(t, testMountPath, container.VolumeMounts[0].MountPath)
	require.Len(t, deploy.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, envName+"-workspace", deploy.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)

	var svc corev1.Service
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &svc))
	assert.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)
	assert.Equal(t, int32(6379), svc.Spec.Ports[0].Port)
	assert.Equal(t, map[string]string{labelEnvironment: envName, labelService: "redis"}, svc.Spec.Selector)
}

func TestReconcileSetsFSGroupOnlyOnServicesThatMountTheVolume(t *testing.T) {
	// fsGroup on a pod with no volume changes nothing, and setting it
	// everywhere would roll every service the first time anyone added the
	// field (#47).
	gid := int64(65534)
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Storage = &devenvv1alpha1.StorageSpec{
			Size:    resource.MustParse("1Gi"),
			FSGroup: &gid,
		}
		e.Spec.Services[0].MountPath = testMountPath
		e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name: "api", Image: "api:1", Port: 8080,
		})
	}))
	reconcile(t, r)
	ctx := context.Background()

	var mounts appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &mounts))
	require.NotNil(t, mounts.Spec.Template.Spec.SecurityContext)
	assert.Equal(t, &gid, mounts.Spec.Template.Spec.SecurityContext.FSGroup)

	var doesNot appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "api", Namespace: targetNS}, &doesNot))
	assert.Nil(t, doesNot.Spec.Template.Spec.SecurityContext,
		"a service that mounts nothing should not get a pod securityContext")
}

func TestReconcileLeavesSecurityContextUnsetWithoutFSGroup(t *testing.T) {
	// Asserts nil, which holds here only because the fake client does no
	// defaulting — a real API server stores `securityContext: {}`. The point
	// of the assertion is that we set no fsGroup, not that the stored form is
	// nil.
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Storage = &devenvv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")}
		e.Spec.Services[0].MountPath = testMountPath
	}))
	reconcile(t, r)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Nil(t, deploy.Spec.Template.Spec.SecurityContext)
}

func TestReconcileSkipsPVCWhenNoStorageRequested(t *testing.T) {
	r, c := newReconciler(t, newEnv())
	reconcile(t, r)

	var pvc corev1.PersistentVolumeClaim
	err := c.Get(context.Background(), types.NamespacedName{Name: envName + "-workspace", Namespace: targetNS}, &pvc)
	assert.True(t, apierrors.IsNotFound(err), "expected no PVC, got %v", err)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Empty(t, deploy.Spec.Template.Spec.Volumes)
}

func TestReconcileCopiesReferencedSecrets(t *testing.T) {
	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: envNS},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"PASSWORD": []byte("hunter2")},
	}
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services[0].SecretRefs = []string{"db-creds"}
	}), src)
	reconcile(t, r)

	var copied corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "db-creds", Namespace: targetNS}, &copied))
	assert.Equal(t, []byte("hunter2"), copied.Data["PASSWORD"])
	assert.Equal(t, corev1.SecretTypeOpaque, copied.Type)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	envFrom := deploy.Spec.Template.Spec.Containers[0].EnvFrom
	require.Len(t, envFrom, 2)
	assert.Equal(t, "db-creds", envFrom[1].SecretRef.Name)
}

func TestReconcileFailsWhenReferencedSecretMissing(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services[0].SecretRefs = []string{"nope"}
	}))
	ctx := context.Background()

	_, err := r.Reconcile(ctx, request())
	require.Error(t, err)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseFailed, env.Status.Phase)
	assert.True(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionDegraded))
}

// dockerCfg builds a docker-registry Secret of the type the kubelet requires.
func dockerCfg(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envNS},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{".dockerconfigjson": []byte(`{"auths":{}}`)},
	}
}

func TestReconcileCopiesPullSecretAndReferencesItOnThePod(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"regcreds"}
	}), dockerCfg("regcreds"))
	reconcile(t, r)

	// Copied into the environment namespace with its type intact — a
	// dockerconfigjson secret copied as Opaque is ignored by the kubelet.
	var copied corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "regcreds", Namespace: targetNS}, &copied))
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, copied.Type)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Equal(t,
		[]corev1.LocalObjectReference{{Name: "regcreds"}},
		deploy.Spec.Template.Spec.ImagePullSecrets,
	)
}

func TestReconcileServicePullSecretsReplaceTheEnvironments(t *testing.T) {
	// Replace, not append — the same precedence registry uses.
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"env-creds"}
		e.Spec.Services[0].ImagePullSecrets = []string{"svc-creds"}
	}), dockerCfg("env-creds"), dockerCfg("svc-creds"))
	reconcile(t, r)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Equal(t,
		[]corev1.LocalObjectReference{{Name: "svc-creds"}},
		deploy.Spec.Template.Spec.ImagePullSecrets,
	)

	// The environment's secret is still copied even though this service does
	// not use it: another service may, and an unused copy is harmless.
	var copied corev1.Secret
	assert.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "env-creds", Namespace: targetNS}, &copied))
}

func TestReconcileTreatsAnEmptyServicePullSecretListAsInherit(t *testing.T) {
	// A templated manifest that renders `imagePullSecrets: []` must not
	// silently drop the environment's credentials into an ImagePullBackOff.
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"env-creds"}
		e.Spec.Services[0].ImagePullSecrets = []string{}
	}), dockerCfg("env-creds"))
	reconcile(t, r)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Equal(t,
		[]corev1.LocalObjectReference{{Name: "env-creds"}},
		deploy.Spec.Template.Spec.ImagePullSecrets,
	)
}

func TestReconcileLeavesPullSecretsUnsetWhenNoneAreNamed(t *testing.T) {
	// nil rather than an empty slice: an empty slice round-trips through the
	// API server as nil and would rewrite the pod template every reconcile.
	r, c := newReconciler(t, newEnv())
	reconcile(t, r)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Nil(t, deploy.Spec.Template.Spec.ImagePullSecrets)
}

func TestReconcileAcceptsALegacyDockercfgPullSecret(t *testing.T) {
	// The kubelet still honours the pre-1.9 format, so rejecting it would fail
	// the whole environment over a credential that would have worked.
	legacy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "old-creds", Namespace: envNS},
		Type:       corev1.SecretTypeDockercfg,
		Data:       map[string][]byte{".dockercfg": []byte(`{}`)},
	}
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"old-creds"}
	}), legacy)
	reconcile(t, r)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Equal(t,
		[]corev1.LocalObjectReference{{Name: "old-creds"}},
		deploy.Spec.Template.Spec.ImagePullSecrets,
	)
}

func TestReconcileRejectsAPullSecretOfTheWrongType(t *testing.T) {
	// An Opaque secret is accepted by the API server and then silently ignored
	// by the kubelet, so without this check the only symptom is an
	// ImagePullBackOff indistinguishable from a bad credential.
	opaque := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "regcreds", Namespace: envNS},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"PASSWORD": []byte("hunter2")},
	}
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"regcreds"}
	}), opaque)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, request())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes.io/dockerconfigjson")

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseFailed, env.Status.Phase)
	assert.True(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionDegraded))
}

func TestReconcileAcceptsASecretUsedForBothEnvFromAndPulling(t *testing.T) {
	// The same name in secretRefs and imagePullSecrets must be copied once and
	// still satisfy the pull-secret type check.
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"shared"}
		e.Spec.Services[0].SecretRefs = []string{"shared"}
	}), dockerCfg("shared"))
	reconcile(t, r)

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Equal(t,
		[]corev1.LocalObjectReference{{Name: "shared"}},
		deploy.Spec.Template.Spec.ImagePullSecrets,
	)
}

func TestReferencedPullSecretsAreDedupedAndSorted(t *testing.T) {
	env := newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"zulu", "alpha"}
		e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name:             "api",
			Image:            "api:1",
			Port:             8080,
			ImagePullSecrets: []string{"alpha", "mike"},
		})
	})
	assert.Equal(t, []string{"alpha", "mike", "zulu"}, referencedPullSecrets(env))
}

func TestReconcileRefusesToReplaceASecretItDoesNotOwn(t *testing.T) {
	// The environment namespace is somewhere developers deploy into, so a
	// same-named Secret from cert-manager or a person is possible. Replacing
	// the copy on a type change must never destroy one of those (#48).
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "creds",
			Namespace: targetNS,
			Labels:    map[string]string{"app": "something-else"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": []byte("theirs")},
	}
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.ImagePullSecrets = []string{"creds"}
	}), dockerCfg("creds"), foreign)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, request())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by this DevEnvironment")

	// Still there, untouched.
	var after corev1.Secret
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "creds", Namespace: targetNS}, &after))
	assert.Equal(t, corev1.SecretTypeTLS, after.Type)
	assert.Equal(t, []byte("theirs"), after.Data["tls.crt"])
}

func TestReplaceOnTypeChangeReadsThroughTheAPIReader(t *testing.T) {
	// In production Client reads through the manager's cache. Acting on a
	// stale copy would delete a Secret that is already correct, so the check
	// must go through reader(). Client and APIReader are seeded to disagree:
	// only a reader()-based lookup sees the current type.
	env := newEnv()
	sch := testScheme(t)

	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: targetNS},
		Type:       corev1.SecretTypeOpaque,
	}
	current := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: targetNS},
		Type:       corev1.SecretTypeDockerConfigJson,
	}
	r := &DevEnvironmentReconciler{
		Client:    fake.NewClientBuilder().WithScheme(sch).WithObjects(stale).Build(),
		Scheme:    sch,
		APIReader: fake.NewClientBuilder().WithScheme(sch).WithObjects(current).Build(),
	}

	replaced, err := r.replaceOnTypeChange(
		context.Background(), env, "creds", targetNS, corev1.SecretTypeDockerConfigJson,
	)
	require.NoError(t, err)
	assert.False(t, replaced, "should not replace: the API says the copy is already the right type")
}

func TestReconcileRefusesToAdoptUnmanagedNamespace(t *testing.T) {
	// A namespace someone else already owns must not be taken over, and above
	// all must not later be deleted by our finalizer.
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNS}}
	r, c := newReconciler(t, newEnv(), foreign)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, request())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by this DevEnvironment")

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseFailed, env.Status.Phase)

	var deploy appsv1.Deployment
	assert.True(t, apierrors.IsNotFound(
		c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy)),
		"nothing should be deployed into a namespace we do not own")
}

func TestReconcileReportsReadyServices(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name: "postgres", Image: "postgres:16-alpine", Port: 5432,
		})
	}))
	ctx := context.Background()
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseProvisioning, env.Status.Phase)
	assert.Equal(t, int32(0), env.Status.ReadyServices)
	assert.Equal(t, int32(2), env.Status.TotalServices)
	assert.Equal(t, targetNS, env.Status.Namespace)
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionAvailable))

	// Simulate the deployments coming up.
	for _, name := range []string{"redis", "postgres"} {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: targetNS}, &d))
		d.Status.ReadyReplicas = 1
		require.NoError(t, c.Status().Update(ctx, &d))
	}
	reconcile(t, r)

	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseReady, env.Status.Phase)
	assert.Equal(t, int32(2), env.Status.ReadyServices)
	assert.True(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionAvailable))
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionProgressing))
	assert.Equal(t, env.Generation, env.Status.ObservedGeneration)
}

func TestReconcilePrunesRemovedServices(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name: "postgres", Image: "postgres:16-alpine", Port: 5432,
		})
	}))
	ctx := context.Background()
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	env.Spec.Services = env.Spec.Services[:1] // drop postgres
	require.NoError(t, c.Update(ctx, &env))
	reconcile(t, r)

	var gone appsv1.Deployment
	assert.True(t, apierrors.IsNotFound(
		c.Get(ctx, types.NamespacedName{Name: "postgres", Namespace: targetNS}, &gone)))
	var goneSvc corev1.Service
	assert.True(t, apierrors.IsNotFound(
		c.Get(ctx, types.NamespacedName{Name: "postgres", Namespace: targetNS}, &goneSvc)))

	var kept appsv1.Deployment
	assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &kept))
}

func TestReconcileRemovesConfigMapWhenConfigCleared(t *testing.T) {
	r, c := newReconciler(t, newEnv())
	ctx := context.Background()
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	env.Spec.Config = nil
	require.NoError(t, c.Update(ctx, &env))
	reconcile(t, r)

	var cm corev1.ConfigMap
	assert.True(t, apierrors.IsNotFound(
		c.Get(ctx, types.NamespacedName{Name: envName + "-config", Namespace: targetNS}, &cm)))

	var deploy appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &deploy))
	assert.Empty(t, deploy.Spec.Template.Spec.Containers[0].EnvFrom)
}

func TestReconcileDeleteTearsDownNamespaceThenReleasesFinalizer(t *testing.T) {
	r, c := newReconciler(t, newEnv())
	ctx := context.Background()
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	require.NoError(t, c.Delete(ctx, &env))

	// First delete pass issues the namespace deletion and keeps the finalizer.
	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	var ns corev1.Namespace
	nsErr := c.Get(ctx, types.NamespacedName{Name: targetNS}, &ns)
	assert.True(t, apierrors.IsNotFound(nsErr), "namespace should be deleted")

	// Once the namespace is gone the finalizer is released and the record goes.
	_, err = r.Reconcile(ctx, request())
	require.NoError(t, err)
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, request().NamespacedName, &env)))
}

func TestReconcileDeleteLeavesUnmanagedNamespaceAlone(t *testing.T) {
	env := newEnv()
	env.Finalizers = []string{finalizerName}
	now := metav1.Now()
	env.DeletionTimestamp = &now
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNS}}
	r, c := newReconciler(t, env, foreign)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	var ns corev1.Namespace
	assert.NoError(t, c.Get(ctx, types.NamespacedName{Name: targetNS}, &ns),
		"a namespace we did not create must survive")
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, request().NamespacedName, env)),
		"finalizer should still be released so the record is not stuck")
}

func TestReconcileIsIdempotent(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services[0].Env = map[string]string{"B": "2", "A": "1", "C": "3"}
	}))
	ctx := context.Background()
	reconcile(t, r)

	var first appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &first))

	reconcile(t, r)

	var second appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &second))

	// Env comes from a map; unsorted iteration would churn the pod template and
	// trigger a rollout on every pass.
	assert.Equal(t, first.Spec.Template.Spec.Containers[0].Env, second.Spec.Template.Spec.Containers[0].Env)
	assert.Equal(t, []corev1.EnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}, {Name: "C", Value: "3"}},
		second.Spec.Template.Spec.Containers[0].Env)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion, "a no-op reconcile must not rewrite the deployment")
}

func TestReconcileMissingResourceIsNoOp(t *testing.T) {
	r, _ := newReconciler(t)
	res, err := r.Reconcile(context.Background(), request())
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res)
}

func TestTargetNamespaceDefaultsToResourceName(t *testing.T) {
	tests := []struct {
		name          string
		namespaceName string
		want          string
	}{
		{"defaults to the resource name", "", envName},
		{"honours an explicit namespace", "custom-ns", "custom-ns"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(func(e *devenvv1alpha1.DevEnvironment) { e.Spec.NamespaceName = tc.namespaceName })
			assert.Equal(t, tc.want, env.TargetNamespace())
		})
	}
}

func TestReferencedSecretsAreDedupedAndSorted(t *testing.T) {
	env := newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services = []devenvv1alpha1.ServiceSpec{
			{Name: "a", SecretRefs: []string{"zeta", "alpha"}},
			{Name: "b", SecretRefs: []string{"alpha"}},
		}
	})
	assert.Equal(t, []string{"alpha", "zeta"}, referencedSecrets(env))
}

// The controller cannot use Owns() for Deployments or Namespaces: they live
// outside the DevEnvironment's namespace and Kubernetes forbids a
// cross-namespace ownerReference, so the built-in owner watch never fires.
// These cases pin the label-based mapping that replaces it.
func TestEnvironmentFromLabelsMapsOwnedObjects(t *testing.T) {
	r := &DevEnvironmentReconciler{}
	owned := map[string]string{
		labelManagedBy:      managerName,
		labelEnvironment:    envName,
		labelOwnerNamespace: envNS,
		labelService:        "redis",
	}

	tests := []struct {
		name   string
		obj    client.Object
		expect []ctrl.Request
	}{
		{
			name:   "deployment in the environment namespace",
			obj:    &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: targetNS, Labels: owned}},
			expect: []ctrl.Request{request()},
		},
		{
			name:   "namespace itself",
			obj:    &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNS, Labels: owned}},
			expect: []ctrl.Request{request()},
		},
		{
			name: "object managed by something else",
			obj: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: targetNS, Labels: map[string]string{
				labelManagedBy: "helm", labelEnvironment: envName, labelOwnerNamespace: envNS,
			}}},
			expect: nil,
		},
		{
			name: "ours but missing the owner namespace label",
			obj: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "partial", Namespace: targetNS, Labels: map[string]string{
				labelManagedBy: managerName, labelEnvironment: envName,
			}}},
			expect: nil,
		},
		{
			name:   "unlabelled object",
			obj:    &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
			expect: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, r.mapToEnvironment(context.Background(), tc.obj))
		})
	}
}

// crashingPod builds a pod belonging to service in the environment namespace,
// stuck on reason with an optional previous exit code.
func crashingPod(service, reason string, exitCode *int32) *corev1.Pod {
	cs := corev1.ContainerStatus{
		Name:  service,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
	}
	if exitCode != nil {
		cs.LastTerminationState = corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: *exitCode},
		}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service + "-abc123",
			Namespace: targetNS,
			Labels: map[string]string{
				labelManagedBy:   managerName,
				labelEnvironment: envName,
				labelService:     service,
			},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

// A container that cannot start must surface as Degraded. Before this, Degraded
// was hardcoded false whenever the reconciler itself succeeded, so a pod in
// CrashLoopBackOff was indistinguishable from one still pulling its image.
func TestReconcileMarksDegradedWhenAContainerCannotStart(t *testing.T) {
	exit := int32(1)
	r, c := newReconciler(t,
		newEnv(func(e *devenvv1alpha1.DevEnvironment) {
			e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
				Name: "postgres", Image: "postgres:16-alpine", Port: 5432,
			})
		}),
		crashingPod("postgres", "CrashLoopBackOff", &exit),
	)
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))

	degraded := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
	require.NotNil(t, degraded)
	assert.Equal(t, metav1.ConditionTrue, degraded.Status)
	assert.Equal(t, "WorkloadUnhealthy", degraded.Reason)
	assert.Contains(t, degraded.Message, `service "postgres"`)
	assert.Contains(t, degraded.Message, "CrashLoopBackOff")
	assert.Contains(t, degraded.Message, "code 1")
	assert.Contains(t, degraded.Message, "kubectl logs")

	// Progressing must not simultaneously claim the environment is on its way.
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionProgressing))
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionAvailable))
}

// The inverse, and the reason the waiting reason is allow-listed: a pod that is
// merely still starting must not be reported as degraded, or the condition
// would fire on every normal provision.
func TestReconcileDoesNotMarkDegradedWhileContainersAreStillStarting(t *testing.T) {
	r, c := newReconciler(t, newEnv(), crashingPod("redis", "ContainerCreating", nil))
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))

	degraded := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
	require.NotNil(t, degraded)
	assert.Equal(t, metav1.ConditionFalse, degraded.Status)
	assert.Equal(t, "ReconcileSucceeded", degraded.Reason)
	assert.True(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionProgressing))
}

func TestReconcileReportsImagePullAndSchedulingFailures(t *testing.T) {
	t.Run("image pull carries the API server's own message", func(t *testing.T) {
		pod := crashingPod("redis", "ImagePullBackOff", nil)
		pod.Status.ContainerStatuses[0].State.Waiting.Message = `pull access denied for redis`
		r, c := newReconciler(t, newEnv(), pod)
		reconcile(t, r)

		var env devenvv1alpha1.DevEnvironment
		require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
		d := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
		require.NotNil(t, d)
		assert.Equal(t, metav1.ConditionTrue, d.Status)
		assert.Contains(t, d.Message, "ImagePullBackOff: pull access denied for redis")
	})

	t.Run("an unschedulable pod outranks container state", func(t *testing.T) {
		pod := crashingPod("redis", "ContainerCreating", nil)
		pod.Status.Conditions = []corev1.PodCondition{{
			Type:    corev1.PodScheduled,
			Status:  corev1.ConditionFalse,
			Reason:  corev1.PodReasonUnschedulable,
			Message: "0/3 nodes are available: insufficient cpu",
		}}
		r, c := newReconciler(t, newEnv(), pod)
		reconcile(t, r)

		var env devenvv1alpha1.DevEnvironment
		require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
		d := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
		require.NotNil(t, d)
		assert.Equal(t, metav1.ConditionTrue, d.Status)
		assert.Contains(t, d.Message, "Unschedulable: 0/3 nodes are available: insufficient cpu")
	})
}

// stalledDeployment is a Deployment the controller has given up progressing:
// the shape Kubernetes produces when no replica becomes ready in time. Named
// for newEnv's only service, which these fixtures pair with.
func stalledDeployment() *appsv1.Deployment {
	const svc = "redis"
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       svc,
			Namespace:  targetNS,
			Generation: 1,
			Labels: map[string]string{
				labelManagedBy:   managerName,
				labelEnvironment: envName,
				labelService:     svc,
			},
		},
		Status: appsv1.DeploymentStatus{
			// Status has caught up with spec; see the ObservedGeneration gate.
			ObservedGeneration: 1,
			Conditions: []appsv1.DeploymentCondition{{
				Type:    appsv1.DeploymentProgressing,
				Status:  corev1.ConditionFalse,
				Reason:  "ProgressDeadlineExceeded",
				Message: `ReplicaSet "redis-abc" has timed out progressing.`,
			}},
		},
	}
}

// runningNotReadyPod is the case #15 describes: nothing is wrong with the
// container, it just never answers its probe. No waiting reason, so the pod
// pass finds nothing to report.
func runningNotReadyPod() *corev1.Pod {
	const svc = "redis"
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc + "-pod",
			Namespace: targetNS,
			Labels: map[string]string{
				labelManagedBy:   managerName,
				labelEnvironment: envName,
				labelService:     svc,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  svc,
				Ready: false,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func TestReconcileReportsAServiceThatNeverBecomesReady(t *testing.T) {
	r, c := newReconciler(t, newEnv(),
		runningNotReadyPod(),
		stalledDeployment(),
	)
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))

	assert.True(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionDegraded),
		"a service stuck not-ready past the progress deadline should be degraded")
	cond := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, `service "redis"`)
	assert.Contains(t, cond.Message, "readinessProbe")
}

func TestReconcileIgnoresAStaleDeadlineFromThePreviousRollout(t *testing.T) {
	// The user has just fixed the probe, so spec moved and the Deployment
	// controller has not caught up. Its conditions still describe the rollout
	// that failed; reporting them would fire Degraded in the very reconcile
	// that applies the fix.
	stale := stalledDeployment()
	stale.Generation = 2
	stale.Status.ObservedGeneration = 1

	r, c := newReconciler(t, newEnv(), runningNotReadyPod(), stale)
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionDegraded),
		"a stale condition from the previous rollout must not report as current")
}

func TestStalledReportsReplicaFailureRatherThanGuessingAtTheProbe(t *testing.T) {
	// A ReplicaSet that cannot create pods at all trips the same deadline with
	// no pod to inspect. Claiming a readinessProbe problem there would bury
	// the real reason.
	blocked := stalledDeployment()
	blocked.Status.Conditions = append(blocked.Status.Conditions, appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentReplicaFailure,
		Status:  corev1.ConditionTrue,
		Reason:  "FailedCreate",
		Message: `pods "redis-" is forbidden: exceeded quota: dev-quota`,
	})

	r, c := newReconciler(t, newEnv(), blocked)
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
	cond := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "exceeded quota")
	assert.NotContains(t, cond.Message, "readinessProbe")
}

func TestReconcileDoesNotReportAServiceStillWithinItsDeadline(t *testing.T) {
	// The same not-ready pod, but the Deployment has not given up. This is an
	// ordinary slow start and must not read as degraded.
	progressing := stalledDeployment()
	progressing.Status.Conditions[0].Status = corev1.ConditionTrue
	progressing.Status.Conditions[0].Reason = "ReplicaSetUpdated"

	r, c := newReconciler(t, newEnv(), runningNotReadyPod(), progressing)
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionDegraded))
}

func TestStalledDeploymentDoesNotMaskAContainerProblem(t *testing.T) {
	// A crash loop trips the progress deadline too. "It crashed" explains more
	// than "it timed out", so the pod problem must win.
	crashing := runningNotReadyPod()
	crashing.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s"},
	}

	r, c := newReconciler(t, newEnv(), crashing, stalledDeployment())
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
	cond := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "CrashLoopBackOff")
	assert.NotContains(t, cond.Message, "Stalled")
}

// A terminating pod is on its way out by design — reporting it would make every
// rollout look degraded.
func TestWorkloadProblemsIgnoresTerminatingPods(t *testing.T) {
	exit := int32(1)
	pod := crashingPod("redis", "CrashLoopBackOff", &exit)
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	pod.Finalizers = []string{"kubernetes.io/test"} // fake client requires one to accept a deletionTimestamp

	r, _ := newReconciler(t, newEnv(), pod)
	assert.Empty(t, r.workloadProblems(context.Background(), newEnv(), targetNS))
}

// Once everything is ready the pods are not consulted at all, so a stale
// crashing pod from a previous revision cannot flip a healthy environment.
func TestReconcileDoesNotReportProblemsOnceAllServicesAreReady(t *testing.T) {
	exit := int32(1)
	r, c := newReconciler(t, newEnv(), crashingPod("redis", "CrashLoopBackOff", &exit))
	ctx := context.Background()
	reconcile(t, r)

	var d appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &d))
	d.Status.ReadyReplicas = 1
	require.NoError(t, c.Status().Update(ctx, &d))
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseReady, env.Status.Phase)
	assert.False(t, meta.IsStatusConditionTrue(env.Status.Conditions, devenvv1alpha1.ConditionDegraded))
}

// Without a requeue nothing brings the controller back once a Deployment's
// status settles, so a container that starts crashing after the last reconcile
// would never be reported. Verified on a live cluster before this was added:
// the operator logged nothing while a pod restarted six times.
func TestReconcileRequeuesWhileServicesAreNotReady(t *testing.T) {
	r, c := newReconciler(t, newEnv())
	ctx := context.Background()

	res, err := r.Reconcile(ctx, request())
	require.NoError(t, err)
	// A literal, not notReadyRequeue: asserting against the constant passes for
	// any value including zero, which is the bug this test exists to catch.
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "an unready environment must schedule its own re-examination")
	assert.Positive(t, res.RequeueAfter)

	var d appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &d))
	d.Status.ReadyReplicas = 1
	require.NoError(t, c.Status().Update(ctx, &d))

	res, err = r.Reconcile(ctx, request())
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "a ready environment must stop requeuing")
}

// A crash-looping container is Waiting only between restarts; when the backoff
// expires it is Running again until it dies. A reconcile landing in that window
// used to see nothing wrong and flip Degraded back to false.
func TestReconcileKeepsDegradedWhileAContainerIsRestarting(t *testing.T) {
	pod := crashingPod("redis", "CrashLoopBackOff", nil)
	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name:                 "redis",
		Ready:                false,
		RestartCount:         6,
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	r, c := newReconciler(t, newEnv(), pod)
	reconcile(t, r)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, &env))
	d := meta.FindStatusCondition(env.Status.Conditions, devenvv1alpha1.ConditionDegraded)
	require.NotNil(t, d)
	assert.Equal(t, metav1.ConditionTrue, d.Status, "a container that keeps dying must stay degraded between restarts")
	assert.Contains(t, d.Message, "restarting after failure")
	assert.Contains(t, d.Message, "6 restart(s)")
}

// The inverse guard: an environment is inspected whenever ANY service is
// unready, so a healthy service that restarted once during startup must not be
// reported alongside the one that is genuinely broken.
func TestWorkloadProblemsIgnoresAReadyContainerWithRestartHistory(t *testing.T) {
	pod := crashingPod("redis", "CrashLoopBackOff", nil)
	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name:                 "redis",
		Ready:                true,
		RestartCount:         3,
		State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	r, _ := newReconciler(t, newEnv(), pod)
	assert.Empty(t, r.workloadProblems(context.Background(), newEnv(), targetNS))
}

// An init container that never succeeds blocks the pod; the app container below
// it only ever reports ContainerCreating, which reads as "still starting".
func TestPodProblemReportsInitContainerFailures(t *testing.T) {
	pod := crashingPod("redis", "PodInitializing", nil)
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:                 "wait-for-db",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}},
	}}
	got := podProblem(pod)
	assert.Contains(t, got, "init container")
	assert.Contains(t, got, "wait-for-db")
	assert.Contains(t, got, "exit code 2")
}

// "exit code 137" alone sends people to the logs, which explain nothing about
// an OOM kill.
func TestPodProblemNamesTheTerminationReason(t *testing.T) {
	pod := crashingPod("redis", "CrashLoopBackOff", nil)
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"},
	}
	assert.Contains(t, podProblem(pod), "OOMKilled, exit code 137")
}

// Two broken services must both be reported, in a stable order.
func TestWorkloadProblemsReportsEveryBrokenServiceSorted(t *testing.T) {
	exit := int32(1)
	env := newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name: "postgres", Image: "postgres:16-alpine", Port: 5432,
		})
	})
	r, _ := newReconciler(t, env,
		crashingPod("redis", "CrashLoopBackOff", &exit),
		crashingPod("postgres", "ImagePullBackOff", nil),
	)
	// Spec order is redis then postgres, and the output must follow it on every
	// call — not the map's randomised iteration order.
	want := []string{
		`service "redis": CrashLoopBackOff (container "redis" last exited with exit code 1); ` +
			"see `kubectl logs -n " + targetNS + " redis-abc123`",
		`service "postgres": ImagePullBackOff`,
	}
	for i := 0; i < 20; i++ {
		assert.Equal(t, want, r.workloadProblems(context.Background(), env, targetNS),
			"the condition message must be byte-identical across reconciles")
	}
}

// ReadyReplicas > 0 reported a 3-replica service ready as soon as one pod came
// up, hiding two permanently crash-looping replicas and stopping the requeue.
func TestReconcileRequiresEveryRequestedReplicaToBeReady(t *testing.T) {
	three := int32(3)
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services[0].Replicas = &three
	}))
	ctx := context.Background()
	reconcile(t, r)

	var d appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &d))
	d.Status.ReadyReplicas = 1
	require.NoError(t, c.Status().Update(ctx, &d))

	res, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	var env devenvv1alpha1.DevEnvironment
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, int32(0), env.Status.ReadyServices, "1 of 3 replicas is not a ready service")
	assert.Equal(t, devenvv1alpha1.PhaseProvisioning, env.Status.Phase)
	assert.Positive(t, res.RequeueAfter, "a partially-rolled-out service must keep being revisited")

	d.Status.ReadyReplicas = 3
	require.NoError(t, c.Status().Update(ctx, &d))
	reconcile(t, r)
	require.NoError(t, c.Get(ctx, request().NamespacedName, &env))
	assert.Equal(t, devenvv1alpha1.PhaseReady, env.Status.Phase)
}

// The state a real crash-looping container actually spends most of its time
// in. Measured on k3s 1.30 against a probe-less container: 10 of 14 samples
// were State.Terminated, only 3 were Waiting{CrashLoopBackOff}. The fabricated
// Running state above is the rarer case, so this covers the common one.
func TestPodProblemReportsAContainerCaughtTerminated(t *testing.T) {
	pod := crashingPod("redis", "CrashLoopBackOff", nil)
	pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name:                 "redis",
		Ready:                false,
		RestartCount:         3,
		State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}
	got := podProblem(pod)
	assert.Contains(t, got, "restarting after failure")
	assert.Contains(t, got, "3 restart(s)")
}

// A container that exited cleanly is finished, not broken. A completed init
// container is the everyday case.
func TestPodProblemIgnoresACleanlyCompletedContainer(t *testing.T) {
	pod := crashingPod("redis", "PodInitializing", nil)
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:                 "wait-for-db",
		Ready:                false,
		RestartCount:         1,
		State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
	}}
	pod.Status.ContainerStatuses = nil
	assert.Empty(t, podProblem(pod), "an init container that eventually succeeded is not a problem")
}

func TestReadinessProbe(t *testing.T) {
	t.Run("absent by default, preserving prior behaviour", func(t *testing.T) {
		spec := &devenvv1alpha1.ServiceSpec{Name: "redis", Port: 6379}
		assert.Nil(t, readinessProbe(spec))
	})

	t.Run("an empty handler becomes a TCP check on the service's own port", func(t *testing.T) {
		spec := &devenvv1alpha1.ServiceSpec{
			Name: "redis", Port: 6379,
			ReadinessProbe: &corev1.Probe{PeriodSeconds: 5},
		}
		got := readinessProbe(spec)
		require.NotNil(t, got)
		require.NotNil(t, got.TCPSocket, "an empty handler is a silent no-op in Kubernetes; it must be filled in")
		assert.Equal(t, intstrFromInt32(6379), got.TCPSocket.Port)
		assert.Equal(t, int32(5), got.PeriodSeconds, "the caller's other probe settings must survive")
	})

	t.Run("an explicit handler is passed through untouched", func(t *testing.T) {
		spec := &devenvv1alpha1.ServiceSpec{
			Name: "api", Port: 8080,
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstrFromInt32(9090)},
			}},
		}
		got := readinessProbe(spec)
		require.NotNil(t, got.HTTPGet)
		assert.Equal(t, "/healthz", got.HTTPGet.Path)
		assert.Equal(t, intstrFromInt32(9090), got.HTTPGet.Port, "an explicit port must not be replaced by spec.port")
		assert.Nil(t, got.TCPSocket)
	})

	t.Run("the caller's spec is not mutated", func(t *testing.T) {
		spec := &devenvv1alpha1.ServiceSpec{
			Name: "redis", Port: 6379,
			ReadinessProbe: &corev1.Probe{},
		}
		_ = readinessProbe(spec)
		assert.Equal(t, corev1.ProbeHandler{}, spec.ReadinessProbe.ProbeHandler,
			"filling the handler in place would mutate the DevEnvironment we were handed")
	})
}

func TestReconcileRendersTheReadinessProbeOntoTheDeployment(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services[0].ReadinessProbe = &corev1.Probe{}
	}))
	reconcile(t, r)

	var d appsv1.Deployment
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "redis", Namespace: targetNS}, &d))
	probe := d.Spec.Template.Spec.Containers[0].ReadinessProbe
	require.NotNil(t, probe)
	require.NotNil(t, probe.TCPSocket)
	assert.Equal(t, intstrFromInt32(6379), probe.TCPSocket.Port)
}

// Catches the operator producing a different template on the second pass from
// its own logic — map iteration order, a re-allocated slice, and so on.
//
// It does NOT prove the Deployment is left unwritten, and cannot: the fake
// client applies no server-side defaults and never maintains Generation, so
// both would be zero however badly the operator behaved. The real assertion,
// against an API server that defaults timeoutSeconds/periodSeconds/path/scheme
// and therefore can churn, lives in the envtest spec "does not rewrite the
// Deployment on repeated reconciles".
func TestReconcileWithAProbeProducesTheSameTemplateTwice(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Services[0].ReadinessProbe = &corev1.Probe{}
	}))
	ctx := context.Background()
	reconcile(t, r)

	var first appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &first))
	reconcile(t, r)
	var second appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "redis", Namespace: targetNS}, &second))

	assert.Equal(t, first.Spec.Template, second.Spec.Template)
}

func TestImageRef(t *testing.T) {
	env := func(reg string) *devenvv1alpha1.DevEnvironment {
		return &devenvv1alpha1.DevEnvironment{
			Spec: devenvv1alpha1.DevEnvironmentSpec{Registry: reg},
		}
	}
	svc := func(image, reg string) *devenvv1alpha1.ServiceSpec {
		return &devenvv1alpha1.ServiceSpec{Name: "s", Image: image, Registry: reg}
	}

	for _, tc := range []struct {
		name               string
		envReg, image, reg string
		want               string
	}{
		{"no registry anywhere leaves the image alone",
			"", "redis:7-alpine", "", "redis:7-alpine"},
		{"the environment registry applies to a bare image",
			"ghcr.io/myorg", "redis:7-alpine", "", "ghcr.io/myorg/redis:7-alpine"},
		{"a service registry overrides the environment's",
			"ghcr.io/myorg", "redis:7-alpine", "quay.io/other", "quay.io/other/redis:7-alpine"},
		{"a service with no registry inherits the environment's",
			"registry.internal:5000", "postgres:16", "", "registry.internal:5000/postgres:16"},
		{"a service registry works with none set on the environment",
			"", "redis:7-alpine", "ghcr.io/myorg", "ghcr.io/myorg/redis:7-alpine"},

		// The rule that removes the need for a per-service opt-out.
		{"an image that already names a registry is never rewritten",
			"ghcr.io/myorg", "quay.io/team/api:1", "", "quay.io/team/api:1"},
		{"...even when the service sets its own registry",
			"", "quay.io/team/api:1", "ghcr.io/myorg", "quay.io/team/api:1"},
		{"a host with a port counts as a registry",
			"ghcr.io", "registry.internal:5000/api:1", "", "registry.internal:5000/api:1"},
		{"localhost counts as a registry",
			"ghcr.io", "localhost/api:1", "", "localhost/api:1"},
		{"localhost with a port counts as a registry",
			"ghcr.io", "localhost:5000/api:1", "", "localhost:5000/api:1"},

		// The other half of Docker's rule: a first segment with no dot or
		// colon is a Docker Hub org, not a host, so it DOES get prefixed.
		{"a Docker Hub org is not a registry and is still prefixed",
			"ghcr.io/myorg", "bitnami/redis:7", "", "ghcr.io/myorg/bitnami/redis:7"},
		{"a bare name with a tag containing a colon is not a registry",
			"ghcr.io", "redis:7-alpine", "", "ghcr.io/redis:7-alpine"},

		// Docker's fourth case: a path component may not contain uppercase, so
		// a dotless uppercase first segment can only be a host. Without this,
		// the result would be an invalid reference rather than a left-alone one.
		{"a dotless uppercase first segment is a host",
			"ghcr.io/myorg", "MYHOST/app:1", "", "MYHOST/app:1"},

		// The reference forms most likely to break a future rewrite.
		{"a digest reference with no registry is still prefixed",
			"ghcr.io/myorg", "redis@sha256:abc123", "", "ghcr.io/myorg/redis@sha256:abc123"},
		{"a digest reference that names a registry is left alone",
			"ghcr.io/myorg", "quay.io/team/api@sha256:abc123", "", "quay.io/team/api@sha256:abc123"},
		{"an image with no tag at all is still prefixed",
			"ghcr.io/myorg", "redis", "", "ghcr.io/myorg/redis"},

		// An explicit empty registry is NOT covered here: Go cannot distinguish
		// an unset string from an empty one, so such a case would be a copy of
		// the inherit case above and could only fail when it does. It is a CRD
		// validation question, covered by the unstructured envtest spec.
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, imageRef(env(tc.envReg), svc(tc.image, tc.reg)))
		})
	}
}

func TestReconcileRendersTheResolvedImageOntoTheDeployment(t *testing.T) {
	r, c := newReconciler(t, newEnv(func(e *devenvv1alpha1.DevEnvironment) {
		e.Spec.Registry = "ghcr.io/myorg"
		e.Spec.Services = append(e.Spec.Services, devenvv1alpha1.ServiceSpec{
			Name: "postgres", Image: "postgres:16-alpine", Port: 5432,
			Registry: "registry.internal:5000",
		}, devenvv1alpha1.ServiceSpec{
			Name: "api", Image: "quay.io/team/api:1", Port: 8080,
		})
	}))
	ctx := context.Background()
	reconcile(t, r)

	for name, want := range map[string]string{
		"redis":    "ghcr.io/myorg/redis:7-alpine",              // inherits the environment
		"postgres": "registry.internal:5000/postgres:16-alpine", // own override
		"api":      "quay.io/team/api:1",                        // already has a registry
	} {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: targetNS}, &d))
		assert.Equal(t, want, d.Spec.Template.Spec.Containers[0].Image, "service %q", name)
	}
}
