package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setCondition sets a condition on a slice, preserving LastTransitionTime when
// the status has not changed.
//
// meta.SetStatusCondition does this correctly, so this only exists to supply
// ObservedGeneration consistently — a condition without it cannot be
// distinguished from a stale one.
func setCondition(conditions *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// conditionTrue reports whether a condition is present and True.
func conditionTrue(conditions []metav1.Condition, condType string) bool {
	return meta.IsStatusConditionTrue(conditions, condType)
}

// findCondition returns a condition, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	return meta.FindStatusCondition(conditions, condType)
}
