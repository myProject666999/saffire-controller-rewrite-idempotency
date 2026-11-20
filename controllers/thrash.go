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
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

const (
	// thrashWindow is the sliding window used to detect an external actor
	// reverting our patches.
	thrashWindow = 10 * time.Minute
	// thrashThreshold is the number of patches inside the window after which
	// the controller stops rewriting the target until the window slides out.
	thrashThreshold = 3
)

// thrashState keeps the patch timestamps observed for one target.
type thrashState struct {
	attempts []time.Time
}

// thrashTracker records per-target patch attempts. It is in-memory: under
// leader election a single controller instance owns the map. The timestamps
// are also mirrored onto each AIS status so a restart can rehydrate instead
// of resetting detection (see rehydrate).
type thrashTracker struct {
	mu     sync.Mutex
	now    func() time.Time
	states map[targetKey]*thrashState
}

func newThrashTracker(now func() time.Time) *thrashTracker {
	if now == nil {
		now = time.Now
	}
	return &thrashTracker{
		now:    now,
		states: make(map[targetKey]*thrashState),
	}
}

// pruneLocked drops attempts older than the window. Caller must hold the lock.
func (t *thrashTracker) pruneLocked(key targetKey) []time.Time {
	state, ok := t.states[key]
	if !ok {
		return nil
	}
	cutoff := t.now().Add(-thrashWindow)
	kept := state.attempts[:0]
	for _, ts := range state.attempts {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	state.attempts = kept
	return kept
}

// shouldThrottle reports whether the target must not be patched in this
// reconcile because the thrash threshold is currently reached.
func (t *thrashTracker) shouldThrottle(key targetKey) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pruneLocked(key)) >= thrashThreshold
}

// recordAttempt stores a new patch timestamp and returns the attempts still
// inside the window after pruning.
func (t *thrashTracker) recordAttempt(key targetKey) []time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.states[key]
	if !ok {
		state = &thrashState{}
		t.states[key] = state
	}
	state.attempts = append(state.attempts, t.now())
	return t.pruneLocked(key)
}

// attemptsLockedView returns the current in-window timestamps for a target.
func (t *thrashTracker) attemptsLockedView(key targetKey) []time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]time.Time(nil), t.pruneLocked(key)...)
}

// rehydrate rebuilds the in-memory window from the patchAttempts persisted on
// AIS statuses so a process restart does not erase thrash detection. Entries
// older than the window are ignored.
func (t *thrashTracker) rehydrate(results []saffirev1alpha1.TargetResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	cutoff := now.Add(-thrashWindow)
	for _, result := range results {
		key := keyFor(result.Target)
		state := &thrashState{}
		for _, ts := range result.PatchAttempts {
			if ts.Time.After(cutoff) {
				state.attempts = append(state.attempts, ts.Time)
			}
		}
		if len(state.attempts) > 0 {
			t.states[key] = state
		}
	}
}

// windowTimestamps converts in-window attempt times to UTC metav1 times for
// durable status persistence.
func toMetav1Times(times []time.Time) []metav1.Time {
	if len(times) == 0 {
		return nil
	}
	out := make([]metav1.Time, 0, len(times))
	for _, ts := range times {
		out = append(out, metav1.NewTime(ts.UTC()))
	}
	return out
}
