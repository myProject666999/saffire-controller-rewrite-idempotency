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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

var testStart = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func reconcileOnce(t *testing.T, r *AlternateImageSourceReconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Namespace: testNamespace, Name: "ais"},
	})
	require.NoError(t, err)
}

func getDeployment(t *testing.T, c client.Client, name string) *appsv1.Deployment {
	t.Helper()
	d := &appsv1.Deployment{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, d))
	return d
}

func getAIS(t *testing.T, c client.Client) *saffirev1alpha1.AlternateImageSource {
	t.Helper()
	ais := &saffirev1alpha1.AlternateImageSource{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "ais"}, ais))
	return ais
}

// Scenario 1: Deployment template is rewritten; annotations, labels and
// unrelated container fields are preserved.
func TestDeploymentTemplateRewritePreservesOtherFields(t *testing.T) {
	deploy := newDeployment(plainContainer("app", oldImage))
	pod := newOwnedPod("web-pod", deploy, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, plainContainer("app", oldImage))
	// Owner chain: Pod -> ReplicaSet -> Deployment.
	rs := newReplicaSetFor(deploy)
	pod.OwnerReferences[0].Name = rs.Name

	r, counting, _ := newTestReconciler(testStart, newAIS(), deploy, rs, pod)

	reconcileOnce(t, r)

	updated := getDeployment(t, r.Client, "web")
	assert.Equal(t, newImage, updated.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "preserved", updated.Spec.Template.Annotations["keep.me/annotation"])
	assert.Equal(t, "web", updated.Spec.Template.Labels["app"])
	assert.Equal(t, []string{"/bin/keep"}, updated.Spec.Template.Spec.Containers[0].Command)
	assert.Equal(t, []corev1.EnvVar{{Name: "PRESERVE", Value: "me"}}, updated.Spec.Template.Spec.Containers[0].Env)
	assert.NotEqual(t, "100", updated.ResourceVersion, "patch must bump resourceVersion")

	calls := counting.Snapshot()
	assert.Equal(t, 1, calls.Patches)
	assert.Equal(t, 0, calls.Updates, "workloads are patched, never fully updated")
	assert.Equal(t, 1, calls.StatusUpdates)

	// The failing pod itself was never touched: fake seeds objects without an
	// explicit RV at "999", and an unwritten pod keeps that exact value.
	podObj := &corev1.Pod{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "web-pod"}, podObj))
	assert.Equal(t, "999", podObj.ResourceVersion, "owned pod must not be rewritten")
}

// Scenario 2: a second identical reconcile performs no writes at all and the
// workload resourceVersion does not change.
func TestIdempotentSecondReconcileZeroWrites(t *testing.T) {
	deploy := newDeployment(plainContainer("app", oldImage))
	rs := newReplicaSetFor(deploy)
	pod := newOwnedPod("web-pod", rs, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, plainContainer("app", oldImage))

	r, counting, clock := newTestReconciler(testStart, newAIS(), deploy, rs, pod)

	reconcileOnce(t, r)
	rvAfterFirst := getDeployment(t, r.Client, "web").ResourceVersion
	counting.Reset()
	clock.advance(time.Minute)

	reconcileOnce(t, r)

	assert.Equal(t, rvAfterFirst, getDeployment(t, r.Client, "web").ResourceVersion)
	calls := counting.Snapshot()
	assert.Equal(t, 0, calls.Patches, "repeat reconcile must not patch the workload")
	assert.Equal(t, 0, calls.Updates)
	assert.Equal(t, 0, calls.StatusUpdates, "unchanged status must not be rewritten")
	assert.Equal(t, 0, calls.StatusPatches)
}

// Scenario 3: Job templates are immutable and must be skipped and reported.
func TestJobImmutableSkipped(t *testing.T) {
	job := newJob(plainContainer("migrate", oldImage))
	pod := newOwnedPod("job-pod", job, metav1.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, plainContainer("migrate", oldImage))

	r, counting, _ := newTestReconciler(testStart, newAIS(), job, pod)

	reconcileOnce(t, r)

	jobObj := &batchv1.Job{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "migrate"}, jobObj))
	assert.Equal(t, oldImage, jobObj.Spec.Template.Spec.Containers[0].Image, "Job template must never be modified")
	assert.Equal(t, "400", jobObj.ResourceVersion)

	calls := counting.Snapshot()
	assert.Equal(t, 0, calls.Patches)
	assert.Equal(t, 0, calls.Updates)
	assert.Equal(t, 1, calls.StatusUpdates, "the skip decision is still audited")

	ais := getAIS(t, r.Client)
	require.Len(t, ais.Status.Results, 1)
	result := ais.Status.Results[0]
	assert.Equal(t, "Job", result.Target.Kind)
	assert.Equal(t, saffirev1alpha1.PhaseSkipped, result.Phase)
	assert.Equal(t, saffirev1alpha1.ReasonImmutableTemplate, result.Reason)
}

