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
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1alpha1 "github.com/partofaplan/kado-operator/api/v1alpha1"
)

const (
	// finalizerName guards deletion so the provisioned namespace is torn down
	// before the DevEnvironment record disappears. A namespace created in a
	// different namespace-scope than its owner cannot use ownerReferences, so
	// cleanup has to be explicit.
	finalizerName = "devenv.aviture.dev/finalizer"

	// labelManagedBy marks every object the operator creates. Namespaces
	// without it are never adopted and never deleted.
	labelManagedBy = "app.kubernetes.io/managed-by"
	// labelEnvironment records which DevEnvironment owns the object.
	labelEnvironment = "devenv.aviture.dev/environment"
	// labelOwnerNamespace records where that DevEnvironment lives, so two
	// environments of the same name in different namespaces stay distinct.
	labelOwnerNamespace = "devenv.aviture.dev/owner-namespace"
	// labelService names the service within the environment.
	labelService = "devenv.aviture.dev/service"
	// labelOwner carries spec.owner for cost attribution.
	labelOwner = "devenv.aviture.dev/owner"

	managerName = "kado-operator"

	// notReadyRequeue is how often an environment that is not fully ready is
	// re-examined, so workload health is noticed without watching every pod.
	notReadyRequeue = 30 * time.Second

	// sharedVolumeName is the pod volume backed by the environment's PVC.
	sharedVolumeName = "workspace"
)

// DevEnvironmentReconciler reconciles a DevEnvironment object
type DevEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// APIReader reads straight from the API server, bypassing the manager's
	// cache. Optional: when nil, Client is used, which is what the fake-client
	// tests rely on.
	APIReader client.Reader
}

