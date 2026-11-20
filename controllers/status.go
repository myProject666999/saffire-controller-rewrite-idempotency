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
	"reflect"

	"k8s.io/apimachinery/pkg/types"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

// normalizeStatus canonicalizes the slice shapes inside a status so that the
// semantically-important content is what gets compared. The fake client used
// in tests round-trips empty slices to nil; a real API server does the same
// for fields tagged omitempty. Without this, an identical reconcile would
// appear to change the status and write on every loop.
func normalizeStatus(in saffirev1alpha1.AlternateImageSourceStatus) saffirev1alpha1.AlternateImageSourceStatus {
	out := in.DeepCopy()
	if len(out.Results) == 0 {
		out.Results = nil
	}
	for i := range out.Results {
		if len(out.Results[i].Containers) == 0 {
			out.Results[i].Containers = nil
		}
		if len(out.Results[i].PatchAttempts) == 0 {
			out.Results[i].PatchAttempts = nil
		}
	}
	return *out
}

// updateStatusIfChanged writes the AIS status only when it actually differs
// from the stored one. A repeat reconcile with identical inputs therefore
// performs no status write at all (and no resourceVersion bump). A fresh copy
// is always read first so a stale spec is never carried into the status call.
func (r *AlternateImageSourceReconciler) updateStatusIfChanged(
	ctx context.Context,
	key types.NamespacedName,
	newStatus saffirev1alpha1.AlternateImageSourceStatus,
) error {
	fresh := &saffirev1alpha1.AlternateImageSource{}
	if err := r.Get(ctx, key, fresh); err != nil {
		return err
	}
	if reflect.DeepEqual(normalizeStatus(fresh.Status), normalizeStatus(newStatus)) {
		return nil
	}
	fresh.Status = *newStatus.DeepCopy()
	return r.Status().Update(ctx, fresh)
}
