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
	"sort"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

// AlternateImageSourceReconciler rewrites pod template images on top-level
// workload controllers when pods experience image pull failures.
type AlternateImageSourceReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// thrash holds per-target patch attempts used to detect an external actor
	// reverting our writes. It is lazily initialized and rehydrated from AIS
	// status on the first reconcile so process restarts do not reset detection.
	thrash *thrashTracker
	// nowFunc is injectable for deterministic time-window tests.
	nowFunc func() time.Time
}

// +kubebuilder:rbac:groups=saffire.fairwinds.com,resources=alternateimagesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=saffire.fairwinds.com,resources=alternateimagesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;watch;list;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets;replicasets,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch;patch

// Reconcile evaluates the AlternateImageSource against pods currently failing
// to pull images in its namespace, and rewrites the owning workload's pod
// template when an equivalent repository is available.
func (r *AlternateImageSourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.Log.WithValues("alternateimagesource", req.NamespacedName)

	r.ensureTracker()

	var ais saffirev1alpha1.AlternateImageSource
	if err := r.Get(ctx, req.NamespacedName, &ais); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		log.Error(err, "unable to fetch AlternateImageSource")
		return reconcile.Result{}, err
	}

	// Restore thrash windows from status without clobbering live in-memory state.
	r.thrash.rehydrate(ais.Status.Results)

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(req.Namespace)); err != nil {
		log.Error(err, "unable to list pods")
		return reconcile.Result{}, err
	}

	badRepositories := map[string]bool{}
	var failingPods []corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !r.podHasImagePullErr(pod) {
			continue
		}
		failingPods = append(failingPods, *pod)
		collectBadRepositories(pod, badRepositories)
	}

	// Results are keyed by target and carried across reconciles: once a target
	// has been rewritten its audit history (patch time, attempts, rollout
	// snapshot) remains stable even when later reconciles only observe that it
	// has converged. This keeps a repeat reconcile a true no-op at the API.
	priorByKey := map[targetKey]saffirev1alpha1.TargetResult{}
	var priorOrder []targetKey
	for _, prior := range ais.Status.Results {
		key := keyFor(prior.Target)
		if _, seen := priorByKey[key]; !seen {
			priorOrder = append(priorOrder, key)
		}
		priorByKey[key] = prior
	}
	resultsByKey := map[targetKey]saffirev1alpha1.TargetResult{}
	var resultOrder []targetKey

	if len(failingPods) > 0 {
		targets, err := r.collectTargets(ctx, failingPods)
		if err != nil {
			log.Error(err, "unable to resolve pod owners")
			return reconcile.Result{}, err
		}
		for _, entry := range targets {
			key := keyFor(targetRefFor(entry.obj, entry.gvk))
			prior := priorByKey[key]
			result := r.handleTarget(ctx, log, &ais, entry, badRepositories, prior)
			resultsByKey[key] = result
			resultOrder = append(resultOrder, key)
		}
	}

	// Preserve results for targets no longer failing this round, in their
	// previous order, so historical audit entries are never silently dropped.
	for _, key := range priorOrder {
		if _, seen := resultsByKey[key]; !seen {
			resultsByKey[key] = priorByKey[key]
			resultOrder = append(resultOrder, key)
		}
	}
	var results []saffirev1alpha1.TargetResult
	for _, key := range resultOrder {
		results = append(results, resultsByKey[key])
	}

	newStatus := saffirev1alpha1.AlternateImageSourceStatus{
		ObservedGeneration: ais.ObjectMeta.Generation,
		Results:            results,
	}
	if err := r.updateStatusIfChanged(ctx, req.NamespacedName, newStatus); err != nil {
		log.Error(err, "unable to update AlternateImageSource status")
		return reconcile.Result{}, err
	}

	return ctrl.Result{}, nil
}

// resolvedTarget pairs an object with its own GVK.
type resolvedTarget struct {
	obj client.Object
	gvk schema.GroupVersionKind
}

