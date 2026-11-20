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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Phase describes what the controller did to a target object during a reconcile.
type Phase string

const (
	// PhaseTemplatePatched means the pod template of the target was patched with new images.
	PhaseTemplatePatched Phase = "TemplatePatched"
	// PhaseSkipped means the target was evaluated but not written to.
	PhaseSkipped Phase = "Skipped"
	// PhaseThrashing means an external actor keeps reverting our patch and the
	// controller has stopped rewriting this target within the current window.
	PhaseThrashing Phase = "Thrashing"
	// PhaseError means an unexpected error occurred while handling the target.
	PhaseError Phase = "Error"
)

// Decision describes what happened to a single container.
type Decision string

const (
	// DecisionRewritten means the container image was changed to an equivalent repository.
	DecisionRewritten Decision = "Rewritten"
	// DecisionAlreadyTarget means the container already runs an equivalent repository.
	DecisionAlreadyTarget Decision = "AlreadyTarget"
	// DecisionSkipped means the container did not match any rule.
	DecisionSkipped Decision = "Skipped"
	// DecisionUndecidable means the container image could not be parsed or evaluated.
	// The container is always left untouched in this case.
	DecisionUndecidable Decision = "Undecidable"
)

// Reasons attached to phases or container decisions.
const (
	ReasonImmutableTemplate     = "ImmutableTemplate"
	ReasonUnsupportedKind       = "UnsupportedKind"
	ReasonAlreadyConverged      = "AlreadyConverged"
	ReasonNoMatchingRule        = "NoMatchingRule"
	ReasonNoAlternateRepository = "NoAlternateRepository"
	ReasonUnparseableImage      = "UnparseableImage"
	ReasonThrashing             = "ThrashingDetected"
	ReasonThrottled             = "ThrashingThrottled"
)

// ImageSourceReplacement is a single replacement
type ImageSourceReplacement struct {
	// EquivalentRepositories is a list of possible replacement repositories
	// they should each have the same set of tags available
	EquivalentRepositories []string `json:"equivalentRepositories"`
}

// AlternateImageSourceSpec defines the desired state of AlternateImageSource
type AlternateImageSourceSpec struct {
	ImageSourceReplacements []ImageSourceReplacement `json:"imageSourceReplacements"`
}

// ContainerDecision records the per-container outcome of an evaluation.
type ContainerDecision struct {
	// Name is the name of the container inside the pod template.
	Name string `json:"name"`
	// Decision is what the controller did (or decided not to do) with this container.
	Decision Decision `json:"decision"`
	// Reason optionally explains why the decision was taken.
	Reason string `json:"reason,omitempty"`
	// OldImage is the image found in the template before rewriting.
	OldImage string `json:"oldImage,omitempty"`
	// NewImage is the image written to the template, when Decision is Rewritten.
	NewImage string `json:"newImage,omitempty"`
}

// RolloutStatus reports whether the workload controller has applied the
// template change. Template write success and rollout are reported separately.
type RolloutStatus struct {
	// Generation is the metadata.generation of the workload after the template patch.
	Generation int64 `json:"generation"`
	// ObservedGeneration is the generation the workload controller reports having processed.
	ObservedGeneration int64 `json:"observedGeneration"`
	// UpdatedReplicas is the number of replicas currently running the updated template.
	UpdatedReplicas int32 `json:"updatedReplicas"`
	// DesiredReplicas is the number of replicas the workload is configured to run.
	DesiredReplicas int32 `json:"desiredReplicas"`
	// Manual is true when the workload update strategy does not roll pods
	// automatically (e.g. a StatefulSet using the OnDelete strategy).
	Manual bool `json:"manual,omitempty"`
}

// TargetRef identifies the controller object a result belongs to.
type TargetRef struct {
	Group     string    `json:"group"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	UID       types.UID `json:"uid,omitempty"`
}

// TargetResult is the audit record for a single top-level controller object.
// Audit data lives on the AlternateImageSource status, never inside the
// workload's pod template, so recording an audit never triggers a rollout.
type TargetResult struct {
	// Target identifies the workload or standalone pod.
	Target TargetRef `json:"target"`
	// Phase is the overall outcome for this target.
	Phase Phase `json:"phase"`
	// Reason optionally explains the phase.
	Reason string `json:"reason,omitempty"`
	// Containers holds one decision per container in the pod template.
	Containers []ContainerDecision `json:"containers,omitempty"`
	// LastPatchTime is the last time this controller patched the target.
	LastPatchTime *metav1.Time `json:"lastPatchTime,omitempty"`
	// PatchAttempts lists the patch timestamps still inside the thrashing window.
	PatchAttempts []metav1.Time `json:"patchAttempts,omitempty"`
	// Rollout reports rollout progress for workload kinds that support it.
	Rollout *RolloutStatus `json:"rollout,omitempty"`
}

// AlternateImageSourceStatus defines the observed state of AlternateImageSource
type AlternateImageSourceStatus struct {
	// ObservedGeneration is the last observed generation of the object
	ObservedGeneration int64 `json:"observedGeneration"`
	// Results holds one audit entry per top-level controller object evaluated.
	Results []TargetResult `json:"results,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AlternateImageSource is the Schema for the alternateimagesources API
type AlternateImageSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AlternateImageSourceSpec   `json:"spec,omitempty"`
	Status AlternateImageSourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AlternateImageSourceList contains a list of AlternateImageSource
type AlternateImageSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AlternateImageSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AlternateImageSource{}, &AlternateImageSourceList{})
}
