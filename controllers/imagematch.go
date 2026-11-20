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
	corev1 "k8s.io/api/core/v1"

	saffirev1alpha1 "github.com/fairwindsops/saffire/api/v1alpha1"
)

// findEquivalentRepositories returns the equivalentRepositories list that
// contains the given repository, or nil when no rule matches.
func findEquivalentRepositories(repository string, replacements []saffirev1alpha1.ImageSourceReplacement) []string {
	for _, replacement := range replacements {
		if containsString(replacement.EquivalentRepositories, repository) {
			return replacement.EquivalentRepositories
		}
	}
	return nil
}

// evaluateContainers evaluates every container of a pod template against the
// AIS rules and the set of repositories currently failing to pull. It never
// mutates the pod spec: rewrites maps container index to the desired new image
// and the caller applies it only when the target is writable.
//
// A container whose image cannot be parsed is marked Undecidable and never
// appears in rewrites; the remaining containers are still evaluated.
func evaluateContainers(
	podSpec *corev1.PodSpec,
	replacements []saffirev1alpha1.ImageSourceReplacement,
	badRepositories map[string]bool,
) (decisions []saffirev1alpha1.ContainerDecision, rewrites map[int]string) {
	rewrites = map[int]string{}

	for idx := range podSpec.Containers {
		container := &podSpec.Containers[idx]
		decision := saffirev1alpha1.ContainerDecision{
			Name:     container.Name,
			OldImage: container.Image,
		}

		repository, _, err := parseImageString(container.Image)
		if err != nil {
			decision.Decision = saffirev1alpha1.DecisionUndecidable
			decision.Reason = saffirev1alpha1.ReasonUnparseableImage
			decisions = append(decisions, decision)
			continue
		}

		equivalentRepositories := findEquivalentRepositories(repository, replacements)
		if equivalentRepositories == nil {
			decision.Decision = saffirev1alpha1.DecisionSkipped
			decision.Reason = saffirev1alpha1.ReasonNoMatchingRule
			decisions = append(decisions, decision)
			continue
		}

		// The template already runs an equivalent repository that is not the
		// failing one: a previous switch has converged, never rewrite again.
		if !badRepositories[repository] {
			decision.Decision = saffirev1alpha1.DecisionAlreadyTarget
			decisions = append(decisions, decision)
			continue
		}

		newImage, err := getNewImage(container.Image, equivalentRepositories)
		if err != nil {
			decision.Decision = saffirev1alpha1.DecisionSkipped
			decision.Reason = saffirev1alpha1.ReasonNoAlternateRepository
			decisions = append(decisions, decision)
			continue
		}

		if newImage == container.Image {
			decision.Decision = saffirev1alpha1.DecisionAlreadyTarget
			decision.NewImage = newImage
			decisions = append(decisions, decision)
			continue
		}

		decision.Decision = saffirev1alpha1.DecisionRewritten
		decision.NewImage = newImage
		decisions = append(decisions, decision)
		rewrites[idx] = newImage
	}

	return decisions, rewrites
}

// applyRewrites writes the desired images onto the pod spec in place. Only the
// image field of the named containers changes; everything else is preserved.
func applyRewrites(podSpec *corev1.PodSpec, rewrites map[int]string) {
	for idx, image := range rewrites {
		podSpec.Containers[idx].Image = image
	}
}
