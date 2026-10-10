package agent

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	"github.com/dexadata/dexaflow/internal/domain"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	yaml "go.yaml.in/yaml/v3"
)

// capturingBatchResolver records exactly which refs the chain asks it to resolve,
// so a test can assert that the dexaflow.yaml declaration — not the vault contents,
// not the whole backend — is the scope authority for the external request.
type capturingBatchResolver struct {
	hits map[secretsource.Ref]string
	got  []secretsource.Ref
}

func (c *capturingBatchResolver) Resolve(context.Context, string, secretsource.Kind) (value string, found bool, err error) {
	return "", false, nil // never used when ResolveBatch is available
}

func (c *capturingBatchResolver) ResolveBatch(_ context.Context, refs []secretsource.Ref) (map[secretsource.Ref]string, error) {
	c.got = append(c.got, refs...)
	out := map[secretsource.Ref]string{}
	for _, ref := range refs {
		if v, ok := c.hits[ref]; ok {
			out[ref] = v
		}
	}
	return out, nil
}

func sortedRefs(refs []secretsource.Ref) []secretsource.Ref {
	cp := slices.Clone(refs)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].Kind != cp[j].Kind {
			return cp[i].Kind < cp[j].Kind
		}
		return cp[i].Name < cp[j].Name
	})
	return cp
}

// effectiveDeclared mirrors the compiler's verbatim yaml->dag.json carry of the
// declared secret sets and storage.declaredVariables/declaredConnections: a
// task's own list narrows the DAG-level declaration; an empty task list inherits
// it. It is the rule that decides the DeclaredVariables/DeclaredConnections a
// TaskSpec carries to the agent.
func effectiveDeclared(taskLevel, dagLevel []string) []string {
	if len(taskLevel) > 0 {
		return taskLevel
	}
	return dagLevel
}

// TestSecretsFromRealLeoflowYAML is the integration seam ADR 0060 was otherwise
// missing: an author's real dexaflow.yaml, parsed by the domain loader, driving the
// pod-side external resolver. The per-layer unit tests each cover one side (yaml
// parsing; the resolver chain given a hand-built TaskSpec). This joins them: the
// connections/variables an author writes — narrowed per task — must be exactly
// what the external backend is asked for, and the resolved values must override
// the vault in the task env. If the yaml->declaration->resolver-request wiring
// ever drifts (a renamed field, a dropped narrowing), this fails.
func TestSecretsFromRealLeoflowYAML(t *testing.T) {
	const y = `
dag_id: sales_etl
schedule: "@daily"
connections:
  - warehouse
  - crm
variables:
  - region
  - feature_flag
tasks:
  load:
    # narrows the DAG-level declaration to a subset
    connections: [warehouse]
    variables: [region]
  report:
    # no narrowing -> inherits the full DAG-level declaration
    retries: 2
`
	var cfg domain.LeoflowConfig
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("parse dexaflow.yaml: %v", err)
	}

	// The author's declaration parsed as written.
	if cfg.DagID != "sales_etl" {
		t.Fatalf("dag_id = %q, want sales_etl", cfg.DagID)
	}
	if !slices.Equal(cfg.Connections, []string{"warehouse", "crm"}) {
		t.Fatalf("DAG-level connections = %v", cfg.Connections)
	}
	if !slices.Equal(cfg.Variables, []string{"region", "feature_flag"}) {
		t.Fatalf("DAG-level variables = %v", cfg.Variables)
	}

	// A vault that holds a value for every declared name, so the assertion that
	// external hits OVERRIDE the vault is meaningful (both sides present).
	newRunner := func(res *capturingBatchResolver) *Runner {
		return &Runner{
			Client: &fakeClient{
				vars:  map[string]string{"region": "us-vault", "feature_flag": "on-vault"},
				conns: map[string]string{"warehouse": "postgres://vault-warehouse", "crm": "https://vault-crm"},
			},
			Resolver:      res,
			SecretBackend: secretsource.Backend{Connections: true, Variables: true},
		}
	}

	t.Run("task narrowing scopes the external request", func(t *testing.T) {
		load := cfg.Tasks["load"]
		spec := &agentv1.TaskSpec{
			DeclaredConnections: effectiveDeclared(load.Connections, cfg.Connections), // [warehouse]
			DeclaredVariables:   effectiveDeclared(load.Variables, cfg.Variables),     // [region]
		}
		res := &capturingBatchResolver{hits: map[secretsource.Ref]string{
			{Name: "warehouse", Kind: secretsource.KindConnection}: "postgres://ext-warehouse",
			{Name: "region", Kind: secretsource.KindVariable}:      "eu-ext",
		}}
		out, err := newRunner(res).secretsEnv(context.Background(), spec)
		if err != nil {
			t.Fatalf("secretsEnv: %v", err)
		}

		// The resolver was asked for EXACTLY the narrowed declaration — not crm or
		// feature_flag (declared at the DAG level but narrowed out of this task),
		// and not driven by what the vault or backend happen to hold.
		want := sortedRefs([]secretsource.Ref{
			{Name: "region", Kind: secretsource.KindVariable},
			{Name: "warehouse", Kind: secretsource.KindConnection},
		})
		if got := sortedRefs(res.got); !slices.Equal(got, want) {
			t.Errorf("resolver asked for %v, want %v (task narrowing must scope the request)", got, want)
		}

		// The external hits reached the task env and overrode the vault values.
		if !slices.Contains(out, "AIRFLOW_CONN_WAREHOUSE=postgres://ext-warehouse") {
			t.Errorf("external connection missing from env: %v", out)
		}
		if !slices.Contains(out, "AIRFLOW_VAR_REGION=eu-ext") {
			t.Errorf("external variable missing from env: %v", out)
		}
		if slices.Contains(out, "AIRFLOW_CONN_WAREHOUSE=postgres://vault-warehouse") ||
			slices.Contains(out, "AIRFLOW_VAR_REGION=us-vault") {
			t.Errorf("vault value must be overridden by the external hit: %v", out)
		}
	})

	t.Run("no narrowing inherits the full DAG-level declaration", func(t *testing.T) {
		report := cfg.Tasks["report"]
		spec := &agentv1.TaskSpec{
			DeclaredConnections: effectiveDeclared(report.Connections, cfg.Connections), // warehouse, crm
			DeclaredVariables:   effectiveDeclared(report.Variables, cfg.Variables),     // region, feature_flag
		}
		res := &capturingBatchResolver{hits: map[secretsource.Ref]string{}}
		if _, err := newRunner(res).secretsEnv(context.Background(), spec); err != nil {
			t.Fatalf("secretsEnv: %v", err)
		}
		want := sortedRefs([]secretsource.Ref{
			{Name: "warehouse", Kind: secretsource.KindConnection},
			{Name: "crm", Kind: secretsource.KindConnection},
			{Name: "region", Kind: secretsource.KindVariable},
			{Name: "feature_flag", Kind: secretsource.KindVariable},
		})
		if got := sortedRefs(res.got); !slices.Equal(got, want) {
			t.Errorf("resolver asked for %v, want %v (a task with no narrowing inherits the DAG-level set)", got, want)
		}
	})
}
