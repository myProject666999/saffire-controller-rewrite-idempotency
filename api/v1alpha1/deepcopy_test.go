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

package v1alpha1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestStatusDeepCopyIsolation ensures the hand-written DeepCopy of the new
// status types fully detaches slices and pointers.
func TestStatusDeepCopyIsolation(t *testing.T) {
	patchTime := metav1.NewTime(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	original := AlternateImageSourceStatus{
		ObservedGeneration: 2,
		Results: []TargetResult{{
			Target:        TargetRef{Group: "apps", Kind: "Deployment", Name: "web", Namespace: "default"},
			Phase:         PhaseTemplatePatched,
			Containers:    []ContainerDecision{{Name: "app", Decision: DecisionRewritten, NewImage: "docker.io/x:v1"}},
			LastPatchTime: &patchTime,
			PatchAttempts: []metav1.Time{patchTime},
			Rollout:       &RolloutStatus{Generation: 2, ObservedGeneration: 1, UpdatedReplicas: 1, DesiredReplicas: 3},
		}},
	}

	copied := original.DeepCopy()
	require.NotNil(t, copied)

	// Mutate every reference-typed field of the copy; the original must be untouched.
	copied.Results[0].Containers[0].Name = "mutated"
	copied.Results[0].PatchAttempts[0] = metav1.NewTime(time.Time{})
	copied.Results[0].LastPatchTime.Time = time.Time{}
	copied.Results[0].Rollout.Generation = 99
	copied.Results[0].Target.Name = "mutated"

	assert.Equal(t, "app", original.Results[0].Containers[0].Name)
	assert.Equal(t, "web", original.Results[0].Target.Name)
	assert.Equal(t, patchTime, original.Results[0].PatchAttempts[0])
	assert.Equal(t, patchTime, *original.Results[0].LastPatchTime)
	assert.Equal(t, int64(2), original.Results[0].Rollout.Generation)

	// Root object DeepCopy must implement runtime.Object semantics.
	root := &AlternateImageSource{Status: original}
	objCopy := root.DeepCopyObject().(*AlternateImageSource)
	objCopy.Status.Results[0].Phase = PhaseThrashing
	assert.Equal(t, PhaseTemplatePatched, root.Status.Results[0].Phase)
}
