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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

const (
	testNamespace = "default"
	oldRepository = "quay.io/fairwinds/demo"
	newRepository = "docker.io/fairwinds/demo"
	imageTag      = "v1.0.0"
	oldImage      = oldRepository + ":" + imageTag
	newImage      = newRepository + ":" + imageTag
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = saffirev1alpha1.AddToScheme(s)
	return s
}

func newAIS() *saffirev1alpha1.AlternateImageSource {
	return &saffirev1alpha1.AlternateImageSource{
		ObjectMeta: metav1.ObjectMeta{Name: "ais", Namespace: testNamespace, Generation: 1, ResourceVersion: "1"},
		Spec: saffirev1alpha1.AlternateImageSourceSpec{
			ImageSourceReplacements: []saffirev1alpha1.ImageSourceReplacement{
				{EquivalentRepositories: []string{oldRepository, newRepository}},
			},
		},
	}
}

func failingContainerStatus(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
		},
	}
}

func int32Ptr(i int32) *int32 { return &i }

func newDeployment(containers ...corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: testNamespace, ResourceVersion: "100"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(2),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": "web"},
					Annotations: map[string]string{"keep.me/annotation": "preserved"},
				},
				Spec: corev1.PodSpec{Containers: containers},
			},
		},
	}
}

func newStatefulSet(strategy appsv1.StatefulSetUpdateStrategyType, containers ...corev1.Container) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace, ResourceVersion: "200"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(2),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: strategy,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}},
				Spec:       corev1.PodSpec{Containers: containers},
			},
		},
	}
}

func newDaemonSet(containers ...corev1.Container) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: testNamespace, ResourceVersion: "300"},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "agent"}},
				Spec:       corev1.PodSpec{Containers: containers},
			},
		},
	}
}

func newJob(containers ...corev1.Container) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: testNamespace, ResourceVersion: "400"},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers, RestartPolicy: corev1.RestartPolicyNever}},
		},
	}
}

func newCronJob(containers ...corev1.Container) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: testNamespace, ResourceVersion: "500"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 * * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"cron/keep": "yes"}},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers, RestartPolicy: corev1.RestartPolicyNever}},
				},
			},
		},
	}
}

func newReplicaSetFor(deploy *appsv1.Deployment) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploy.Name + "-abc123",
			Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(deploy, appsv1.SchemeGroupVersion.WithKind("Deployment")),
			},
		},
	}
}

func newOwnedPod(name string, owner metav1.Object, gvk metav1.GroupVersionKind, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: gvk.Group + "/" + gvk.Version,
				Kind:       gvk.Kind,
				Name:       owner.GetName(),
				UID:        owner.GetUID(),
				Controller: boolPtr(true),
			}},
		},
		Spec: corev1.PodSpec{Containers: containers},
		Status: corev1.PodStatus{
			ContainerStatuses: waitingStatusesFor(containers),
		},
	}
}

func newStandalonePod(name string, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, ResourceVersion: "600"},
		Spec:       corev1.PodSpec{Containers: containers},
		Status: corev1.PodStatus{
			ContainerStatuses: waitingStatusesFor(containers),
		},
	}
}

func waitingStatusesFor(containers []corev1.Container) []corev1.ContainerStatus {
	statuses := make([]corev1.ContainerStatus, 0, len(containers))
	for _, c := range containers {
		statuses = append(statuses, failingContainerStatus(c.Name))
	}
	return statuses
}

func boolPtr(b bool) *bool { return &b }

func plainContainer(name, image string) corev1.Container {
	return corev1.Container{
		Name:    name,
		Image:   image,
		Command: []string{"/bin/keep"},
		Env:     []corev1.EnvVar{{Name: "PRESERVE", Value: "me"}},
	}
}

// newTestReconciler builds a reconciler backed by a counting fake client. The
// returned clock lets tests advance time deterministically.
func newTestReconciler(now time.Time, objs ...client.Object) (*AlternateImageSourceReconciler, *CountingClient, *fakeClock) {
	clock := &fakeClock{t: now}
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		Build()
	counting := &CountingClient{Client: fakeClient}
	r := &AlternateImageSourceReconciler{
		Client:  counting,
		Scheme:  testScheme(),
		Log:     testLogger(),
		thrash:  newThrashTracker(clock.now),
		nowFunc: clock.now,
	}
	return r, counting, clock
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