// reader returns the uncached reader when one is wired, falling back to the
// cached Client.
//
// Pods are deliberately read through this rather than through the cache.
// Caching them would start a cluster-wide pod informer and hold every pod in
// the cluster in memory, and this controller needs pods only to explain a
// failure after the fact — never to drive reconciliation. Deployment events
// already requeue us when readiness changes.
func (r *DevEnvironmentReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// +kubebuilder:rbac:groups=devenv.aviture.dev,resources=devenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=devenv.aviture.dev,resources=devenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=devenv.aviture.dev,resources=devenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;persistentvolumeclaims;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// Pods are read, never written: they are how the operator explains why a
// service is not ready. No watch verb — see reader() for why they are not cached.
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list

// Reconcile drives the cluster toward the DevEnvironment spec: a dedicated
// namespace holding a config map, copies of any referenced secrets, an
// optional shared PVC, and a Deployment plus Service per requested service.
func (r *DevEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var env devenvv1alpha1.DevEnvironment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		// Not found is normal after deletion; nothing left to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !env.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &env)
	}

	// The finalizer has to land before anything is created, so a crash between
	// the two cannot strand a namespace with no record pointing at it. Update
	// writes the new resourceVersion back into env, so the rest of this pass
	// can carry on with the same object.
	if !controllerutil.ContainsFinalizer(&env, finalizerName) {
		controllerutil.AddFinalizer(&env, finalizerName)
		if err := r.Update(ctx, &env); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	ready, err := r.reconcileEnvironment(ctx, &env)
	if err != nil {
		log.Error(err, "provisioning failed", "environment", env.Name)
		if statusErr := r.setFailed(ctx, &env, err); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	if err := r.setProvisioned(ctx, &env, ready); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue while the environment is not fully ready. Nothing else will
	// bring us back: the Deployment watch fires on Deployment *status*
	// changes, and once a pod settles into CrashLoopBackOff that status stops
	// changing — ReadyReplicas is already 0 and the conditions are stable. So
	// a container that starts failing after the last reconcile would never be
	// noticed, and Degraded would never be set. Verified against a live
	// cluster: without this the operator logged nothing after startup while a
	// pod restarted six times.
	//
	// Watching Pods instead would be event-driven, but it would also make
	// controller-runtime cache every pod in the cluster for data this
	// controller reads only on the failure path.
	if ready < int32(len(env.Spec.Services)) {
		return ctrl.Result{RequeueAfter: notReadyRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileEnvironment creates or updates every owned resource and returns the
// number of services reporting at least one ready replica.
func (r *DevEnvironmentReconciler) reconcileEnvironment(ctx context.Context, env *devenvv1alpha1.DevEnvironment) (int32, error) {
	ns := env.TargetNamespace()

	if err := r.reconcileNamespace(ctx, env, ns); err != nil {
		return 0, err
	}
	if err := r.reconcileConfigMap(ctx, env, ns); err != nil {
		return 0, err
	}
	if err := r.reconcileSecrets(ctx, env, ns); err != nil {
		return 0, err
	}
	if err := r.reconcilePVC(ctx, env, ns); err != nil {
		return 0, err
	}

	var ready int32
	for i := range env.Spec.Services {
		svc := &env.Spec.Services[i]
		isReady, err := r.reconcileService(ctx, env, ns, svc)
		if err != nil {
			return ready, err
		}
		if isReady {
			ready++
		}
	}

	if err := r.pruneServices(ctx, env, ns); err != nil {
		return ready, err
	}
	return ready, nil
}

// reconcileNamespace creates the environment namespace, refusing to adopt a
// namespace the operator did not create.
func (r *DevEnvironmentReconciler) reconcileNamespace(ctx context.Context, env *devenvv1alpha1.DevEnvironment, name string) error {
	var existing corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: r.labels(env, ""),
			},
		}
		if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %q: %w", name, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("getting namespace %q: %w", name, err)
	}

	if !r.owns(env, &existing) {
		return fmt.Errorf("namespace %q already exists and is not managed by this DevEnvironment", name)
	}

	// Keep labels current (spec.owner may have changed) without disturbing
	// labels applied by other tooling.
	patch := client.MergeFrom(existing.DeepCopy())
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	maps.Copy(existing.Labels, r.labels(env, ""))
	if err := r.Patch(ctx, &existing, patch); err != nil {
		return fmt.Errorf("updating namespace %q labels: %w", name, err)
	}
	return nil
}

// reconcileConfigMap projects spec.config into the environment namespace.
func (r *DevEnvironmentReconciler) reconcileConfigMap(ctx context.Context, env *devenvv1alpha1.DevEnvironment, ns string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName(env), Namespace: ns}}

	if len(env.Spec.Config) == 0 {
		// Config was removed from the spec: drop the ConfigMap so services
		// stop consuming stale values.
		if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting config map: %w", err)
		}
		return nil
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = r.labels(env, "")
		cm.Data = maps.Clone(env.Spec.Config)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling config map: %w", err)
	}
	return nil
}

// reconcileSecrets copies each referenced Secret from the DevEnvironment's own
// namespace into the environment namespace. Copying keeps the environment
// self-contained; pods cannot reference a Secret across namespaces.
func (r *DevEnvironmentReconciler) reconcileSecrets(ctx context.Context, env *devenvv1alpha1.DevEnvironment, ns string) error {
	pull := map[string]struct{}{}
	for _, name := range referencedPullSecrets(env) {
		pull[name] = struct{}{}
	}

	for _, name := range referencedSecrets(env) {
		var src corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: env.Namespace}, &src); err != nil {
			return fmt.Errorf("reading source secret %q: %w", name, err)
		}

		// A pull secret of the wrong type is accepted by the API server and
		// then silently ignored by the kubelet, so the only symptom is an
		// ImagePullBackOff that looks identical to a missing credential.
		// Rejecting it here puts the reason in the DevEnvironment's own
		// conditions instead.
		//
		// Both types the kubelet honours are accepted. Taking only
		// dockerconfigjson would fail the whole environment over a legacy
		// dockercfg Secret that would in fact have worked — a stricter rule
		// than Kubernetes' own, which is not this check's job.
		if _, isPull := pull[name]; isPull && !isDockerAuthSecret(src.Type) {
			return fmt.Errorf(
				"secret %q is named in imagePullSecrets but has type %q, want %q or %q",
				name, src.Type, corev1.SecretTypeDockerConfigJson, corev1.SecretTypeDockercfg,
			)
		}

		dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
			dst.Labels = r.labels(env, "")
			dst.Type = src.Type
			dst.Data = maps.Clone(src.Data)
			return nil
		})
		if err != nil {
			return fmt.Errorf("copying secret %q: %w", name, err)
		}
	}
	return nil
}

