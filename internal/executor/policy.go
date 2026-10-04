package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/dexadata/dexaflow/internal/domain"
)

// ErrPolicyRefused is wrapped by every error Policy.Apply returns: the task
// asks for a pod the operator's executor policy does not allow (ADR 0063). It
// is permanent, so the dispatcher reports it as Refused and the task fails
// without dispatch retries.
var ErrPolicyRefused = errors.New("refused by executor policy")

// Policy is the operator's executor policy (ADR 0063, executor.policy). Force
// rules set a pod field regardless of the DAG; restrict rules refuse a task
// whose value is not allowed and never clamp it. Every rule is optional, and a
// nil or zero Policy changes nothing. Build one with ParsePolicy, which
// validates it and pre-parses the resource ceiling: a Policy built as a
// literal ignores Resources.Max. The zero value of each rule means "no rule";
// an empty but set list (allowed: []) allows nothing.
type Policy struct {
	// RuntimeClassName, when set, is forced on every task pod.
	RuntimeClassName string `yaml:"runtime_class_name"`
	// ServiceAccount forces or restricts the task pod's ServiceAccount.
	ServiceAccount ServiceAccountRule `yaml:"service_account"`
	// Placement forces node selection and tolerations, and restricts the DAG's
	// own placement and priority class.
	Placement PlacementRule `yaml:"placement"`
	// Metadata restricts the labels and annotations a DAG may put on its pod.
	Metadata MetadataRule `yaml:"metadata"`
	// Resources caps the task container's requests and limits.
	Resources ResourceRule `yaml:"resources"`
	// Images restricts the task image.
	Images ImageRule `yaml:"images"`

	// maxQuantities holds Resources.Max parsed once by ParsePolicy, keyed by
	// resource name, so Apply never re-parses the ceiling.
	maxQuantities map[corev1.ResourceName]resource.Quantity
}

// ServiceAccountRule forces or restricts the task pod's ServiceAccount.
type ServiceAccountRule struct {
	// Force, when set, replaces the DAG's (or the default) ServiceAccount.
	Force string `yaml:"force"`
	// Allowed, when set, lists the only ServiceAccounts a task may run as. It is
	// checked against the resolved value: the DAG's, or the operator default.
	Allowed []string `yaml:"allowed"`
}

// PlacementRule forces and restricts where a task pod may land.
type PlacementRule struct {
	// NodeSelector is merged into the pod's node selector; a key the DAG also
	// sets takes the policy's value.
	NodeSelector map[string]string `yaml:"node_selector"`
	// Tolerations are appended to the DAG's tolerations.
	Tolerations []map[string]any `yaml:"tolerations"`
	// AllowDAGPlacement false refuses a task that sets its own node_selector,
	// tolerations, affinity or topology_spread_constraints. Unset allows them.
	AllowDAGPlacement *bool `yaml:"allow_dag_placement"`
	// AllowedPriorityClasses, when set, lists the only priority classes a task
	// may name. A task that names none is always allowed.
	AllowedPriorityClasses []string `yaml:"allowed_priority_classes"`
}

// MetadataRule restricts the pod labels and annotations a DAG declares
// (execution.labels / execution.annotations). It applies to the DAG's keys
// only; the labels and annotations the executor sets itself are not checked.
type MetadataRule struct {
	// AllowedLabelPrefixes, when set, lists the key prefixes a DAG label must
	// start with. An empty list allows no DAG labels.
	AllowedLabelPrefixes *[]string `yaml:"allowed_label_prefixes"`
	// AllowedAnnotationPrefixes is the same rule for annotations.
	AllowedAnnotationPrefixes *[]string `yaml:"allowed_annotation_prefixes"`
}

// ResourceRule caps a task container's resources.
type ResourceRule struct {
	// Max is the ceiling for both requests and limits, per resource. A task
	// asking for more is refused; a task with no limit for a capped resource gets
	// the ceiling as its limit, so it is never unbounded.
	Max domain.ResourceQuantity `yaml:"max"`
}

// ImageRule restricts the task image.
type ImageRule struct {
	// Allowed lists the images a task may use. An entry ending in "/" is a
	// prefix ("registry.example.com/dags/"); any other entry must equal the
	// image's repository, the reference without its tag or digest
	// ("docker.io/library/python"). Unset allows any image.
	Allowed []string `yaml:"allowed"`
}

