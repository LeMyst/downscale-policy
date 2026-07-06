package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	downscalerv1 "github.com/LeMyst/downscale-policy/api/v1"
)

func newTestReconciler(t *testing.T, objs ...client.Object) *DownscalePolicyReconciler {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := downscalerv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&downscalerv1.DownscalePolicy{}).
		Build()
	return &DownscalePolicyReconciler{Client: c, Scheme: s, Recorder: events.NewFakeRecorder(100)}
}

func newNamespace(name string, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations}}
}

func newPolicy(namespace, name string, age time.Duration, spec downscalerv1.DownscalePolicySpec) *downscalerv1.DownscalePolicy {
	return &downscalerv1.DownscalePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Spec: spec,
	}
}

func reconcileOnce(t *testing.T, r *DownscalePolicyReconciler, namespace, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	})
	if err != nil {
		t.Fatalf("reconcile %s/%s: %v", namespace, name, err)
	}
}

func getNamespace(t *testing.T, r *DownscalePolicyReconciler, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, ns); err != nil {
		t.Fatalf("get namespace %s: %v", name, err)
	}
	return ns
}

func getPolicy(t *testing.T, r *DownscalePolicyReconciler, namespace, name string) *downscalerv1.DownscalePolicy {
	t.Helper()
	p := &downscalerv1.DownscalePolicy{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, p); err != nil {
		t.Fatalf("get policy %s/%s: %v", namespace, name, err)
	}
	return p
}

func TestSinglePolicyAppliesAnnotations(t *testing.T) {
	replicas := intstr.FromInt32(1)
	exclude := true
	policy := newPolicy("team-a", "office-hours", time.Hour, downscalerv1.DownscalePolicySpec{
		Uptime:           "Mon-Fri 08:00-19:00 Europe/Paris",
		DowntimeReplicas: &replicas,
		Exclude:          &exclude,
		ExcludeUntil:     "2026-08-31",
	})
	r := newTestReconciler(t, newNamespace("team-a", nil), policy)

	reconcileOnce(t, r, "team-a", "office-hours")

	ns := getNamespace(t, r, "team-a")
	want := map[string]string{
		"downscaler/uptime":            "Mon-Fri 08:00-19:00 Europe/Paris",
		"downscaler/downtime-replicas": "1",
		"downscaler/exclude":           "true",
		"downscaler/exclude-until":     "2026-08-31",
		annotationManagedBy:            "office-hours",
	}
	for k, v := range want {
		if got := ns.Annotations[k]; got != v {
			t.Errorf("namespace annotation %q = %q, want %q", k, got, v)
		}
	}

	p := getPolicy(t, r, "team-a", "office-hours")
	if p.Status.Phase != downscalerv1.PolicyPhaseActive {
		t.Errorf("phase = %q, want Active", p.Status.Phase)
	}
	if !meta.IsStatusConditionTrue(p.Status.Conditions, downscalerv1.ConditionReady) {
		t.Error("Ready condition should be True")
	}
	if len(p.Finalizers) != 1 || p.Finalizers[0] != finalizerName {
		t.Errorf("finalizers = %v, want [%s]", p.Finalizers, finalizerName)
	}
}