// reconcilePVC provisions the environment's shared volume. PVC specs are
// largely immutable, so an existing claim is left alone apart from its labels.
func (r *DevEnvironmentReconciler) reconcilePVC(ctx context.Context, env *devenvv1alpha1.DevEnvironment, ns string) error {
	if env.Spec.Storage == nil {
		return nil
	}

	name := pvcName(env)
	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting pvc %q: %w", name, err)
	}

	accessModes := env.Spec.Storage.AccessModes
	if len(accessModes) == 0 {
		accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: r.labels(env, "")},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      accessModes,
			StorageClassName: env.Spec.Storage.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: env.Spec.Storage.Size},
			},
		},
	}
	if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating pvc %q: %w", name, err)
	}
	return nil
}

// reconcileService materialises one service as a Deployment plus ClusterIP
// Service and reports whether it has a ready replica.
func (r *DevEnvironmentReconciler) reconcileService(
	ctx context.Context,
	env *devenvv1alpha1.DevEnvironment,
	ns string,
	spec *devenvv1alpha1.ServiceSpec,
) (bool, error) {
	selector := map[string]string{
		labelEnvironment: env.Name,
		labelService:     spec.Name,
	}

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Labels = r.labels(env, spec.Name)
		deploy.Spec.Replicas = spec.Replicas
		// The selector is immutable after creation; setting it only on create
		// avoids a rejected update if the label scheme ever changes.
		if deploy.CreationTimestamp.IsZero() {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		}
		deploy.Spec.Template.Labels = r.labels(env, spec.Name)
		deploy.Spec.Template.Spec.Containers = []corev1.Container{r.container(env, spec)}
		deploy.Spec.Template.Spec.Volumes = r.volumes(env, spec)
		deploy.Spec.Template.Spec.ImagePullSecrets = pullSecretRefs(env, spec)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("reconciling deployment %q: %w", spec.Name, err)
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: ns}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = r.labels(env, spec.Name)
		svc.Spec.Selector = selector
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       spec.Port,
			TargetPort: intstrFromInt32(spec.Port),
			Protocol:   corev1.ProtocolTCP,
		}}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("reconciling service %q: %w", spec.Name, err)
	}

	// Compare against the requested replica count, not zero. With
	// `ReadyReplicas > 0` a Deployment asking for three replicas reported the
	// service ready as soon as one came up, so two permanently crash-looping
	// replicas were invisible — and the not-ready requeue stopped, meaning
	// nothing ever revisited them. Replicas defaults to 1, so single-replica
	// services behave exactly as before.
	desired := int32(1)
	if spec.Replicas != nil {
		desired = *spec.Replicas
	}
	return deploy.Status.ReadyReplicas >= desired, nil
}

