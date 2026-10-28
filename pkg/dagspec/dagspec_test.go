package dagspec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// dag builds a dag.json with two tasks, b after a, and the given schedule and
// pool_slots on b. An empty schedule leaves the field out. edit, when given,
// changes the document before it is encoded.
func dag(schedule string, slots int, edit ...func(spec map[string]any)) []byte {
	a := map[string]any{"task_id": "a", "type": "python", "entrypoint": "orders:extract"}
	b := map[string]any{"task_id": "b", "type": "python", "entrypoint": "orders:load", "depends_on": []string{"a"}}
	if slots > 0 {
		b["pool_slots"] = slots
	}
	spec := map[string]any{
		"schema_version": "1.0",
		"dag_id":         "orders",
		"dag_version":    "v1",
		"image":          "registry.example.com/orders:v1",
		"tasks":          []any{a, b},
	}
	if schedule != "" {
		spec["schedule"] = schedule
	}
	for _, e := range edit {
		e(spec)
	}
	out, err := json.Marshal(spec)
	if err != nil {
		panic(err)
	}
	return out
}

func TestValidateAcceptsAValidDAG(t *testing.T) {
	if err := Validate(dag("0 * * * *", 0)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRefusesWhatRegistrationRefuses(t *testing.T) {
	cases := map[string]struct {
		in   []byte
		want string
	}{
		"empty":            {nil, "empty"},
		"not json":         {[]byte("{"), "does not decode"},
		"schema violation": {dag("", 0, func(s map[string]any) { s["dag_id"] = "has space" }), "/dag_id"},
		"cycle": {dag("", 0, func(s map[string]any) {
			s["tasks"].([]any)[0].(map[string]any)["depends_on"] = []string{"b"}
		}), "cyclic"},
		"bad cron": {dag("* * * *", 0), "invalid schedule"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := Validate(c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want an error naming %q", err, c.want)
			}
		})
	}
}

// The server decodes the body into its spec type before it validates, so a
// field the format does not know is dropped, not refused. Validate must agree
// with it, or a caller would refuse a DAG the server registers.
func TestValidateDropsAnUnknownFieldAsTheServerDoes(t *testing.T) {
	if err := Validate(dag("", 0, func(s map[string]any) { s["bogus"] = 1 })); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestCheckLimits(t *testing.T) {
	l := Limits{MinScheduleIntervalSeconds: 300, MaxTaskPoolSlots: 2}
	if err := CheckLimits(dag("*/5 * * * *", 2), l); err != nil {
		t.Fatalf("within limits: %v", err)
	}
	if err := CheckLimits(dag("* * * * *", 0), l); !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "min_schedule_interval_seconds") {
		t.Fatalf("schedule too often: %v", err)
	}
	if err := CheckLimits(dag("", 3), l); !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "max_task_pool_slots") {
		t.Fatalf("task too large: %v", err)
	}
	if err := CheckLimits(dag("* * * * *", 9), Limits{}); err != nil {
		t.Fatalf("zero limits are unlimited: %v", err)
	}
	if err := CheckLimits(dag("* * * *", 0), Limits{}); err == nil {
		t.Fatal("CheckLimits skipped Validate")
	}
}

func TestMinScheduleInterval(t *testing.T) {
	if gap, ok := MinScheduleInterval("*/15 * * * *"); !ok || gap != 15*time.Minute {
		t.Fatalf("*/15: %v %v", gap, ok)
	}
	if _, ok := MinScheduleInterval("@once"); ok {
		t.Fatal("@once has no interval")
	}
}

func TestSchemaIsTheOneValidateApplies(t *testing.T) {
	var s map[string]any
	if err := json.Unmarshal(Schema(), &s); err != nil {
		t.Fatalf("Schema is not JSON: %v", err)
	}
	if s["title"] != "Dexaflow DAG Specification" {
		t.Fatalf("title = %v", s["title"])
	}
	Schema()[0] = 'x'
	if Schema()[0] != '{' {
		t.Fatal("Schema returned the embedded bytes, not a copy")
	}
}
