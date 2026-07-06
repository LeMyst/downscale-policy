package controller

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downscalerv1 "github.com/LeMyst/downscale-policy/api/v1"
)

const (
	// annotationManagedBy marks a namespace with the name of the policy that
	// owns its downscaler annotations, so ownership survives operator restarts
	// and policy hand-overs.
	annotationManagedBy = "downscaler.io/managed-by"
	finalizerName       = "downscaler.io/finalizer"
)

// managedAnnotations is every downscaler annotation key the operator may set
// on a namespace. Keys in this set that are not produced by the active
// policy's spec are removed from the namespace, so manual edits cannot stick.
var managedAnnotations = []string{
	"downscaler/uptime",
	"downscaler/downtime",
	"downscaler/upscale-period",
	"downscaler/downscale-period",
	"downscaler/force-uptime",
	"downscaler/force-downtime",
	"downscaler/downtime-replicas",
	"downscaler/exclude",
	"downscaler/exclude-until",
}

// DownscalePolicyReconciler reconciles a DownscalePolicy object.
type DownscalePolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=downscaler.io,resources=downscalepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=downscaler.io,resources=downscalepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=downscaler.io,resources=downscalepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile applies the oldest DownscalePolicy of a namespace to the
// namespace's downscaler annotations and marks any younger policies as Failed.
func (r *DownscalePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &downscalerv1.DownscalePolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, policy)
	}

	policies := &downscalerv1.DownscalePolicyList{}
	if err := r.List(ctx, policies, client.InNamespace(policy.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing policies in namespace %q: %w", policy.Namespace, err)
	}

	active := electActivePolicy(policies.Items)
	if active == nil || active.Name != policy.Name {
		return r.markConflicting(ctx, policy, active)
	}
	return r.applyPolicy(ctx, policy)
}

// electActivePolicy returns the policy that should drive the namespace
// annotations: the oldest by creation time, name as tie-breaker. Policies
// being deleted are ignored so the next policy can take over immediately.
func electActivePolicy(policies []downscalerv1.DownscalePolicy) *downscalerv1.DownscalePolicy {
	candidates := make([]downscalerv1.DownscalePolicy, 0, len(policies))
	for _, p := range policies {
		if p.DeletionTimestamp.IsZero() {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		ti, tj := candidates[i].CreationTimestamp, candidates[j].CreationTimestamp
		if !ti.Equal(&tj) {
			return ti.Before(&tj)
		}
		return candidates[i].Name < candidates[j].Name
	})
	return &candidates[0]
}

// desiredAnnotations computes the namespace annotations a policy asks for,
// including the ownership marker.
func desiredAnnotations(policy *downscalerv1.DownscalePolicy) map[string]string {
	spec := policy.Spec
	desired := map[string]string{annotationManagedBy: policy.Name}
	set := func(key, value string) {
		if value != "" {
			desired[key] = value
		}
	}
	set("downscaler/uptime", spec.Uptime)
	set("downscaler/downtime", spec.Downtime)
	set("downscaler/upscale-period", spec.UpscalePeriod)
	set("downscaler/downscale-period", spec.DownscalePeriod)
	set("downscaler/force-uptime", spec.ForceUptime)
	set("downscaler/force-downtime", spec.ForceDowntime)
	if spec.DowntimeReplicas != nil {
		desired["downscaler/downtime-replicas"] = spec.DowntimeReplicas.String()
	}
	if spec.Exclude != nil && *spec.Exclude {
		desired["downscaler/exclude"] = "true"
	}
	set("downscaler/exclude-until", spec.ExcludeUntil)
	return desired
}

// applyPolicy syncs the namespace annotations with the active policy's spec
// and reverts any manual drift on the managed annotation keys.
func (r *DownscalePolicyReconciler) applyPolicy(ctx context.Context, policy *downscalerv1.DownscalePolicy) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(policy, finalizerName) {
		controllerutil.AddFinalizer(policy, finalizerName)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: policy.Namespace}, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("getting namespace %q: %w", policy.Namespace, err)
	}

	desired := desiredAnnotations(policy)
	base := ns.DeepCopy()
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	changed := false
	for k, v := range desired {
		if ns.Annotations[k] != v {
			ns.Annotations[k] = v
			changed = true
		}
	}
	for _, k := range managedAnnotations {
		if _, isDesired := desired[k]; isDesired {
			continue
		}
		if _, present := ns.Annotations[k]; present {
			delete(ns.Annotations, k)
			changed = true
		}
	}
	if changed {
		if err := r.Patch(ctx, ns, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("patching namespace %q: %w", ns.Name, err)
		}
		log.Info("synced downscaler annotations on namespace", "namespace", ns.Name)
		r.Recorder.Eventf(policy, corev1.EventTypeNormal, downscalerv1.ReasonAnnotationsApplied,
			"Applied downscaler annotations to namespace %q", ns.Name)
	}

	err := r.updateStatus(ctx, policy, downscalerv1.PolicyPhaseActive, metav1.ConditionTrue,
		downscalerv1.ReasonAnnotationsApplied,
		fmt.Sprintf("downscaler annotations applied to namespace %q", ns.Name))
	return ctrl.Result{}, err
}