// container builds the pod container for a service, wiring in the environment
// ConfigMap, any copied secrets, and the shared volume mount.
func (r *DevEnvironmentReconciler) container(env *devenvv1alpha1.DevEnvironment, spec *devenvv1alpha1.ServiceSpec) corev1.Container {
	c := corev1.Container{
		Name:      spec.Name,
		Image:     imageRef(env, spec),
		Ports:     []corev1.ContainerPort{{ContainerPort: spec.Port, Protocol: corev1.ProtocolTCP}},
		Resources: spec.Resources,
	}

	// Map iteration order is random; sort so the pod template is stable and
	// does not trigger a rollout on every reconcile.
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		c.Env = append(c.Env, corev1.EnvVar{Name: k, Value: spec.Env[k]})
	}

	if len(env.Spec.Config) > 0 {
		c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapName(env)},
			},
		})
	}
	for _, name := range spec.SecretRefs {
		c.EnvFrom = append(c.EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
			},
		})
	}

	if spec.MountPath != "" && env.Spec.Storage != nil {
		c.VolumeMounts = []corev1.VolumeMount{{Name: sharedVolumeName, MountPath: spec.MountPath}}
	}

	c.ReadinessProbe = readinessProbe(spec)
	return c
}

// imageRef resolves the image a service runs, applying the environment's
// registry — or the service's own override — to an image that does not already
// name one.
func imageRef(env *devenvv1alpha1.DevEnvironment, spec *devenvv1alpha1.ServiceSpec) string {
	registry := spec.Registry
	if registry == "" {
		registry = env.Spec.Registry
	}
	if registry == "" || hasRegistry(spec.Image) {
		return spec.Image
	}
	return registry + "/" + spec.Image
}

// hasRegistry reports whether an image reference already names a registry.
//
// This is Docker's own rule, and following it rather than inventing one is
// what makes the feature predictable. The first path segment is a host if it
// contains a dot or a colon, is exactly "localhost", or contains an uppercase
// letter. Everything else is a Docker Hub repository — which is why
// `redis:7-alpine` is a repository named redis rather than a registry named
// redis, and why `myteam/api` is a Docker Hub org rather than a host.
//
// It also removes the need for a per-service opt-out: a service that pins
// `quay.io/team/api:1` keeps it even when the environment sets a registry,
// because rewriting it would produce `<registry>/quay.io/team/api:1`.
func hasRegistry(image string) bool {
	first, _, found := strings.Cut(image, "/")
	if !found {
		return false
	}
	// The uppercase clause is the easiest of the four to miss: a path component
	// may not contain uppercase, so a dotless uppercase segment cannot be a
	// repository. Without it `MYHOST/app:1` would become
	// `<registry>/MYHOST/app:1`, an invalid reference that fails at pull time.
	return first == "localhost" ||
		strings.ContainsAny(first, ".:") ||
		strings.ToLower(first) != first
}

// readinessProbe renders the service's probe, defaulting an empty handler to a
// TCP check against the service's own port.
//
// The default exists because the empty handler is otherwise a silent no-op:
// Kubernetes accepts a Probe with no action and never runs anything, so
// `readinessProbe: {}` would look like it was doing something while changing
// nothing. A TCP check on the port the service already has to declare is both
// the obvious intent and correct for most databases, caches and queues.
func readinessProbe(spec *devenvv1alpha1.ServiceSpec) *corev1.Probe {
	if spec.ReadinessProbe == nil {
		return nil
	}
	// Copied because the spec belongs to the caller's object; filling the
	// handler in place would mutate the DevEnvironment we were handed.
	probe := spec.ReadinessProbe.DeepCopy()
	if probe.ProbeHandler == (corev1.ProbeHandler{}) {
		probe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstrFromInt32(spec.Port)},
		}
	}
	return probe
}

func (r *DevEnvironmentReconciler) volumes(env *devenvv1alpha1.DevEnvironment, spec *devenvv1alpha1.ServiceSpec) []corev1.Volume {
	if spec.MountPath == "" || env.Spec.Storage == nil {
		return nil
	}
	return []corev1.Volume{{
		Name: sharedVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName(env)},
		},
	}}
}

