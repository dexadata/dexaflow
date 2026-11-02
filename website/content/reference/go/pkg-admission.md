---
title: "pkg/admission"
linkTitle: "pkg/admission"
weight: 15
---

```go
import "github.com/dexadata/dexaflow/pkg/admission"
```

Package admission is the contract of the dispatch admission hook \(ADR 0100\): operator code that admits, changes or refuses each task attempt after the server's own checks and before the attempt's agent token is minted.

A hook sees a copy of the attempt \([Request](<#Request>)\) and answers with a [Decision](<#Decision>): [Admit](<#Admit>) runs it as it is, [AdmitWith](<#AdmitWith>) runs it with the pod placement and metadata changed, [Hold](<#Hold>) keeps it waiting and offers it again, and [Refuse](<#Refuse>) fails the task for good with a reason the task instance shows. An error means the hook itself is broken: the attempt is held too, so a hook that is briefly down never lets a task run unchecked, unless the operator chose to fail open.

What a hook may change is limited on purpose: the node selector, tolerations, priority class, service account, and labels and annotations outside the keys Dexaflow reserves. The image, the command, the environment, the namespace and the resources stay as the server chose them, so the attempt is charged the pool slots it was admitted with.

Everything exported here follows the pkg/ compatibility rule of ADR 0099: no breaking change in a patch release.

<details><summary>Example</summary>
<p>

A hook that runs each tenant's tasks on that tenant's nodes, holds work during a freeze window and refuses tasks that ask for more than 8 CPUs.

```go
package main

import (
	"context"
	"fmt"

	"github.com/dexadata/dexaflow/pkg/admission"
)

func main() {
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
}
```

#### Output

```
true weekly maintenance freeze until 06:00 UTC
```

</p>
</details>

## Index

- [func IsReservedKey\(key string\) bool](<#IsReservedKey>)
- [func ReservedPrefixes\(\) \[\]string](<#ReservedPrefixes>)
- [type Changes](<#Changes>)
  - [func \(c Changes\) IsZero\(\) bool](<#Changes.IsZero>)
- [type Decision](<#Decision>)
  - [func Admit\(\) Decision](<#Admit>)
  - [func AdmitWith\(c Changes\) Decision](<#AdmitWith>)
  - [func Hold\(reason string\) Decision](<#Hold>)
  - [func Refuse\(reason string\) Decision](<#Refuse>)
  - [func \(d Decision\) Changes\(\) Changes](<#Decision.Changes>)
  - [func \(d Decision\) Held\(\) \(string, bool\)](<#Decision.Held>)
  - [func \(d Decision\) Refused\(\) \(string, bool\)](<#Decision.Refused>)
- [type Execution](<#Execution>)
- [type Hook](<#Hook>)
- [type HookFunc](<#HookFunc>)
  - [func \(f HookFunc\) Admit\(ctx context.Context, req \*Request\) \(Decision, error\)](<#HookFunc.Admit>)
- [type Quantity](<#Quantity>)
- [type Request](<#Request>)
- [type Resources](<#Resources>)
- [type Toleration](<#Toleration>)


<a name="IsReservedKey"></a>
## func [IsReservedKey](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L138>)

```go
func IsReservedKey(key string) bool
```

IsReservedKey reports whether a label or annotation key falls under one of ReservedPrefixes.

<a name="ReservedPrefixes"></a>
## func [ReservedPrefixes](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L134>)

```go
func ReservedPrefixes() []string
```

ReservedPrefixes returns the label and annotation key prefixes a hook may not set: the server decides what a pod is from keys under them.

<a name="Changes"></a>
## type [Changes](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L115-L128>)

Changes are what a hook may change on the attempt. A zero field changes nothing: map entries are merged over the task's own \(the hook's keys win\), tolerations are appended, and a non\-empty string replaces the task's value.

```go
type Changes struct {
    NodeSelector map[string]string
    Tolerations  []Toleration
    // PriorityClassName and ServiceAccount must be allowed by the server's
    // execution allowlist (execution_allowlist.priority_classes and
    // service_accounts) when those lists are set.
    PriorityClassName string
    // ServiceAccount must be allowed by the server's execution allowlist
    // (execution_allowlist.service_accounts) when that list is set.
    ServiceAccount string
    // Labels and Annotations may not use a key under one of ReservedPrefixes.
    Labels      map[string]string
    Annotations map[string]string
}
```

<a name="Changes.IsZero"></a>
### func \(Changes\) [IsZero](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L148>)

```go
func (c Changes) IsZero() bool
```

IsZero reports whether c changes nothing.

<a name="Decision"></a>
## type [Decision](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L162-L166>)

Decision is a hook's answer. Its zero value admits the attempt unchanged.

```go
type Decision struct {
    // contains filtered or unexported fields
}
```

<a name="Admit"></a>
### func [Admit](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L169>)

```go
func Admit() Decision
```

Admit runs the attempt as it is.

<a name="AdmitWith"></a>
### func [AdmitWith](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L172>)

```go
func AdmitWith(c Changes) Decision
```

AdmitWith runs the attempt with c applied.

<a name="Hold"></a>
### func [Hold](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L179>)

```go
func Hold(reason string) Decision
```

Hold keeps the attempt waiting and offers it again later, for a condition that passes \(a freeze window, a budget that resets\). It is not a failure of the hook: it never fails open and never counts toward skipping the hook. A held attempt waits within the same window as an unreachable cluster \(ADR 0093\) and fails with reason once that window is spent.

<a name="Refuse"></a>
### func [Refuse](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L183>)

```go
func Refuse(reason string) Decision
```

Refuse fails the task for good, without retries, and the task instance shows reason. For a temporary condition use Hold.

<a name="Decision.Changes"></a>
### func \(Decision\) [Changes](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L192>)

```go
func (d Decision) Changes() Changes
```

Changes returns what d changes on an admitted attempt.

<a name="Decision.Held"></a>
### func \(Decision\) [Held](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L189>)

```go
func (d Decision) Held() (string, bool)
```

Held reports whether d holds the attempt, and why.

<a name="Decision.Refused"></a>
### func \(Decision\) [Refused](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L186>)

```go
func (d Decision) Refused() (string, bool)
```

Refused reports whether d refuses the attempt, and why.

<a name="Execution"></a>
## type [Execution](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L91-L98>)

Execution is the subset of a task's execution settings a hook sees.

```go
type Execution struct {
    ServiceAccount    string
    NodeSelector      map[string]string
    Tolerations       []Toleration
    PriorityClassName string
    Labels            map[string]string
    Annotations       map[string]string
}
```

<a name="Hook"></a>
## type [Hook](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L33-L35>)

Hook admits, changes, holds or refuses one task attempt. The server calls Admit once per attempt, with a deadline on ctx, and stops waiting when the deadline passes. A hook must return once ctx is done: one that ignores it keeps running in the background after the server moved on. Admit must not keep req after it returns.

```go
type Hook interface {
    Admit(ctx context.Context, req *Request) (Decision, error)
}
```

<a name="HookFunc"></a>
## type [HookFunc](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L38>)

HookFunc lets an ordinary function be a Hook.

```go
type HookFunc func(ctx context.Context, req *Request) (Decision, error)
```

<a name="HookFunc.Admit"></a>
### func \(HookFunc\) [Admit](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L41>)

```go
func (f HookFunc) Admit(ctx context.Context, req *Request) (Decision, error)
```

Admit calls f\(ctx, req\).

<a name="Quantity"></a>
## type [Quantity](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L84-L88>)

Quantity holds Kubernetes quantities, for example "500m" or "1Gi". An empty string means not set.

```go
type Quantity struct {
    CPU              string
    Memory           string
    EphemeralStorage string
}
```

<a name="Request"></a>
## type [Request](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L48-L74>)

Request describes the attempt about to run. It is a copy: changing it has no effect, a hook answers with a Decision instead. It never carries the task's environment, its source, secrets or the attempt's token.

```go
type Request struct {
    // TenantID, DagID, RunID and TaskID name the attempt; TryNumber counts its
    // tries from 1.
    TenantID  string
    DagID     string
    RunID     string
    TaskID    string
    TryNumber int
    // Operator is the task's operator type, for example "python" or "bash".
    Operator string
    // Image is the image the run resolved, the one the pod would run.
    Image string
    // SourceMode reports whether a Cluster attempt runs the DAG source on the
    // operator's runtime image (ADR 0067). Lite always runs from source and
    // reports false.
    SourceMode bool
    // Resources are the effective requests and limits: the task's own, or the
    // resource unit's or the platform defaults' where the server applies them.
    Resources Resources
    // Execution is the task's placement and pod metadata, with the service
    // account after the server's default is applied.
    Execution Execution
    // ServiceAccountChosen reports whether the task named its service account
    // itself; false means Execution.ServiceAccount is the server's default (or
    // empty when no default is configured).
    ServiceAccountChosen bool
}
```

<a name="Resources"></a>
## type [Resources](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L77-L80>)

Resources are the task pod's requests and limits.

```go
type Resources struct {
    Requests Quantity
    Limits   Quantity
}
```

<a name="Toleration"></a>
## type [Toleration](<https://github.com/dexadata/dexaflow/blob/main/pkg/admission/admission.go#L102-L110>)

Toleration is a Kubernetes pod toleration. Empty fields are left out of the pod.

```go
type Toleration struct {
    Key      string
    Operator string // "Exists" or "Equal"; empty means Equal
    Value    string
    Effect   string // "NoSchedule", "PreferNoSchedule", "NoExecute" or empty for all
    // TolerationSeconds, for NoExecute only, is how long the pod stays bound
    // to a node that gains the taint; nil means forever.
    TolerationSeconds *int64
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