// Scenario 4: StatefulSet with the OnDelete strategy is still patched once,
// but its rollout block reports manual=true.
func TestStatefulSetOnDeleteReportsManual(t *testing.T) {
	sts := newStatefulSet(appsv1.OnDeleteStatefulSetStrategyType, plainContainer("db", oldImage))
	pod := newOwnedPod("db-pod", sts, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, plainContainer("db", oldImage))

	r, counting, _ := newTestReconciler(testStart, newAIS(), sts, pod)

	reconcileOnce(t, r)

	stsObj := &appsv1.StatefulSet{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "db"}, stsObj))
	assert.Equal(t, newImage, stsObj.Spec.Template.Spec.Containers[0].Image)

	calls := counting.Snapshot()
	assert.Equal(t, 1, calls.Patches)
	assert.Equal(t, 0, calls.Updates)

	ais := getAIS(t, r.Client)
	require.Len(t, ais.Status.Results, 1)
	result := ais.Status.Results[0]
	assert.Equal(t, saffirev1alpha1.PhaseTemplatePatched, result.Phase)
	require.NotNil(t, result.Rollout)
	assert.True(t, result.Rollout.Manual, "OnDelete must be reported as a manual rollout")
}

// Scenario 5: a StatefulSet using RollingUpdate reports manual=false.
func TestStatefulSetRollingUpdateReportsAutomatic(t *testing.T) {
	sts := newStatefulSet(appsv1.RollingUpdateStatefulSetStrategyType, plainContainer("db", oldImage))
	pod := newOwnedPod("db-pod", sts, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, plainContainer("db", oldImage))

	r, _, _ := newTestReconciler(testStart, newAIS(), sts, pod)
	reconcileOnce(t, r)

	result := getAIS(t, r.Client).Status.Results[0]
	require.NotNil(t, result.Rollout)
	assert.False(t, result.Rollout.Manual)
}

// Scenario 6: standalone (ownerless) pods are rewritten directly.
func TestStandalonePodRewrittenDirectly(t *testing.T) {
	pod := newStandalonePod("lonely", plainContainer("app", oldImage))
	r, counting, _ := newTestReconciler(testStart, newAIS(), pod)

	reconcileOnce(t, r)

	podObj := &corev1.Pod{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "lonely"}, podObj))
	assert.Equal(t, newImage, podObj.Spec.Containers[0].Image)
	assert.Equal(t, 1, counting.Snapshot().Patches)

	result := getAIS(t, r.Client).Status.Results[0]
	assert.Equal(t, "Pod", result.Target.Kind)
	assert.Equal(t, saffirev1alpha1.PhaseTemplatePatched, result.Phase)
}

// Scenario 7: an external syncer reverts every patch. The controller patches
// three times inside the window, then stops and reports Thrashing; after the
// window slides out it is allowed exactly one retry.
func TestThrashingDetectorStopsRepeatedPatches(t *testing.T) {
	deploy := newDeployment(plainContainer("app", oldImage))
	rs := newReplicaSetFor(deploy)
	pod := newOwnedPod("web-pod", rs, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, plainContainer("app", oldImage))

	r, counting, clock := newTestReconciler(testStart, newAIS(), deploy, rs, pod)

	// Emulate a declarative syncer that restores the original image after each patch.
	counting.PatchHook = func(obj client.Object) {
		if d, ok := obj.(*appsv1.Deployment); ok {
			d.Spec.Template.Spec.Containers[0].Image = oldImage
			require.NoError(t, r.Client.Update(context.Background(), d))
		}
	}

	// First three reconciles each perform one patch (the third crosses the threshold).
	for i := 0; i < 3; i++ {
		clock.advance(time.Minute)
		reconcileOnce(t, r)
	}
	assert.Equal(t, 3, counting.Snapshot().Patches)

	third := getAIS(t, r.Client).Status.Results[0]
	assert.Equal(t, saffirev1alpha1.PhaseThrashing, third.Phase)
	assert.Equal(t, saffirev1alpha1.ReasonThrashing, third.Reason)
	require.Len(t, third.PatchAttempts, 3)

	// Further reconciles inside the window must not write anything.
	counting.Reset()
	clock.advance(time.Minute)
	reconcileOnce(t, r)
	assert.Equal(t, 0, counting.Snapshot().Patches, "throttled target must not be patched again")
	throttled := getAIS(t, r.Client).Status.Results[0]
	assert.Equal(t, saffirev1alpha1.PhaseThrashing, throttled.Phase)
	assert.Equal(t, saffirev1alpha1.ReasonThrottled, throttled.Reason)

	// Once the window expires, exactly one retry patch is allowed.
	counting.Reset()
	clock.advance(thrashWindow + time.Second)
	reconcileOnce(t, r)
	assert.Equal(t, 1, counting.Snapshot().Patches)
}

