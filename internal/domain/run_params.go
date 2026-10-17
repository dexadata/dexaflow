package domain

import (
	"encoding/json"
	"fmt"
)

// RunParams returns the params a task of this DAG runs with, as the JSON object
// the agent delivers in LEOFLOW_PARAMS. It layers them the way Airflow does with
// dag_run_conf_overrides_params on: the DAG's declared param defaults, then the
// task's own params (operator_args.params for an operator, the task's params
// field for a bash or python task), then the run conf, each layer winning per key
// over the one before. A run created without conf, such as a scheduled
// run, still gets the DAG defaults and task params (#1841, #1844).
//
// When neither the DAG nor the task declares params, conf is returned unchanged.
// A conf that is not a JSON object (or null) is an error.
func (d DAGSpec) RunParams(task TaskSpec, conf json.RawMessage) (json.RawMessage, error) {
	taskParams := task.Params
	// An operator's params value that is not an object is not a layer.
	if tp, ok := task.OperatorArgs["params"].(map[string]any); ok && len(taskParams) == 0 {
		taskParams = tp
	}
	if len(d.Params) == 0 && len(taskParams) == 0 {
		return conf, nil
	}
	merged := make(map[string]json.RawMessage, len(d.Params)+len(taskParams))
	for name, p := range d.Params {
		if len(p.Default) > 0 {
			merged[name] = p.Default
		}
	}
	for name, v := range taskParams {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encoding task param %q: %w", name, err)
		}
		merged[name] = b
	}
	if len(conf) > 0 {
		var overrides map[string]json.RawMessage
		if err := json.Unmarshal(conf, &overrides); err != nil {
			return nil, fmt.Errorf("decoding run conf: %w", err)
		}
		for name, v := range overrides {
			merged[name] = v
		}
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("encoding run params: %w", err)
	}
	return out, nil
}
