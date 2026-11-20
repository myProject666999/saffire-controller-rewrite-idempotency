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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

func TestEvaluateContainersPerContainerDecisions(t *testing.T) {
	replacements := []saffirev1alpha1.ImageSourceReplacement{
		{EquivalentRepositories: []string{oldRepository, newRepository}},
	}
	podSpec := &corev1.PodSpec{Containers: []corev1.Container{
		{Name: "rewrite-me", Image: oldImage},
		{Name: "no-rule", Image: "other.registry/app:v2"},
		{Name: "broken", Image: "unparseable"},
		{Name: "already-good", Image: newImage},
	}}
	bad := map[string]bool{oldRepository: true}

	decisions, rewrites := evaluateContainers(podSpec, replacements, bad)

	require.Len(t, decisions, 4)
	assert.Equal(t, saffirev1alpha1.DecisionRewritten, decisions[0].Decision)
	assert.Equal(t, newImage, decisions[0].NewImage)
	assert.Equal(t, newImage, rewrites[0])

	assert.Equal(t, saffirev1alpha1.DecisionSkipped, decisions[1].Decision)
	assert.Equal(t, saffirev1alpha1.ReasonNoMatchingRule, decisions[1].Reason)
	_, hasOne := rewrites[1]
	assert.False(t, hasOne)

	assert.Equal(t, saffirev1alpha1.DecisionUndecidable, decisions[2].Decision)
	_, hasTwo := rewrites[2]
	assert.False(t, hasTwo)

	assert.Equal(t, saffirev1alpha1.DecisionAlreadyTarget, decisions[3].Decision)

	// Evaluation must not mutate the spec on its own.
	assert.Equal(t, oldImage, podSpec.Containers[0].Image)
	assert.Equal(t, "unparseable", podSpec.Containers[2].Image)

	applyRewrites(podSpec, rewrites)
	assert.Equal(t, newImage, podSpec.Containers[0].Image)
	assert.Equal(t, "unparseable", podSpec.Containers[2].Image, "undecidable container stays untouched")
}

func TestThrashTrackerWindowAndRehydrate(t *testing.T) {
	base := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: base}
	tracker := newThrashTracker(clock.now)
	ref := saffirev1alpha1.TargetRef{Group: "apps", Kind: "Deployment", Name: "web", Namespace: "default"}
	key := keyFor(ref)

	for i := 0; i < thrashThreshold; i++ {
		assert.False(t, tracker.shouldThrottle(key), "not throttled before threshold")
		tracker.recordAttempt(key)
		clock.advance(time.Minute)
	}
	assert.True(t, tracker.shouldThrottle(key), "threshold reached -> throttled")

	// After the window slides out, pruning drops the old attempts.
	clock.advance(thrashWindow + time.Second)
	assert.False(t, tracker.shouldThrottle(key), "window expiry releases the throttle")

	// Rehydrate from status restores an in-window history across a restart.
	fresh := newThrashTracker(clock.now)
	fresh.rehydrate([]saffirev1alpha1.TargetResult{{
		Target:        ref,
		PatchAttempts: []metav1.Time{metav1.NewTime(clock.now().Add(-time.Minute))},
	}})
	assert.Len(t, fresh.attemptsLockedView(key), 1)
}