// collectTargets resolves the top controller of every failing pod and
// de-duplicates them by their TargetRef. A pod owned by a controller is never
// returned as its own target; only standalone pods are.
func (r *AlternateImageSourceReconciler) collectTargets(ctx context.Context, failingPods []corev1.Pod) ([]resolvedTarget, error) {
	byKey := map[targetKey]resolvedTarget{}
	keys := []targetKey{}
	for i := range failingPods {
		pod := &failingPods[i]
		top, gvk, err := r.resolveTop(ctx, pod)
		if err != nil {
			// A NotFound owner may be transiently absent; return to requeue.
			return nil, err
		}
		ref := targetRefFor(top, gvk)
		key := keyFor(ref)
		if _, seen := byKey[key]; !seen {
			byKey[key] = resolvedTarget{obj: top, gvk: gvk}
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.namespace != b.namespace {
			return a.namespace < b.namespace
		}
		return a.name < b.name
	})
	out := make([]resolvedTarget, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out, nil
}

// handleTarget evaluates one top-level object and patches it when appropriate.
// The decision is always based on the object's pod template, never on the
// images of individual pods, so mixed old/new pods during a rollout cannot
// cause a repeated rewrite.
func (r *AlternateImageSourceReconciler) handleTarget(
	ctx context.Context,
	log logr.Logger,
	ais *saffirev1alpha1.AlternateImageSource,
	target resolvedTarget,
	badRepositories map[string]bool,
	prior saffirev1alpha1.TargetResult,
) saffirev1alpha1.TargetResult {
	ref := targetRefFor(target.obj, target.gvk)
	key := keyFor(ref)
	result := saffirev1alpha1.TargetResult{Target: ref}

	podSpec, ok := podSpecOf(target.obj)
	if !ok {
		return mergePrior(prior, saffirev1alpha1.TargetResult{
			Target: ref,
			Phase:  saffirev1alpha1.PhaseSkipped,
			Reason: saffirev1alpha1.ReasonUnsupportedKind,
		})
	}

	decisions, rewrites := evaluateContainers(podSpec, ais.Spec.ImageSourceReplacements, badRepositories)
	result.Containers = decisions

	if _, isJob := target.obj.(*batchv1.Job); isJob {
		// A Job's pod template is immutable once created; never attempt it.
		result.Phase = saffirev1alpha1.PhaseSkipped
		result.Reason = saffirev1alpha1.ReasonImmutableTemplate
		return mergePrior(prior, result)
	}

	if !isRewritableTopLevelKind(target.gvk.Kind) {
		result.Phase = saffirev1alpha1.PhaseSkipped
		result.Reason = saffirev1alpha1.ReasonUnsupportedKind
		return mergePrior(prior, result)
	}

	if len(rewrites) == 0 {
		// The template already holds the desired image (or nothing matches).
		// Keep the previous audit record intact so converged reconciles do not
		// rewrite status; a target we have never seen is reported as skipped.
		if prior.Target.Name != "" {
			return prior
		}
		result.Phase = saffirev1alpha1.PhaseSkipped
		result.Reason = saffirev1alpha1.ReasonAlreadyConverged
		return result
	}

	if r.thrash.shouldThrottle(key) {
		// Continue reporting the throttled state but preserve the patch history.
		result.Phase = saffirev1alpha1.PhaseThrashing
		result.Reason = saffirev1alpha1.ReasonThrottled
		result.PatchAttempts = toMetav1Times(r.thrash.attemptsLockedView(key))
		result.Rollout = rolloutOf(target.obj)
		return mergePrior(prior, result)
	}

	base := target.obj.DeepCopyObject().(client.Object)
	applyRewrites(podSpec, rewrites)

	if err := r.strategicPatch(ctx, target.obj, base); err != nil {
		log.Error(err, "unable to patch target", "kind", target.gvk.Kind, "name", target.obj.GetName())
		result.Phase = saffirev1alpha1.PhaseError
		result.Reason = err.Error()
		return mergePrior(prior, result)
	}

	attempts := r.thrash.recordAttempt(key)
	now := metav1.NewTime(r.now().UTC())
	result.LastPatchTime = &now
	result.PatchAttempts = toMetav1Times(attempts)
	result.Rollout = rolloutOf(target.obj)
	if len(attempts) >= thrashThreshold {
		// The write itself happened; flag that further writes are now suppressed
		// and an external actor appears to be reverting us.
		result.Phase = saffirev1alpha1.PhaseThrashing
		result.Reason = saffirev1alpha1.ReasonThrashing
	} else {
		result.Phase = saffirev1alpha1.PhaseTemplatePatched
	}
	return result
}

// mergePrior overlays a freshly computed result on top of the previous audit
// record, keeping historical patch time and attempts when the new result did
// not perform a write.
func mergePrior(prior, fresh saffirev1alpha1.TargetResult) saffirev1alpha1.TargetResult {
	if prior.Target.Name == "" {
		return fresh
	}
	merged := fresh
	if merged.LastPatchTime == nil {
		merged.LastPatchTime = prior.LastPatchTime
	}
	if len(merged.PatchAttempts) == 0 {
		merged.PatchAttempts = prior.PatchAttempts
	}
	return merged
}

// ensureTracker lazily builds the thrash tracker and clock so callers that
// construct the reconciler without test doubles still get a working default.
func (r *AlternateImageSourceReconciler) ensureTracker() {
	if r.nowFunc == nil {
		r.nowFunc = time.Now
	}
	if r.thrash == nil {
		r.thrash = newThrashTracker(r.nowFunc)
	}
}

func (r *AlternateImageSourceReconciler) now() time.Time {
	r.ensureTracker()
	return r.nowFunc()
}

// collectBadRepositories extracts the repositories referenced by containers
// currently in an image pull failure state.
func collectBadRepositories(pod *corev1.Pod, into map[string]bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting == nil {
			continue
		}
		if status.State.Waiting.Reason != "ErrImagePull" && status.State.Waiting.Reason != "ImagePullBackOff" {
			continue
		}
		// Map the status name back to the spec container to get its image.
		for _, container := range pod.Spec.Containers {
			if container.Name != status.Name {
				continue
			}
			if repository, _, err := parseImageString(container.Image); err == nil {
				into[repository] = true
			}
		}
	}
}

