package admission_test

import (
	"context"
	"fmt"

	"github.com/dexadata/dexaflow/pkg/admission"
)

// A hook that runs each tenant's tasks on that tenant's nodes, holds work
// during a freeze window and refuses tasks that ask for more than 8 CPUs.
func Example() {
	frozen := func() bool { return true } // ask your change calendar
	hook := admission.HookFunc(func(_ context.Context, req *admission.Request) (admission.Decision, error) {
		if frozen() {
			return admission.Hold("weekly maintenance freeze until 06:00 UTC"), nil
		}
		if req.Resources.Requests.CPU == "16" {
			return admission.Refuse("tasks may request at most 8 CPUs; lower resources.requests.cpu"), nil
		}
		return admission.AdmitWith(admission.Changes{
			NodeSelector: map[string]string{"tenant": req.TenantID},
			Tolerations:  []admission.Toleration{{Key: "tenant", Operator: "Equal", Value: req.TenantID, Effect: "NoSchedule"}},
			Labels:       map[string]string{"cost-center": req.TenantID},
		}), nil
	})

	dec, _ := hook.Admit(context.Background(), &admission.Request{TenantID: "acme", DagID: "nightly_etl", TaskID: "extract"})
	reason, held := dec.Held()
	fmt.Println(held, reason)
	// Output: true weekly maintenance freeze until 06:00 UTC
}
