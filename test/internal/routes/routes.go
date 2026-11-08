// Package routes reads Gateway API routes' status
package routes

// Condition is the part of a Kubernetes status condition the tests read.
type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// ConditionsMet reports whether every wanted condition type has status True.
func ConditionsMet(conditions []Condition, types ...string) bool {
	status := map[string]string{}
	for _, c := range conditions {
		status[c.Type] = c.Status
	}
	for _, t := range types {
		if status[t] != "True" {
			return false
		}
	}
	return true
}

// HTTPRoute is the part of a Gateway API HTTPRoute the tests read.
type HTTPRoute struct {
	Spec struct {
		Hostnames []string `json:"hostnames"`
	} `json:"spec"`
	Status struct {
		Parents []struct {
			Conditions []Condition `json:"conditions"`
		} `json:"parents"`
	} `json:"status"`
}

// Ready reports whether the route has a hostname and every parent Gateway accepted it with resolved backends.
func (r HTTPRoute) Ready() bool {
	if len(r.Spec.Hostnames) == 0 || len(r.Status.Parents) == 0 {
		return false
	}
	for _, p := range r.Status.Parents {
		if !ConditionsMet(p.Conditions, "Accepted", "ResolvedRefs") {
			return false
		}
	}
	return true
}