// PodToAlternateImageSource enqueues the AIS objects of a namespace when a pod
// in it experiences an image pull error.
func (r *AlternateImageSourceReconciler) PodToAlternateImageSource(o client.Object) []ctrl.Request {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		r.Log.Error(nil, "expected a Pod", "got", o.GetObjectKind().GroupVersionKind())
		return nil
	}
	if !r.podHasImagePullErr(pod) {
		return nil
	}
	return r.requestAISInNamespace(pod.Namespace)
}

// SetupWithManager sets up the reconciler.
func (r *AlternateImageSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&saffirev1alpha1.AlternateImageSource{}).
		Watches(
			&source.Kind{Type: &corev1.Pod{}},
			handler.EnqueueRequestsFromMapFunc(r.PodToAlternateImageSource),
		).
		Complete(r)
}

// podHasImagePullErr reports whether any container is blocked on an image pull.
func (r *AlternateImageSourceReconciler) podHasImagePullErr(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting == nil {
			continue
		}
		if status.State.Waiting.Reason == "ErrImagePull" || status.State.Waiting.Reason == "ImagePullBackOff" {
			return true
		}
	}
	return false
}

// requestAISInNamespace requests reconciliation of any AlternateImageSources in a namespace.
func (r *AlternateImageSourceReconciler) requestAISInNamespace(namespace string) []ctrl.Request {
	log := r.Log.WithValues("requestAISInNamespace", namespace)
	var list saffirev1alpha1.AlternateImageSourceList
	result := []ctrl.Request{}
	if err := r.List(context.Background(), &list, client.InNamespace(namespace)); err != nil {
		log.Error(err, "error getting AlternateImageSources in namespace")
		return nil
	}
	for _, ais := range list.Items {
		result = append(result, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: ais.Namespace, Name: ais.Name}})
	}
	return result
}