// pruneServices removes Deployments and Services for entries that have been
// dropped from the spec.
func (r *DevEnvironmentReconciler) pruneServices(ctx context.Context, env *devenvv1alpha1.DevEnvironment, ns string) error {
	wanted := make(map[string]struct{}, len(env.Spec.Services))
	for _, s := range env.Spec.Services {
		wanted[s.Name] = struct{}{}
	}

	opts := []client.ListOption{
		client.InNamespace(ns),
		client.MatchingLabels{labelEnvironment: env.Name, labelManagedBy: managerName},
	}

	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, opts...); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if _, keep := wanted[d.Labels[labelService]]; keep || d.Labels[labelService] == "" {
			continue
		}
		if err := r.Delete(ctx, d); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("pruning deployment %q: %w", d.Name, err)
		}
	}

	var services corev1.ServiceList
	if err := r.List(ctx, &services, opts...); err != nil {
		return fmt.Errorf("listing services: %w", err)
	}
	for i := range services.Items {
		s := &services.Items[i]
		if _, keep := wanted[s.Labels[labelService]]; keep || s.Labels[labelService] == "" {
			continue
		}
		if err := r.Delete(ctx, s); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("pruning service %q: %w", s.Name, err)
		}
	}
	return nil
}

// reconcileDelete tears down the namespace and then releases the finalizer.
func (r *DevEnvironmentReconciler) reconcileDelete(ctx context.Context, env *devenvv1alpha1.DevEnvironment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(env, finalizerName) {
		return ctrl.Result{}, nil
	}

	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: env.TargetNamespace()}, &ns)
	switch {
	case err == nil:
		if r.owns(env, &ns) {
			if ns.DeletionTimestamp.IsZero() {
				log.Info("deleting environment namespace", "namespace", ns.Name)
				if err := r.Delete(ctx, &ns); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("deleting namespace %q: %w", ns.Name, err)
				}
			}
			// Namespace teardown is asynchronous. Hold the finalizer until the
			// namespace is gone so the record documents what is still being
			// reclaimed; the namespace watch requeues us when it disappears.
			return ctrl.Result{}, nil
		}
		// Not ours — someone else's namespace of the same name. Leave it.
		log.Info("skipping deletion of unmanaged namespace", "namespace", ns.Name)
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, fmt.Errorf("getting namespace during delete: %w", err)
	}

	controllerutil.RemoveFinalizer(env, finalizerName)
	if err := r.Update(ctx, env); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *DevEnvironmentReconciler) setProvisioned(ctx context.Context, env *devenvv1alpha1.DevEnvironment, ready int32) error {
	total := int32(len(env.Spec.Services))
	allReady := ready == total

	phase := devenvv1alpha1.PhaseProvisioning
	if allReady {
		phase = devenvv1alpha1.PhaseReady
	}

	env.Status.Phase = phase
	env.Status.Namespace = env.TargetNamespace()
	env.Status.ReadyServices = ready
	env.Status.TotalServices = total
	env.Status.ObservedGeneration = env.Generation

	message := fmt.Sprintf("%d/%d services ready", ready, total)
	r.setCondition(env, devenvv1alpha1.ConditionAvailable, boolStatus(allReady), reasonFor(allReady, "EnvironmentReady", "ServicesNotReady"), message)

	// Degraded used to be hardcoded false whenever the reconciler itself
	// succeeded. That conflates two different questions: "did I manage to
	// create the resources" and "is the environment actually working". A
	// service whose container cannot start — a missing required env var, a
	// bad image, an unschedulable pod — reports exactly the same zero ready
	// replicas as one still pulling, so the environment sat in Provisioning
	// with Degraded=false indefinitely and nothing said what was wrong.
	var problems []string
	if !allReady {
		problems = r.workloadProblems(ctx, env, env.TargetNamespace())
	}

	if len(problems) > 0 {
		detail := strings.Join(problems, "; ")
		// Not Progressing: a container in CrashLoopBackOff is not on its way
		// to Ready, and saying otherwise is the same lie in a second field.
		r.setCondition(env, devenvv1alpha1.ConditionProgressing, metav1.ConditionFalse, "WorkloadUnhealthy", detail)
		r.setCondition(env, devenvv1alpha1.ConditionDegraded, metav1.ConditionTrue, "WorkloadUnhealthy", detail)
	} else {
		r.setCondition(env, devenvv1alpha1.ConditionProgressing, boolStatus(!allReady), reasonFor(!allReady, "Provisioning", "Provisioned"), message)
		r.setCondition(env, devenvv1alpha1.ConditionDegraded, metav1.ConditionFalse, "ReconcileSucceeded", "Reconciliation completed without error")
	}

	return r.patchStatus(ctx, env)
}

