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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DevEnvironmentPhase is a high level summary of where the environment is in
// its lifecycle. It is a convenience for humans and kubectl columns; machine
// readable detail belongs in Conditions.
type DevEnvironmentPhase string

const (
	// PhasePending means the environment has been accepted but no resources
	// have been created yet.
	PhasePending DevEnvironmentPhase = "Pending"
	// PhaseProvisioning means resources are being created or are not yet ready.
	PhaseProvisioning DevEnvironmentPhase = "Provisioning"
	// PhaseReady means every requested service reports ready replicas.
	PhaseReady DevEnvironmentPhase = "Ready"
	// PhaseFailed means reconciliation could not make progress.
	PhaseFailed DevEnvironmentPhase = "Failed"
)

// Condition types set by the controller.
const (
	// ConditionAvailable is true once the environment is fully provisioned.
	ConditionAvailable = "Available"
	// ConditionProgressing is true while resources are being created or updated.
	ConditionProgressing = "Progressing"
	// ConditionDegraded is true when reconciliation hit an error.
	ConditionDegraded = "Degraded"
)

// StorageSpec describes the shared PersistentVolumeClaim provisioned for the
// environment. Services may mount it via their MountPath.
type StorageSpec struct {
	// size is the requested capacity of the environment's shared volume.
	// +required
	Size resource.Quantity `json:"size"`

	// storageClassName selects the StorageClass backing the claim. When empty
	// the cluster default StorageClass is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// accessModes for the claim. Defaults to ReadWriteOnce.
	// +optional
	// +listType=atomic
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// ServiceSpec describes one supporting service (database, cache, queue, ...)
// deployed into the environment namespace.
type ServiceSpec struct {
	// name is the DNS label used for the Deployment and Service objects. It
	// must be unique within the DevEnvironment.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// image is the container image to run.
	// +required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// registry is the image registry, optionally with a namespace path, that
	// service images are pulled from — `ghcr.io`, `ghcr.io/myorg`, or
	// `registry.internal:5000/mirror`. No scheme, no trailing slash.
	//
	// It is prefixed onto a service image that does not already name a
	// registry of its own, so `redis:7-alpine` becomes
	// `<registry>/redis:7-alpine` while `quay.io/team/api:1` is left alone.
	// That is the same rule Docker and Kubernetes use to decide whether the
	// first path segment is a host, so a service that pins its own registry
	// keeps it without needing to opt out.
	//
	// Overrides spec.registry for this service alone. Leave unset to inherit
	// it; there is no need to opt out, because an image that names its own
	// registry is never rewritten.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`
	Registry string `json:"registry,omitempty"`

	// port is the container port exposed through the ClusterIP Service.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// replicas is the desired number of pods. Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// env are plain environment variables passed to the container. Secret
	// material belongs in SecretRefs, not here.
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// secretRefs names Secrets in the DevEnvironment's own namespace that are
	// copied into the environment namespace and mounted via envFrom.
	// +optional
	// +listType=atomic
	SecretRefs []string `json:"secretRefs,omitempty"`

	// mountPath, when set, mounts the environment's shared volume into the
	// container at this path. Requires spec.storage to be set.
	// +optional
	MountPath string `json:"mountPath,omitempty"`

	// resources are the compute resources for the container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// readinessProbe gates when this service counts as ready.
	//
	// Without one, Kubernetes calls a running container ready the instant it
	// starts, so a service that takes time to accept connections — or that
	// crashes a few seconds in — reports Ready in the meantime, and the
	// environment's own readyServices count inherits that. Setting a probe
	// makes "Ready" mean the service is actually answering.
	//
	// Leave the handler empty for a TCP check against this service's own
	// port, which is the right answer for most databases, caches and queues:
	//
	//	readinessProbe: {}
	//
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`
}

// DevEnvironmentSpec defines the desired state of DevEnvironment.
type DevEnvironmentSpec struct {
	// namespaceName is the namespace provisioned for this environment. It
	// defaults to the DevEnvironment's own name and is immutable once set,
	// because moving an environment between namespaces would orphan its data.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="namespaceName is immutable"
	NamespaceName string `json:"namespaceName,omitempty"`

	// owner identifies the person or team the environment belongs to. It is
	// stamped onto the namespace as a label for cost attribution.
	// +optional
	Owner string `json:"owner,omitempty"`

	// storage requests a shared PersistentVolumeClaim for the environment.
	// +optional
	Storage *StorageSpec `json:"storage,omitempty"`

	// registry is the image registry, optionally with a namespace path, that
	// service images are pulled from — `ghcr.io`, `ghcr.io/myorg`, or
	// `registry.internal:5000/mirror`. No scheme, no trailing slash.
	//
	// It is prefixed onto a service image that does not already name a
	// registry of its own, so `redis:7-alpine` becomes
	// `<registry>/redis:7-alpine` while `quay.io/team/api:1` is left alone.
	// That is the same rule Docker and Kubernetes use to decide whether the
	// first path segment is a host, so a service that pins its own registry
	// keeps it without needing to opt out.
	//
	// Leave unset for the default: each image reference is used exactly as
	// written, which for a bare name means Docker Hub.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`
	Registry string `json:"registry,omitempty"`

	// services are the supporting services deployed into the environment.
	// +optional
	// +listType=map
	// +listMapKey=name
	Services []ServiceSpec `json:"services,omitempty"`

	// config is injected into the environment namespace as a ConfigMap named
	// "<environment>-config" and exposed to every service via envFrom.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// DevEnvironmentStatus defines the observed state of DevEnvironment.
type DevEnvironmentStatus struct {
	// phase is a one word summary of the environment lifecycle.
	// +optional
	Phase DevEnvironmentPhase `json:"phase,omitempty"`

	// namespace is the namespace actually provisioned for this environment.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// readyServices is the number of services with at least one ready replica.
	// +optional
	ReadyServices int32 `json:"readyServices"`

	// totalServices is the number of services requested in the spec.
	// +optional
	TotalServices int32 `json:"totalServices"`

	// observedGeneration is the spec generation this status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the DevEnvironment resource.
	//
	// Standard condition types include:
	// - "Available": the environment is fully provisioned
	// - "Progressing": resources are being created or updated
	// - "Degraded": reconciliation failed to reach the desired state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// Degraded is surfaced in the default output on purpose: the symptom that
// prompted it was an environment sitting at "Provisioning 1 2" with the reason
// reachable only through `describe`.
// +kubebuilder:printcolumn:name="Degraded",type=string,JSONPath=`.status.conditions[?(@.type=="Degraded")].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.readyServices`
// +kubebuilder:printcolumn:name="Services",type=string,JSONPath=`.status.totalServices`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DevEnvironment is the Schema for the devenvironments API
type DevEnvironment struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of DevEnvironment
	// +required
	Spec DevEnvironmentSpec `json:"spec"`

	// status defines the observed state of DevEnvironment
	// +optional
	Status DevEnvironmentStatus `json:"status,omitempty,omitzero"`
}

// TargetNamespace returns the namespace the environment provisions, applying
// the default of the resource's own name.
func (d *DevEnvironment) TargetNamespace() string {
	if d.Spec.NamespaceName != "" {
		return d.Spec.NamespaceName
	}
	return d.Name
}

// +kubebuilder:object:root=true

// DevEnvironmentList contains a list of DevEnvironment
type DevEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DevEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DevEnvironment{}, &DevEnvironmentList{})
}