func TestConflictingPolicyMarkedFailed(t *testing.T) {
	older := newPolicy("team-a", "older", 2*time.Hour, downscalerv1.DownscalePolicySpec{Downtime: "Sat-Sun 00:00-24:00 UTC"})
	newer := newPolicy("team-a", "newer", time.Hour, downscalerv1.DownscalePolicySpec{Uptime: "Mon-Fri 08:00-19:00 UTC"})
	r := newTestReconciler(t, newNamespace("team-a", nil), older, newer)

	reconcileOnce(t, r, "team-a", "newer")
	reconcileOnce(t, r, "team-a", "older")

	p := getPolicy(t, r, "team-a", "newer")
	if p.Status.Phase != downscalerv1.PolicyPhaseFailed {
		t.Errorf("newer policy phase = %q, want Failed", p.Status.Phase)
	}
	cond := meta.FindStatusCondition(p.Status.Conditions, downscalerv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != downscalerv1.ReasonConflictingPolicies {
		t.Errorf("Ready condition = %+v, want False/ConflictingPolicies", cond)
	}
	if len(p.Finalizers) != 0 {
		t.Errorf("conflicting policy should have no finalizer, got %v", p.Finalizers)
	}

	ns := getNamespace(t, r, "team-a")
	if got := ns.Annotations["downscaler/downtime"]; got != "Sat-Sun 00:00-24:00 UTC" {
		t.Errorf("downtime annotation = %q, want value from the older policy", got)
	}
	if _, ok := ns.Annotations["downscaler/uptime"]; ok {
		t.Error("uptime annotation from the losing policy must not be applied")
	}
	if got := ns.Annotations[annotationManagedBy]; got != "older" {
		t.Errorf("managed-by = %q, want %q", got, "older")
	}
}

func TestManualDriftIsReverted(t *testing.T) {
	policy := newPolicy("team-a", "office-hours", time.Hour, downscalerv1.DownscalePolicySpec{
		Uptime: "Mon-Fri 08:00-19:00 UTC",
	})
	r := newTestReconciler(t, newNamespace("team-a", nil), policy)
	reconcileOnce(t, r, "team-a", "office-hours")

	// Simulate a user with namespace edit rights tampering with the annotations.
	ns := getNamespace(t, r, "team-a")
	ns.Annotations["downscaler/uptime"] = "always"
	ns.Annotations["downscaler/exclude"] = "true"
	if err := r.Update(context.Background(), ns); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r, "team-a", "office-hours")

	ns = getNamespace(t, r, "team-a")
	if got := ns.Annotations["downscaler/uptime"]; got != "Mon-Fri 08:00-19:00 UTC" {
		t.Errorf("uptime = %q, tampered value was not reverted", got)
	}
	if _, ok := ns.Annotations["downscaler/exclude"]; ok {
		t.Error("manually added exclude annotation was not removed")
	}
}

func TestUnmanagedAnnotationsAreLeftAlone(t *testing.T) {
	policy := newPolicy("team-a", "office-hours", time.Hour, downscalerv1.DownscalePolicySpec{
		Uptime: "Mon-Fri 08:00-19:00 UTC",
	})
	ns := newNamespace("team-a", map[string]string{"team": "a", "unrelated/annotation": "keep-me"})
	r := newTestReconciler(t, ns, policy)

	reconcileOnce(t, r, "team-a", "office-hours")

	got := getNamespace(t, r, "team-a")
	if got.Annotations["team"] != "a" || got.Annotations["unrelated/annotation"] != "keep-me" {
		t.Errorf("unrelated annotations were modified: %v", got.Annotations)
	}
}

func TestDeletionCleansUpAndPromotesNextPolicy(t *testing.T) {
	older := newPolicy("team-a", "older", 2*time.Hour, downscalerv1.DownscalePolicySpec{Downtime: "Sat-Sun 00:00-24:00 UTC"})
	newer := newPolicy("team-a", "newer", time.Hour, downscalerv1.DownscalePolicySpec{Uptime: "Mon-Fri 08:00-19:00 UTC"})
	r := newTestReconciler(t, newNamespace("team-a", nil), older, newer)
	reconcileOnce(t, r, "team-a", "older")
	reconcileOnce(t, r, "team-a", "newer")

	// Delete the active policy: the finalizer must strip its annotations.
	if err := r.Delete(context.Background(), getPolicy(t, r, "team-a", "older")); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "team-a", "older")

	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "older"},
		&downscalerv1.DownscalePolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("policy should be gone after finalizer removal, got err=%v", err)
	}
	ns := getNamespace(t, r, "team-a")
	if _, ok := ns.Annotations["downscaler/downtime"]; ok {
		t.Error("annotations of the deleted policy were not cleaned up")
	}

	// The remaining policy takes over on its next reconcile.
	reconcileOnce(t, r, "team-a", "newer")
	ns = getNamespace(t, r, "team-a")
	if got := ns.Annotations["downscaler/uptime"]; got != "Mon-Fri 08:00-19:00 UTC" {
		t.Errorf("promoted policy not applied, uptime = %q", got)
	}
	p := getPolicy(t, r, "team-a", "newer")
	if p.Status.Phase != downscalerv1.PolicyPhaseActive {
		t.Errorf("promoted policy phase = %q, want Active", p.Status.Phase)
	}
}