// blockingWaitReasons are container waiting reasons that will not clear on
// their own. Anything absent from this set — ContainerCreating,
// PodInitializing — is a pod that is simply still coming up, and reporting it
// as degraded would make the condition meaningless during a normal start.
var blockingWaitReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"ErrImageNeverPull":          true,
	"ImageInspectError":          true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
}

// workloadProblems returns one human-readable problem per service that cannot
// start. It never fails the reconcile: the resources are already correct, and
// a listing error here costs only detail.
func (r *DevEnvironmentReconciler) workloadProblems(ctx context.Context, env *devenvv1alpha1.DevEnvironment, ns string) []string {
	var pods corev1.PodList
	if err := r.reader().List(ctx, &pods,
		client.InNamespace(ns),
		client.MatchingLabels{labelManagedBy: managerName, labelEnvironment: env.Name},
	); err != nil {
		logf.FromContext(ctx).Error(err, "listing pods for status detail", "namespace", ns)
		return nil
	}

	// One entry per service: every replica of a service fails the same way, and
	// repeating it per pod would bury the message.
	byService := map[string]string{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		svc := pod.Labels[labelService]
		if svc == "" || byService[svc] != "" {
			continue
		}
		if detail := podProblem(pod); detail != "" {
			byService[svc] = detail
		}
	}

	// Emitted in spec order rather than by ranging the map, so the message is
	// byte-identical across reconciles by construction. Go randomises map
	// iteration, which would otherwise reshuffle the message on every pass —
	// pure write churn, and a test for it could only ever be probabilistic.
	out := make([]string, 0, len(byService))
	for i := range env.Spec.Services {
		if detail := byService[env.Spec.Services[i].Name]; detail != "" {
			out = append(out, fmt.Sprintf("service %q: %s", env.Spec.Services[i].Name, detail))
		}
	}
	return out
}

// podProblem describes why a pod cannot run, or returns "" if it is healthy or
// merely still starting.
func podProblem(pod *corev1.Pod) string {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return fmt.Sprintf("Unschedulable: %s", c.Message)
		}
	}
	// Init containers first: one that never succeeds blocks the pod entirely,
	// and the app containers below it would report only ContainerCreating,
	// which reads as "still starting" forever. The operator does not create
	// init containers, but reconcileService only overwrites Containers and
	// Volumes, so one added by hand survives every reconcile.
	for i := range pod.Status.InitContainerStatuses {
		if d := containerProblem(&pod.Status.InitContainerStatuses[i], pod, "init container"); d != "" {
			return d
		}
	}
	for i := range pod.Status.ContainerStatuses {
		if d := containerProblem(&pod.Status.ContainerStatuses[i], pod, "container"); d != "" {
			return d
		}
	}
	return ""
}

