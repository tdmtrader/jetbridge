package tests

import (
	rbacv1 "k8s.io/api/rbac/v1"
)

// grants reports whether any of the role's rules allows verb on resource in
// the API group, directly or through "*".
func grants(role *rbacv1.ClusterRole, group, resource, verb string) bool {
	for _, rule := range role.Rules {
		if !contains(rule.APIGroups, group) || !contains(rule.Resources, resource) {
			continue
		}
		if contains(rule.Verbs, verb) || contains(rule.Verbs, "*") {
			return true
		}
	}
	return false
}