func TestSpecChangeRemovesStaleAnnotations(t *testing.T) {
	policy := newPolicy("team-a", "office-hours", time.Hour, downscalerv1.DownscalePolicySpec{
		Uptime:       "Mon-Fri 08:00-19:00 UTC",
		ExcludeUntil: "2026-08-31",
	})
	r := newTestReconciler(t, newNamespace("team-a", nil), policy)
	reconcileOnce(t, r, "team-a", "office-hours")

	p := getPolicy(t, r, "team-a", "office-hours")
	p.Spec = downscalerv1.DownscalePolicySpec{Downtime: "Sat-Sun 00:00-24:00 UTC"}
	if err := r.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r, "team-a", "office-hours")

	ns := getNamespace(t, r, "team-a")
	if got := ns.Annotations["downscaler/downtime"]; got != "Sat-Sun 00:00-24:00 UTC" {
		t.Errorf("downtime = %q, want new spec value", got)
	}
	for _, stale := range []string{"downscaler/uptime", "downscaler/exclude-until"} {
		if _, ok := ns.Annotations[stale]; ok {
			t.Errorf("stale annotation %q was not removed after spec change", stale)
		}
	}
}

func TestElectActivePolicy(t *testing.T) {
	now := metav1.Now()
	older := downscalerv1.DownscalePolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "b-older", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	newer := downscalerv1.DownscalePolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "a-newer", CreationTimestamp: now}}
	sameAge := downscalerv1.DownscalePolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "a-same-age", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	deleting := downscalerv1.DownscalePolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "0-deleting", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
		DeletionTimestamp: &now, Finalizers: []string{finalizerName}}}

	if got := electActivePolicy(nil); got != nil {
		t.Errorf("elect(nil) = %v, want nil", got)
	}
	if got := electActivePolicy([]downscalerv1.DownscalePolicy{newer, older}); got.Name != "b-older" {
		t.Errorf("elect = %q, want oldest %q", got.Name, "b-older")
	}
	if got := electActivePolicy([]downscalerv1.DownscalePolicy{older, sameAge}); got.Name != "a-same-age" {
		t.Errorf("elect = %q, want name tie-breaker %q", got.Name, "a-same-age")
	}
	if got := electActivePolicy([]downscalerv1.DownscalePolicy{deleting, older}); got.Name != "b-older" {
		t.Errorf("elect = %q, deleting policies must be skipped", got.Name)
	}
}

func TestDesiredAnnotations(t *testing.T) {
	percent := intstr.FromString("20%")
	exclude := false
	policy := &downscalerv1.DownscalePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "p"},
		Spec: downscalerv1.DownscalePolicySpec{
			Downtime:         "Mon-Fri 19:00-08:00 UTC",
			DowntimeReplicas: &percent,
			Exclude:          &exclude,
		},
	}
	got := desiredAnnotations(policy)
	if got["downscaler/downtime-replicas"] != "20%" {
		t.Errorf("downtime-replicas = %q, want 20%%", got["downscaler/downtime-replicas"])
	}
	if _, ok := got["downscaler/exclude"]; ok {
		t.Error("exclude=false must not produce an annotation")
	}
	if got[annotationManagedBy] != "p" {
		t.Errorf("managed-by = %q, want p", got[annotationManagedBy])
	}
	if len(got) != 3 {
		t.Errorf("unexpected annotations: %v", got)
	}
}
