/*
Copyright 2020 Fairwinds

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License
*/

package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

// maxOwnerDepth bounds how far we walk up the owner chain. Real chains
// (CronJob -> Job -> Pod, Deployment -> ReplicaSet -> Pod) are much shorter;
// this only guards against malformed/cyclic references.
const maxOwnerDepth = 8

// newControllerObject returns an empty typed object for a supported workload
// owner GVK. The second return is false for GVKs this controller does not know.
func newControllerObject(gvk schema.GroupVersionKind) (client.Object, bool) {
	switch gvk.GroupVersion().String() + "/" + gvk.Kind {
	case "apps/v1/ReplicaSet":
		return &appsv1.ReplicaSet{}, true
	case "apps/v1/Deployment":
		return &appsv1.Deployment{}, true
	case "apps/v1/StatefulSet":
		return &appsv1.StatefulSet{}, true
	case "apps/v1/DaemonSet":
		return &appsv1.DaemonSet{}, true
	case "batch/v1/Job":
		return &batchv1.Job{}, true
	case "batch/v1/CronJob":
		return &batchv1.CronJob{}, true
	default:
		return nil, false
	}
}

// isRewritableTopLevelKind reports whether a resolved top object is a kind
// whose pod template this controller is able to patch.
func isRewritableTopLevelKind(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "CronJob", "Pod":
		return true
	default:
		return false
	}
}

// resolveTop walks the controller ownerReferences of a pod upward and returns
// the highest object along the chain. A pod without a controller owner (or
// owned by a Node, as static pods are) is returned as its own target and is
// rewritten directly. An unknown intermediate owner stops the walk at the
// highest recognized object so unsupported operators degrade gracefully.
func (r *AlternateImageSourceReconciler) resolveTop(ctx context.Context, pod *corev1.Pod) (client.Object, schema.GroupVersionKind, error) {
	current := client.Object(pod)
	for i := 0; i < maxOwnerDepth; i++ {
		ref := metav1.GetControllerOf(current)
		if ref == nil {
			gvk, err := apiutil.GVKForObject(current, r.Scheme)
			if err != nil {
				return nil, schema.GroupVersionKind{}, err
			}
			return current, gvk, nil
		}
		// Static pods are owned by a Node but remain effectively standalone.
		if ref.Kind == "Node" {
			gvk, err := apiutil.GVKForObject(current, r.Scheme)
			if err != nil {
				return nil, schema.GroupVersionKind{}, err
			}
			return current, gvk, nil
		}

		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, schema.GroupVersionKind{}, err
		}
		ownerGVK := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: ref.Kind}
		owner, ok := newControllerObject(ownerGVK)
		if !ok {
			// Unknown owner kind: keep the current object as the top of the chain.
			topGVK, gvkErr := apiutil.GVKForObject(current, r.Scheme)
			if gvkErr != nil {
				return nil, schema.GroupVersionKind{}, gvkErr
			}
			return current, topGVK, nil
		}

		if err := r.Get(ctx, client.ObjectKey{Namespace: current.GetNamespace(), Name: ref.Name}, owner); err != nil {
			return nil, schema.GroupVersionKind{}, err
		}
		current = owner
	}
	return nil, schema.GroupVersionKind{}, fmt.Errorf("owner reference chain exceeded depth %d (cycle?) starting from pod %s/%s", maxOwnerDepth, pod.Namespace, pod.Name)
}

// targetRefFor builds the durable key of an audit result from a resolved object.
func targetRefFor(obj client.Object, gvk schema.GroupVersionKind) saffirev1alpha1.TargetRef {
	return saffirev1alpha1.TargetRef{
		Group:     gvk.Group,
		Kind:      gvk.Kind,
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		UID:       obj.GetUID(),
	}
}

// targetKey is the in-memory thrash map key (UID is optional, e.g. in tests).
type targetKey struct {
	group     string
	kind      string
	namespace string
	name      string
}

func keyFor(ref saffirev1alpha1.TargetRef) targetKey {
	return targetKey{group: ref.Group, kind: ref.Kind, namespace: ref.Namespace, name: ref.Name}
}