// containerProblem describes why a single container cannot run.
func containerProblem(cs *corev1.ContainerStatus, pod *corev1.Pod, kind string) string {
	// A container that is passing its readiness probe right now is not a
	// problem, whatever its history. This matters because an environment is
	// inspected whenever ANY service is unready, so a healthy service that
	// restarted once during startup must not be reported alongside the one
	// that is actually broken.
	if cs.Ready {
		return ""
	}

	if w := cs.State.Waiting; w != nil && blockingWaitReasons[w.Reason] {
		// CrashLoopBackOff's own waiting message is only "back-off 5m0s
		// restarting failed container", which says nothing about the cause.
		// The previous termination carries the exit code and reason.
		if t := cs.LastTerminationState.Terminated; t != nil && w.Reason == "CrashLoopBackOff" {
			return fmt.Sprintf("%s (%s %q last exited with %s); see `kubectl logs -n %s %s`",
				w.Reason, kind, cs.Name, terminationDetail(t), pod.Namespace, pod.Name)
		}
		if w.Message != "" {
			return fmt.Sprintf("%s: %s", w.Reason, w.Message)
		}
		return w.Reason
	}

	// A crash-looping container is Waiting only *between* restarts. Once the
	// backoff expires it is Running again until it dies, and a reconcile that
	// lands in that window would otherwise see nothing wrong — flipping
	// Degraded back to false and rewriting both conditions' transition times
	// on every poll. Restart history is the stable signal.
	// A container that completed successfully is done, not broken — a finished
	// init container is the normal case. Checked explicitly rather than relying
	// on cs.Ready and an empty lastState, both of which are kubelet fields whose
	// init-container semantics moved with restartable init containers in 1.28.
	if t := cs.State.Terminated; t != nil && t.ExitCode == 0 {
		return ""
	}

	if cs.RestartCount > 0 {
		if t := cs.LastTerminationState.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Sprintf("restarting after failure (%s %q exited with %s, %d restart(s)); see `kubectl logs -n %s %s`",
				kind, cs.Name, terminationDetail(t), cs.RestartCount, pod.Namespace, pod.Name)
		}
	}
	return ""
}

// terminationDetail renders an exit, naming the kubelet's reason when it adds
// something an exit code does not. OOMKilled is the case that matters: the
// container logs explain nothing, so "exit code 137" alone sends people to the
// wrong place.
func terminationDetail(t *corev1.ContainerStateTerminated) string {
	if t.Reason != "" && t.Reason != "Error" {
		return fmt.Sprintf("%s, exit code %d", t.Reason, t.ExitCode)
	}
	return fmt.Sprintf("exit code %d", t.ExitCode)
}

func (r *DevEnvironmentReconciler) setFailed(ctx context.Context, env *devenvv1alpha1.DevEnvironment, cause error) error {
	env.Status.Phase = devenvv1alpha1.PhaseFailed
	env.Status.ObservedGeneration = env.Generation

	r.setCondition(env, devenvv1alpha1.ConditionAvailable, metav1.ConditionFalse, "ProvisioningFailed", cause.Error())
	r.setCondition(env, devenvv1alpha1.ConditionProgressing, metav1.ConditionFalse, "ProvisioningFailed", cause.Error())
	r.setCondition(env, devenvv1alpha1.ConditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.patchStatus(ctx, env)
}

func (r *DevEnvironmentReconciler) patchStatus(ctx context.Context, env *devenvv1alpha1.DevEnvironment) error {
	if err := r.Status().Update(ctx, env); err != nil {
		// A conflict just means a fresher copy exists; the next reconcile
		// recomputes status from it.
		if apierrors.IsConflict(err) {
			return nil
		}
		return fmt.Errorf("updating status: %w", err)
	}
	return nil
}

func (r *DevEnvironmentReconciler) setCondition(env *devenvv1alpha1.DevEnvironment, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&env.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: env.Generation,
	})
}

// labels returns the label set stamped on every object the operator creates.
func (r *DevEnvironmentReconciler) labels(env *devenvv1alpha1.DevEnvironment, service string) map[string]string {
	l := map[string]string{
		labelManagedBy:      managerName,
		labelEnvironment:    env.Name,
		labelOwnerNamespace: env.Namespace,
	}
	if env.Spec.Owner != "" {
		l[labelOwner] = env.Spec.Owner
	}
	if service != "" {
		l[labelService] = service
	}
	return l
}

// owns reports whether an object was created by this operator on behalf of
// this specific DevEnvironment.
func (r *DevEnvironmentReconciler) owns(env *devenvv1alpha1.DevEnvironment, obj client.Object) bool {
	l := obj.GetLabels()
	return l[labelManagedBy] == managerName &&
		l[labelEnvironment] == env.Name &&
		l[labelOwnerNamespace] == env.Namespace
}

