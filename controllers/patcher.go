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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

// podSpecOf returns a pointer to the PodSpec embedded in a supported object,
// along with the pod template metadata (labels/annotations) for context.
// The second return is false for unsupported kinds.
func podSpecOf(obj client.Object) (*corev1.PodSpec, bool) {
	switch t := obj.(type) {
	case *appsv1.Deployment:
		return &t.Spec.Template.Spec, true
	case *appsv1.StatefulSet:
		return &t.Spec.Template.Spec, true
	case *appsv1.DaemonSet:
		return &t.Spec.Template.Spec, true
	case *batchv1.Job:
		return &t.Spec.Template.Spec, true
	case *batchv1.CronJob:
		return &t.Spec.JobTemplate.Spec.Template.Spec, true
	case *corev1.Pod:
		return &t.Spec, true
	default:
		return nil, false
	}
}

// strategicPatch changes only the container images mutated on obj relative to
// base. The strategic merge keeps every other field, annotation and label
// untouched, including values written by other controllers or deploy tooling.
// Callers must gate this on an actual diff: an empty strategic patch still
// performs a write in the API (and bumps resourceVersion).
func (r *AlternateImageSourceReconciler) strategicPatch(ctx context.Context, obj client.Object, base client.Object) error {
	return r.Patch(ctx, obj, client.StrategicMergeFrom(base))
}

// rolloutOf reports rollout progress for kinds that surface one. CronJobs and
// standalone pods have no workload rollout and return nil. The controller only
// observes these fields; it never changes an object's rollout settings.
func rolloutOf(obj client.Object) *saffirev1alpha1.RolloutStatus {
	switch t := obj.(type) {
	case *appsv1.Deployment:
		var desired int32 = 1
		if t.Spec.Replicas != nil {
			desired = *t.Spec.Replicas
		}
		return &saffirev1alpha1.RolloutStatus{
			Generation:         t.Generation,
			ObservedGeneration: t.Status.ObservedGeneration,
			UpdatedReplicas:    t.Status.UpdatedReplicas,
			DesiredReplicas:    desired,
		}
	case *appsv1.StatefulSet:
		var desired int32 = 1
		if t.Spec.Replicas != nil {
			desired = *t.Spec.Replicas
		}
		return &saffirev1alpha1.RolloutStatus{
			Generation:         t.Generation,
			ObservedGeneration: t.Status.ObservedGeneration,
			UpdatedReplicas:    t.Status.UpdatedReplicas,
			DesiredReplicas:    desired,
			Manual:             t.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType,
		}
	case *appsv1.DaemonSet:
		return &saffirev1alpha1.RolloutStatus{
			Generation:         t.Generation,
			ObservedGeneration: t.Status.ObservedGeneration,
			UpdatedReplicas:    t.Status.UpdatedNumberScheduled,
			DesiredReplicas:    t.Status.DesiredNumberScheduled,
		}
	default:
		return nil
	}
}