// Scenario 8: one undecidable container stays untouched while a sibling is
// rewritten; the audit lists a decision per container.
func TestPartialRewriteUndecidableContainerUntouched(t *testing.T) {
	containers := []corev1.Container{
		plainContainer("good", oldImage),
		plainContainer("weird", "no-tag-here"),
	}
	deploy := newDeployment(containers...)
	rs := newReplicaSetFor(deploy)
	pod := newOwnedPod("web-pod", rs, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, containers...)

	r, counting, _ := newTestReconciler(testStart, newAIS(), deploy, rs, pod)
	reconcileOnce(t, r)

	updated := getDeployment(t, r.Client, "web")
	assert.Equal(t, newImage, updated.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "no-tag-here", updated.Spec.Template.Spec.Containers[1].Image, "undecidable container is left as-is")
	assert.Equal(t, 1, counting.Snapshot().Patches)

	decisions := getAIS(t, r.Client).Status.Results[0].Containers
	require.Len(t, decisions, 2)
	assert.Equal(t, saffirev1alpha1.DecisionRewritten, decisions[0].Decision)
	assert.Equal(t, newImage, decisions[0].NewImage)
	assert.Equal(t, saffirev1alpha1.DecisionUndecidable, decisions[1].Decision)
	assert.Equal(t, saffirev1alpha1.ReasonUnparseableImage, decisions[1].Reason)
}

// Scenario 9: CronJob template is patched on the jobTemplate path and the
// jobTemplate metadata is preserved.
func TestCronJobTemplateRewrite(t *testing.T) {
	cron := newCronJob(plainContainer("night", oldImage))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-27000000",
			Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cron, batchv1.SchemeGroupVersion.WithKind("CronJob")),
			},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{plainContainer("night", oldImage)}}}},
	}
	// Chain: Pod -> Job -> CronJob.
	pod := newOwnedPod("cron-pod", job, metav1.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, plainContainer("night", oldImage))

	r, counting, _ := newTestReconciler(testStart, newAIS(), cron, job, pod)
	reconcileOnce(t, r)

	cronObj := &batchv1.CronJob{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "nightly"}, cronObj))
	assert.Equal(t, newImage, cronObj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "yes", cronObj.Spec.JobTemplate.Annotations["cron/keep"])
	assert.Equal(t, "0 * * * *", cronObj.Spec.Schedule)
	assert.Equal(t, 1, counting.Snapshot().Patches)

	result := getAIS(t, r.Client).Status.Results[0]
	assert.Equal(t, "CronJob", result.Target.Kind)
	assert.Nil(t, result.Rollout, "CronJobs have no workload rollout block")
}

// Scenario 10: DaemonSet template rewrite and rollout reporting.
func TestDaemonSetRewriteAndRollout(t *testing.T) {
	ds := newDaemonSet(plainContainer("agent", oldImage))
	pod := newOwnedPod("agent-pod", ds, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, plainContainer("agent", oldImage))

	r, _, _ := newTestReconciler(testStart, newAIS(), ds, pod)
	reconcileOnce(t, r)

	dsObj := &appsv1.DaemonSet{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "agent"}, dsObj))
	assert.Equal(t, newImage, dsObj.Spec.Template.Spec.Containers[0].Image)

	result := getAIS(t, r.Client).Status.Results[0]
	assert.Equal(t, saffirev1alpha1.PhaseTemplatePatched, result.Phase)
	require.NotNil(t, result.Rollout)
	assert.Equal(t, dsObj.Generation, result.Rollout.Generation)
}

// Scenario 11: a pod whose top controller is an unsupported kind (here an
// orphan ReplicaSet) is reported skipped and never written.
func TestUnsupportedTopLevelKindSkipped(t *testing.T) {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan-rs", Namespace: testNamespace, ResourceVersion: "700"},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{plainContainer("app", oldImage)}}},
		},
	}
	pod := newOwnedPod("orphan-pod", rs, metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, plainContainer("app", oldImage))

	r, counting, _ := newTestReconciler(testStart, newAIS(), rs, pod)
	reconcileOnce(t, r)

	rsObj := &appsv1.ReplicaSet{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "orphan-rs"}, rsObj))
	assert.Equal(t, oldImage, rsObj.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "700", rsObj.ResourceVersion)
	assert.Equal(t, 0, counting.Snapshot().Patches)

	result := getAIS(t, r.Client).Status.Results[0]
	assert.Equal(t, "ReplicaSet", result.Target.Kind)
	assert.Equal(t, saffirev1alpha1.PhaseSkipped, result.Phase)
	assert.Equal(t, saffirev1alpha1.ReasonUnsupportedKind, result.Reason)
}

// Scenario 12: when no pods are failing, the reconcile produces no writes.
func TestNoFailingPodsNoWrites(t *testing.T) {
	deploy := newDeployment(plainContainer("app", oldImage))
	rs := newReplicaSetFor(deploy)
	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pod", Namespace: testNamespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{plainContainer("app", oldImage)}},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	r, counting, _ := newTestReconciler(testStart, newAIS(), deploy, rs, healthyPod)

	reconcileOnce(t, r)

	assert.Equal(t, 0, counting.Snapshot().Patches)
	assert.Equal(t, 0, counting.Snapshot().Updates)
	// First reconcile still sets observedGeneration via one status write.
	assert.Equal(t, 1, counting.Snapshot().StatusUpdates)

	counting.Reset()
	reconcileOnce(t, r)
	assert.Equal(t, 0, counting.Snapshot().StatusUpdates, "converged empty results must not be re-written")
}