func configMapName(env *devenvv1alpha1.DevEnvironment) string { return env.Name + "-config" }
func pvcName(env *devenvv1alpha1.DevEnvironment) string       { return env.Name + "-workspace" }

// pullSecrets resolves the pull secrets a service uses, applying the
// environment's — or the service's own override — with the same precedence as
// imageRef applies registry.
func pullSecrets(env *devenvv1alpha1.DevEnvironment, spec *devenvv1alpha1.ServiceSpec) []string {
	if len(spec.ImagePullSecrets) > 0 {
		return spec.ImagePullSecrets
	}
	return env.Spec.ImagePullSecrets
}

// pullSecretRefs renders the resolved pull secrets as pod-spec references.
// Returns nil rather than an empty slice when there are none, so the pod
// template matches what the API server stores and does not churn a rollout on
// every reconcile.
func pullSecretRefs(env *devenvv1alpha1.DevEnvironment, spec *devenvv1alpha1.ServiceSpec) []corev1.LocalObjectReference {
	names := pullSecrets(env, spec)
	if len(names) == 0 {
		return nil
	}
	refs := make([]corev1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		refs = append(refs, corev1.LocalObjectReference{Name: name})
	}
	return refs
}

// isDockerAuthSecret reports whether a Secret type is one the kubelet will use
// as an image pull secret. dockercfg is the pre-1.9 format and is still
// honoured.
func isDockerAuthSecret(t corev1.SecretType) bool {
	return t == corev1.SecretTypeDockerConfigJson || t == corev1.SecretTypeDockercfg
}

// referencedPullSecrets returns the deduplicated, sorted set of secrets used as
// image pull secrets — the environment's own plus every service override.
//
// Every name is included rather than only the ones that survive resolution: a
// service overriding the environment's list does not stop the environment's
// secret from being needed by some other service, and copying one that turns
// out to be unused is harmless.
func referencedPullSecrets(env *devenvv1alpha1.DevEnvironment) []string {
	seen := map[string]struct{}{}
	for _, name := range env.Spec.ImagePullSecrets {
		seen[name] = struct{}{}
	}
	for _, s := range env.Spec.Services {
		for _, name := range s.ImagePullSecrets {
			seen[name] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// referencedSecrets returns the deduplicated, sorted set of secrets that must
// exist in the environment namespace: those mounted via envFrom, plus every
// image pull secret. Both are copied by the same loop.
func referencedSecrets(env *devenvv1alpha1.DevEnvironment) []string {
	seen := map[string]struct{}{}
	for _, name := range referencedPullSecrets(env) {
		seen[name] = struct{}{}
	}
	for _, s := range env.Spec.Services {
		for _, name := range s.SecretRefs {
			seen[name] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// sortedKeys returns a set's members in a stable order. Map iteration is
// random, and an unstable order here would rewrite the pod template — and so
// trigger a rollout — on every reconcile.
func sortedKeys(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func boolStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func reasonFor(b bool, whenTrue, whenFalse string) string {
	if b {
		return whenTrue
	}
	return whenFalse
}

// SetupWithManager sets up the controller with the Manager.
func (r *DevEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Owns() cannot be used here: the Deployments and Namespaces this
	// controller creates live outside the DevEnvironment's own namespace, and
	// Kubernetes forbids a cross-namespace ownerReference. Without an owner
	// link the built-in watch never fires, so readiness changes would only be
	// noticed on the cache's periodic resync. Map back via our own labels
	// instead.
	return ctrl.NewControllerManagedBy(mgr).
		For(&devenvv1alpha1.DevEnvironment{}).
		Watches(&appsv1.Deployment{}, r.environmentFromLabels()).
		Watches(&corev1.Namespace{}, r.environmentFromLabels()).
		Named("devenvironment").
		Complete(r)
}
