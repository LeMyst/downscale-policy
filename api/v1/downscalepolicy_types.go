package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// DownscalePolicySpec declares the kube-downscaler schedule for the namespace
// the policy is created in. Each field maps 1:1 to a `downscaler/*` annotation
// that the operator manages on the Namespace object, so users who cannot edit
// the Namespace itself can still control downscaling through this resource.
//
// Timespan format follows kube-downscaler, e.g. "Mon-Fri 08:00-19:00 Europe/Paris",
// "Sat-Sun 00:00-24:00 UTC", an absolute range
// "2026-12-24T00:00:00+01:00-2026-12-26T00:00:00+01:00", "always" or "never".
// Multiple timespans can be comma-separated.
// +kubebuilder:validation:XValidation:rule="has(self.uptime) || has(self.downtime) || has(self.upscalePeriod) || has(self.downscalePeriod) || has(self.forceUptime) || has(self.forceDowntime) || has(self.downtimeReplicas) || has(self.exclude) || has(self.excludeUntil)",message="at least one field must be set"
type DownscalePolicySpec struct {
	// Uptime is the timespan during which workloads in the namespace run
	// normally; outside of it they are scaled down.
	// Maps to the `downscaler/uptime` annotation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Uptime string `json:"uptime,omitempty"`

	// Downtime is the timespan during which workloads in the namespace are
	// scaled down; outside of it they run normally.
	// Maps to the `downscaler/downtime` annotation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Downtime string `json:"downtime,omitempty"`

	// UpscalePeriod is a timespan during which workloads are scaled up once;
	// outside of it the downscaler leaves them untouched.
	// Maps to the `downscaler/upscale-period` annotation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	UpscalePeriod string `json:"upscalePeriod,omitempty"`

	// DownscalePeriod is a timespan during which workloads are scaled down
	// once; outside of it the downscaler leaves them untouched.
	// Maps to the `downscaler/downscale-period` annotation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	DownscalePeriod string `json:"downscalePeriod,omitempty"`

	// ForceUptime forces workloads into their normal (up) state regardless of
	// other schedules. Either the string "true"/"false" or a timespan.
	// Maps to the `downscaler/force-uptime` annotation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	ForceUptime string `json:"forceUptime,omitempty"`

	// ForceDowntime forces workloads into their downscaled state regardless of
	// other schedules. Either the string "true"/"false" or a timespan.
	// Maps to the `downscaler/force-downtime` annotation.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	ForceDowntime string `json:"forceDowntime,omitempty"`

	// DowntimeReplicas is the replica count workloads are scaled down to
	// during downtime. Either an integer (e.g. 1) or a percentage of the
	// current replicas (e.g. "20%"). When unset, kube-downscaler's default
	// (0) applies. Maps to the `downscaler/downtime-replicas` annotation.
	// +optional
	DowntimeReplicas *intstr.IntOrString `json:"downtimeReplicas,omitempty"`

	// Exclude excludes the whole namespace from downscaling while true.
	// Maps to the `downscaler/exclude` annotation.
	// +optional
	Exclude *bool `json:"exclude,omitempty"`

	// ExcludeUntil excludes the namespace from downscaling until the given
	// timestamp, e.g. "2026-08-31" or "2026-08-31T18:00:00Z".
	// Maps to the `downscaler/exclude-until` annotation.
	// +optional
	// +kubebuilder:validation:Pattern=`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}(:\d{2})?(Z|[+-]\d{2}:?\d{2})?)?$`
	ExcludeUntil string `json:"excludeUntil,omitempty"`
}

// DownscalePolicyPhase describes the lifecycle state of a policy.
// +kubebuilder:validation:Enum=Active;Failed
type DownscalePolicyPhase string

const (
	// PolicyPhaseActive means this policy is the one applied to the namespace.
	PolicyPhaseActive DownscalePolicyPhase = "Active"
	// PolicyPhaseFailed means the policy is not applied, e.g. because another
	// policy already exists in the namespace.
	PolicyPhaseFailed DownscalePolicyPhase = "Failed"
)

// Condition types and reasons used in DownscalePolicyStatus.
const (
	// ConditionReady indicates whether the policy's annotations are applied
	// to the namespace.
	ConditionReady = "Ready"

	ReasonAnnotationsApplied  = "AnnotationsApplied"
	ReasonConflictingPolicies = "ConflictingPolicies"
)

// DownscalePolicyStatus defines the observed state of DownscalePolicy.
type DownscalePolicyStatus struct {
	// ObservedGeneration is the spec generation last processed by the operator.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a high-level summary: Active when this policy drives the
	// namespace annotations, Failed when it conflicts with another policy.
	// +optional
	Phase DownscalePolicyPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the policy state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dsp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Uptime",type=string,JSONPath=`.spec.uptime`
// +kubebuilder:printcolumn:name="Downtime",type=string,JSONPath=`.spec.downtime`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DownscalePolicy lets namespace users manage the kube-downscaler annotations
// of their namespace without needing update permission on the Namespace object.
type DownscalePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DownscalePolicySpec   `json:"spec,omitempty"`
	Status DownscalePolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DownscalePolicyList contains a list of DownscalePolicy.
type DownscalePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DownscalePolicy `json:"items"`
}