// markConflicting flags a policy that lost the election as Failed and makes
// sure it holds no finalizer and owns no namespace annotations.
func (r *DownscalePolicyReconciler) markConflicting(ctx context.Context, policy, active *downscalerv1.DownscalePolicy) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(policy, finalizerName) {
		if err := r.cleanupNamespace(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(policy, finalizerName)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
	}

	activeName := "<none>"
	if active != nil {
		activeName = active.Name
	}
	msg := fmt.Sprintf("namespace %q already has an active DownscalePolicy %q; only the oldest policy per namespace is applied",
		policy.Namespace, activeName)
	if !meta.IsStatusConditionFalse(policy.Status.Conditions, downscalerv1.ConditionReady) {
		r.Recorder.Event(policy, corev1.EventTypeWarning, downscalerv1.ReasonConflictingPolicies, msg)
	}
	err := r.updateStatus(ctx, policy, downscalerv1.PolicyPhaseFailed, metav1.ConditionFalse,
		downscalerv1.ReasonConflictingPolicies, msg)
	return ctrl.Result{}, err
}

// finalize removes the policy's annotations from the namespace before letting
// the object go away.
func (r *DownscalePolicyReconciler) finalize(ctx context.Context, policy *downscalerv1.DownscalePolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, finalizerName) {
		return ctrl.Result{}, nil
	}
	if err := r.cleanupNamespace(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(policy, finalizerName)
	if err := r.Update(ctx, policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// cleanupNamespace strips the managed annotations from the namespace, but
// only if this policy is the current owner — another policy may already have
// taken over.
func (r *DownscalePolicyReconciler) cleanupNamespace(ctx context.Context, policy *downscalerv1.DownscalePolicy) error {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: policy.Namespace}, ns); err != nil {
		return client.IgnoreNotFound(err)
	}
	if ns.Annotations[annotationManagedBy] != policy.Name {
		return nil
	}
	base := ns.DeepCopy()
	delete(ns.Annotations, annotationManagedBy)
	for _, k := range managedAnnotations {
		delete(ns.Annotations, k)
	}
	if err := r.Patch(ctx, ns, client.MergeFrom(base)); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// updateStatus writes phase and Ready condition, avoiding no-op updates so
// reconciles settle instead of looping.
func (r *DownscalePolicyReconciler) updateStatus(ctx context.Context, policy *downscalerv1.DownscalePolicy,
	phase downscalerv1.DownscalePolicyPhase, status metav1.ConditionStatus, reason, message string) error {
	changed := meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               downscalerv1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
	})
	if policy.Status.Phase != phase {
		policy.Status.Phase = phase
		changed = true
	}
	if policy.Status.ObservedGeneration != policy.Generation {
		policy.Status.ObservedGeneration = policy.Generation
		changed = true
	}
	if !changed {
		return nil
	}
	if err := r.Status().Update(ctx, policy); err != nil {
		return fmt.Errorf("updating status: %w", err)
	}
	return nil
}

// mapNamespaceToPolicies re-reconciles every policy of a namespace whenever
// the namespace's annotations change, which is how manual edits get reverted.
func (r *DownscalePolicyReconciler) mapNamespaceToPolicies(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.policiesInNamespace(ctx, obj.GetName())
}

// mapPolicyToSiblings re-reconciles every policy in the same namespace, so
// deleting the active policy promotes the next-oldest one.
func (r *DownscalePolicyReconciler) mapPolicyToSiblings(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.policiesInNamespace(ctx, obj.GetNamespace())
}

func (r *DownscalePolicyReconciler) policiesInNamespace(ctx context.Context, namespace string) []reconcile.Request {
	policies := &downscalerv1.DownscalePolicyList{}
	if err := r.List(ctx, policies, client.InNamespace(namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "listing DownscalePolicies for watch mapping", "namespace", namespace)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(policies.Items))
	for _, p := range policies.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: p.Namespace, Name: p.Name},
		})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *DownscalePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&downscalerv1.DownscalePolicy{}).
		Named("downscalepolicy").
		Watches(
			&downscalerv1.DownscalePolicy{},
			handler.EnqueueRequestsFromMapFunc(r.mapPolicyToSiblings),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToPolicies),
			builder.WithPredicates(predicate.AnnotationChangedPredicate{}),
		).
		Complete(r)
}
