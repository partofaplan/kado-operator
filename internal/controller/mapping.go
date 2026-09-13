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

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// environmentFromLabels maps a watched cluster-scoped object back to the
// DevEnvironment that created it. Namespaces cannot carry an ownerReference to
// a namespaced DevEnvironment, so the labels stamped at creation are the only
// link back — without this the controller would never learn that a namespace
// finished terminating and would hold its finalizer forever.
func (r *DevEnvironmentReconciler) environmentFromLabels() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)
}

// mapToEnvironment resolves an owned object back to its DevEnvironment, or
// returns nothing if the object is not one of ours.
func (r *DevEnvironmentReconciler) mapToEnvironment(_ context.Context, obj client.Object) []ctrl.Request {
	l := obj.GetLabels()
	if l[labelManagedBy] != managerName {
		return nil
	}
	name, ns := l[labelEnvironment], l[labelOwnerNamespace]
	if name == "" || ns == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}}
}

// intstrFromInt32 builds an IntOrString from an int32 port without the
// intermediate int conversion that overflows on 32-bit platforms.
func intstrFromInt32(port int32) intstr.IntOrString {
	return intstr.IntOrString{Type: intstr.Int, IntVal: port}
}
