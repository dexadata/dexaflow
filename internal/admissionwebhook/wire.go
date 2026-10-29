package admissionwebhook

import "github.com/dexadata/dexaflow/pkg/admission"

// The wire format of APIVersion. Field names are snake_case, like the rest
// of the Dexaflow API; an empty field is left out.

type review struct {
	APIVersion string       `json:"api_version"`
	Kind       string       `json:"kind"`
	Request    *wireRequest `json:"request,omitempty"`
}

type wireRequest struct {
	UID                  string        `json:"uid"`
	TenantID             string        `json:"tenant_id"`
	DagID                string        `json:"dag_id"`
	RunID                string        `json:"run_id"`
	TaskID               string        `json:"task_id"`
	TryNumber            int           `json:"try_number"`
	Operator             string        `json:"operator"`
	Image                string        `json:"image"`
	SourceMode           bool          `json:"source_mode"`
	Resources            wireResources `json:"resources"`
	Execution            wireExecution `json:"execution"`
	ServiceAccountChosen bool          `json:"service_account_chosen"`
}

type wireResources struct {
	Requests wireQuantity `json:"requests"`
	Limits   wireQuantity `json:"limits"`
}

type wireQuantity struct {
	CPU              string `json:"cpu,omitempty"`
	Memory           string `json:"memory,omitempty"`
	EphemeralStorage string `json:"ephemeral_storage,omitempty"`
}

type wireExecution struct {
	ServiceAccount    string            `json:"service_account,omitempty"`
	NodeSelector      map[string]string `json:"node_selector,omitempty"`
	Tolerations       []wireToleration  `json:"tolerations,omitempty"`
	PriorityClassName string            `json:"priority_class_name,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

type wireToleration struct {
	Key               string `json:"key,omitempty"`
	Operator          string `json:"operator,omitempty"`
	Value             string `json:"value,omitempty"`
	Effect            string `json:"effect,omitempty"`
	TolerationSeconds *int64 `json:"toleration_seconds,omitempty"`
}

// answer is the endpoint's reply.
type answer struct {
	APIVersion string      `json:"api_version"`
	Kind       string      `json:"kind"`
	Response   *wireAnswer `json:"response"`
}

type wireAnswer struct {
	UID string `json:"uid"`
	// Decision is "admit", "hold" or "refuse".
	Decision string `json:"decision"`
	// Reason is shown on the task instance for hold and refuse.
	Reason string `json:"reason,omitempty"`
	// Changes, for admit only, are what the hook changes.
	Changes *wireChanges `json:"changes,omitempty"`
}

type wireChanges struct {
	NodeSelector      map[string]string `json:"node_selector,omitempty"`
	Tolerations       []wireToleration  `json:"tolerations,omitempty"`
	PriorityClassName string            `json:"priority_class_name,omitempty"`
	ServiceAccount    string            `json:"service_account,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

func toWire(uid string, r *admission.Request) *wireRequest {
	q := func(x admission.Quantity) wireQuantity {
		return wireQuantity{CPU: x.CPU, Memory: x.Memory, EphemeralStorage: x.EphemeralStorage}
	}
	e := r.Execution
	return &wireRequest{
		UID:        uid,
		TenantID:   r.TenantID,
		DagID:      r.DagID,
		RunID:      r.RunID,
		TaskID:     r.TaskID,
		TryNumber:  r.TryNumber,
		Operator:   r.Operator,
		Image:      r.Image,
		SourceMode: r.SourceMode,
		Resources:  wireResources{Requests: q(r.Resources.Requests), Limits: q(r.Resources.Limits)},
		Execution: wireExecution{
			ServiceAccount:    e.ServiceAccount,
			NodeSelector:      e.NodeSelector,
			Tolerations:       tolerationsToWire(e.Tolerations),
			PriorityClassName: e.PriorityClassName,
			Labels:            e.Labels,
			Annotations:       e.Annotations,
		},
		ServiceAccountChosen: r.ServiceAccountChosen,
	}
}

func tolerationsToWire(in []admission.Toleration) []wireToleration {
	if in == nil {
		return nil
	}
	out := make([]wireToleration, len(in))
	for i, t := range in {
		out[i] = wireToleration(t)
	}
	return out
}

func (c *wireChanges) toChanges() admission.Changes {
	if c == nil {
		return admission.Changes{}
	}
	var tolerations []admission.Toleration
	for _, t := range c.Tolerations {
		tolerations = append(tolerations, admission.Toleration(t))
	}
	return admission.Changes{
		NodeSelector:      c.NodeSelector,
		Tolerations:       tolerations,
		PriorityClassName: c.PriorityClassName,
		ServiceAccount:    c.ServiceAccount,
		Labels:            c.Labels,
		Annotations:       c.Annotations,
	}
}