// ParsePolicy decodes and validates an executor policy document (the value of
// executor.policy in the server config). Unknown keys are an error, so a typo
// fails startup instead of silently enforcing nothing. An empty document is the
// zero Policy.
func ParsePolicy(doc []byte) (*Policy, error) {
	p := &Policy{}
	if len(bytes.TrimSpace(doc)) == 0 {
		return p, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("executor.policy: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("executor.policy: %w", err)
	}
	return p, nil
}

// validate checks every rule and pre-parses the resource ceiling.
func (p *Policy) validate() error {
	sa := p.ServiceAccount
	if slices.Contains(sa.Allowed, "") {
		return errors.New("service_account.allowed: empty name")
	}
	if sa.Force != "" && sa.Allowed != nil && !slices.Contains(sa.Allowed, sa.Force) {
		return fmt.Errorf("service_account.force %q is not in service_account.allowed", sa.Force)
	}
	for i, t := range p.Placement.Tolerations {
		if err := validateToleration(t); err != nil {
			return fmt.Errorf("placement.tolerations[%d]: %w", i, err)
		}
	}
	for name, list := range map[string]*[]string{
		"metadata.allowed_label_prefixes":      p.Metadata.AllowedLabelPrefixes,
		"metadata.allowed_annotation_prefixes": p.Metadata.AllowedAnnotationPrefixes,
	} {
		if list != nil && slices.Contains(*list, "") {
			return fmt.Errorf("%s: empty prefix (it would allow every key)", name)
		}
	}
	if slices.Contains(p.Images.Allowed, "") {
		return errors.New("images.allowed: empty entry (it would allow every image)")
	}
	maxQ, err := parseQuantities(p.Resources.Max)
	if err != nil {
		return fmt.Errorf("resources.max: %w", err)
	}
	p.maxQuantities = maxQ
	return nil
}

// validateToleration checks that a toleration decodes into the Kubernetes type
// and uses a known operator and effect, so the policy cannot produce a pod the
// apiserver rejects on every dispatch.
func validateToleration(raw map[string]any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("not a toleration: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // a misspelled key must not silently widen the toleration
	var t corev1.Toleration
	if err := dec.Decode(&t); err != nil {
		return fmt.Errorf("not a toleration: %w", err)
	}
	switch t.Operator {
	case "", corev1.TolerationOpEqual, corev1.TolerationOpExists:
	default:
		return fmt.Errorf("unknown operator %q", t.Operator)
	}
	switch t.Effect {
	case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
	default:
		return fmt.Errorf("unknown effect %q", t.Effect)
	}
	// The apiserver's own rules, so a policy cannot make every pod invalid.
	switch {
	case t.Key == "" && t.Operator != corev1.TolerationOpExists:
		return errors.New(`an empty key requires operator "Exists"`)
	case t.Operator == corev1.TolerationOpExists && t.Value != "":
		return errors.New(`operator "Exists" takes no value`)
	case t.TolerationSeconds != nil && t.Effect != corev1.TaintEffectNoExecute:
		return errors.New(`tolerationSeconds requires effect "NoExecute"`)
	}
	return nil
}

// IsZero reports whether the policy has no rule at all.
func (p *Policy) IsZero() bool {
	if p == nil {
		return true
	}
	return p.RuntimeClassName == "" &&
		p.ServiceAccount.Force == "" && p.ServiceAccount.Allowed == nil &&
		len(p.Placement.NodeSelector) == 0 && len(p.Placement.Tolerations) == 0 &&
		p.Placement.AllowDAGPlacement == nil && p.Placement.AllowedPriorityClasses == nil &&
		p.Metadata.AllowedLabelPrefixes == nil && p.Metadata.AllowedAnnotationPrefixes == nil &&
		len(p.maxQuantities) == 0 && p.Images.Allowed == nil
}

// RequiresDedicatedPod reports whether the policy forces a pod field a warm
// worker does not carry (runtime class, placement, ServiceAccount, resource
// limits). Until warm
// pods apply the force rules themselves, such a policy keeps every task on the
// dedicated pod path so a warm worker cannot become a way around it.
func (p *Policy) RequiresDedicatedPod() bool {
	if p == nil {
		return false
	}
	return p.RuntimeClassName != "" || p.ServiceAccount.Force != "" ||
		len(p.Placement.NodeSelector) > 0 || len(p.Placement.Tolerations) > 0 ||
		len(p.maxQuantities) > 0
}

// Apply enforces the policy on a dispatch request: force rules rewrite req,
// restrict rules return an error wrapping ErrPolicyRefused that names the field
// and the offending value. It is applied after the operator's defaults (default
// ServiceAccount, L0 resources), so those are held to the policy too. A nil or
// zero policy returns nil and leaves req untouched. Apply never writes through
// maps or pointers req shares with the DAG spec or with the policy.
func (p *Policy) Apply(req *Request) error {
	if p.IsZero() {
		return nil
	}
	if err := p.checkImage(req.Image); err != nil {
		return err
	}
	if err := p.applyServiceAccount(&req.Execution); err != nil {
		return err
	}
	if err := p.applyPlacement(&req.Execution); err != nil {
		return err
	}
	if err := p.checkMetadata(req.Execution); err != nil {
		return err
	}
	if err := p.applyResources(&req.Resources); err != nil {
		return err
	}
	if p.RuntimeClassName != "" {
		rc := p.RuntimeClassName
		req.Execution.RuntimeClassName = &rc
	}
	return nil
}

// refuse builds a refusal naming the field and value.
func refuse(field, value, why string) error {
	return fmt.Errorf("%w: %s %q %s", ErrPolicyRefused, field, value, why)
}

// refuseField builds a refusal naming only the field, for structured values
// too long to be useful in a task note.
func refuseField(field, why string) error {
	return fmt.Errorf("%w: %s %s", ErrPolicyRefused, field, why)
}

// checkImage enforces images.allowed.
func (p *Policy) checkImage(image string) error {
	if p.Images.Allowed == nil {
		return nil
	}
	repo := imageRepository(image)
	for _, entry := range p.Images.Allowed {
		if strings.HasSuffix(entry, "/") {
			if strings.HasPrefix(image, entry) {
				return nil
			}
		} else if repo == entry {
			return nil
		}
	}
	return refuse("image", image, "is not in images.allowed")
}

// imageRepository strips the digest and the tag from an image reference. The
// tag is the part after the last ":" that follows the last "/", so a registry
// port ("host:5000/repo") is kept.
func imageRepository(image string) string {
	if i := strings.IndexByte(image, '@'); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		image = image[:i]
	}
	return image
}

// applyServiceAccount enforces service_account.force and .allowed.
func (p *Policy) applyServiceAccount(ex *domain.Execution) error {
	if p.ServiceAccount.Force != "" {
		ex.ServiceAccount = p.ServiceAccount.Force
	}
	if p.ServiceAccount.Allowed != nil && !slices.Contains(p.ServiceAccount.Allowed, ex.ServiceAccount) {
		return refuse("execution.service_account", ex.ServiceAccount, "is not in service_account.allowed")
	}
	return nil
}

// applyPlacement enforces the placement rule. The DAG's own fields are checked
// before the policy's forced values are merged in, so forcing never counts as
// DAG placement.
func (p *Policy) applyPlacement(ex *domain.Execution) error {
	rule := p.Placement
	if rule.AllowDAGPlacement != nil && !*rule.AllowDAGPlacement {
		switch {
		case len(ex.NodeSelector) > 0:
			return refuseField("execution.node_selector", "is set but placement.allow_dag_placement is false")
		case len(ex.Tolerations) > 0:
			return refuseField("execution.tolerations", "is set but placement.allow_dag_placement is false")
		case len(ex.Affinity) > 0:
			return refuseField("execution.affinity", "is set but placement.allow_dag_placement is false")
		case len(ex.TopologySpreadConstraints) > 0:
			return refuseField("execution.topology_spread_constraints", "is set but placement.allow_dag_placement is false")
		}
	}
	if rule.AllowedPriorityClasses != nil && ex.PriorityClassName != "" &&
		!slices.Contains(rule.AllowedPriorityClasses, ex.PriorityClassName) {
		return refuse("execution.priority_class_name", ex.PriorityClassName, "is not in placement.allowed_priority_classes")
	}
	if len(rule.NodeSelector) > 0 {
		merged := make(map[string]string, len(ex.NodeSelector)+len(rule.NodeSelector))
		maps.Copy(merged, ex.NodeSelector)
		maps.Copy(merged, rule.NodeSelector)
		ex.NodeSelector = merged
	}
	if len(rule.Tolerations) > 0 {
		tols := make([]map[string]any, 0, len(ex.Tolerations)+len(rule.Tolerations))
		tols = append(tols, ex.Tolerations...)
		for _, t := range rule.Tolerations {
			tols = append(tols, maps.Clone(t))
		}
		ex.Tolerations = tols
	}
	return nil
}

// checkMetadata enforces the label and annotation prefix rules.
func (p *Policy) checkMetadata(ex domain.Execution) error {
	if err := checkPrefixes("execution.labels", ex.Labels, p.Metadata.AllowedLabelPrefixes); err != nil {
		return err
	}
	return checkPrefixes("execution.annotations", ex.Annotations, p.Metadata.AllowedAnnotationPrefixes)
}

// checkPrefixes refuses the first key (in sorted order, so the message is
// stable) that starts with none of the allowed prefixes. A nil list is no rule.
func checkPrefixes(field string, kv map[string]string, allowed *[]string) error {
	if allowed == nil || len(kv) == 0 {
		return nil
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.ContainsFunc(*allowed, func(prefix string) bool { return strings.HasPrefix(k, prefix) }) {
			return refuse(field, k, "does not start with an allowed prefix")
		}
	}
	return nil
}

// applyResources enforces resources.max: any request or limit above the
// ceiling, or one that does not parse, is refused; a capped resource with no
// limit gets the ceiling, and an explicit zero request if it had none. Limits
// and requests are copied before being filled, so the DAG spec's (possibly
// shared) value is never modified.
func (p *Policy) applyResources(r *domain.Resources) error {
	if len(p.maxQuantities) == 0 {
		return nil
	}
	for _, side := range []struct {
		name string
		q    *domain.ResourceQuantity
	}{{"resources.requests", r.Requests}, {"resources.limits", r.Limits}} {
		if side.q == nil {
			continue
		}
		if err := p.checkCeiling(side.name, *side.q); err != nil {
			return err
		}
	}
	limits := domain.ResourceQuantity{}
	if r.Limits != nil {
		limits = *r.Limits
	}
	requests := domain.ResourceQuantity{}
	if r.Requests != nil {
		requests = *r.Requests
	}
	reqFields := quantityFields(&requests)
	filledRequest := false
	for i, qf := range quantityFields(&limits) {
		maxQ, capped := p.maxQuantities[qf.name]
		if *qf.value != "" || !capped {
			continue
		}
		*qf.value = maxQ.String()
		// Kubernetes defaults a missing request to the limit, which would make
		// the pod reserve the whole ceiling. A filled limit gets an explicit
		// zero request instead, so the pod reserves only what it asked for.
		if *reqFields[i].value == "" {
			*reqFields[i].value = "0"
			filledRequest = true
		}
	}
	r.Limits = &limits
	if filledRequest {
		r.Requests = &requests
	}
	return nil
}

// checkCeiling compares one side (requests or limits) to the ceiling.
func (p *Policy) checkCeiling(side string, q domain.ResourceQuantity) error {
	for _, qf := range quantityFields(&q) {
		if *qf.value == "" {
			continue
		}
		v, err := resource.ParseQuantity(*qf.value)
		if err != nil {
			return refuse(side+"."+string(qf.name), *qf.value, "is not a valid quantity")
		}
		if maxQ, ok := p.maxQuantities[qf.name]; ok && v.Cmp(maxQ) > 0 {
			return refuse(side+"."+string(qf.name), *qf.value, "exceeds resources.max "+maxQ.String())
		}
	}
	return nil
}

// quantityField pairs a resource name with the field holding it.
type quantityField struct {
	name  corev1.ResourceName
	value *string
}

// quantityFields lists each resource's field in a fixed order, so the first
// refusal reported is always the same one.
func quantityFields(q *domain.ResourceQuantity) [3]quantityField {
	return [3]quantityField{
		{corev1.ResourceCPU, &q.CPU},
		{corev1.ResourceMemory, &q.Memory},
		{corev1.ResourceEphemeralStorage, &q.EphemeralStorage},
	}
}

// parseQuantities parses a ceiling; an unparseable value is an error.
func parseQuantities(q domain.ResourceQuantity) (map[corev1.ResourceName]resource.Quantity, error) {
	out := map[corev1.ResourceName]resource.Quantity{}
	for _, qf := range quantityFields(&q) {
		if *qf.value == "" {
			continue
		}
		v, err := resource.ParseQuantity(*qf.value)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", qf.name, *qf.value, err)
		}
		out[qf.name] = v
	}
	return out, nil
}
