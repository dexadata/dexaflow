package cli

import (
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
)

// collectConnIDs extracts the connection ids a compiled DAG references, scanning
// each task and its operator_args for any key whose name contains "conn_id". The
// result is sorted and de-duplicated. This is how deploy surfaces the runtime
// dependency the artifact does NOT carry: connections live in the control-plane
// DB, not in the image+dag.json, so a deploy can succeed yet the DAG fail at
// runtime if they are not created on Pro first (ADR 0041).
func collectConnIDs(dagJSON []byte) []string {
	var spec struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(dagJSON, &spec); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	add := func(v any) {
		if s, ok := v.(string); ok && s != "" && !seen[s] {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	scan := func(m map[string]any) {
		for k, v := range m {
			if strings.Contains(strings.ToLower(k), "conn_id") {
				add(v)
			}
		}
	}
	for _, t := range spec.Tasks {
		scan(t)
		if args, ok := t["operator_args"].(map[string]any); ok {
			scan(args)
		}
	}
	sort.Strings(ids)
	return ids
}

// surfaceConnections reads the compiled dag.json and, when the DAG references any
// connection, prints a reminder to create them on the control plane — never a
// silent gap between "deploy succeeded" and "the DAG actually runs".
func surfaceConnections(out io.Writer, dagJSONPath string) {
	data, err := os.ReadFile(dagJSONPath) // path is the operator-controlled DAG dir.
	if err != nil {
		return
	}
	ids := collectConnIDs(data)
	if len(ids) == 0 {
		return
	}
	devPrintf(out, "  note: this DAG expects connection(s): %s\n", strings.Join(ids, ", "))
	devPrintf(out, "        create them on the control plane (UI or API) before the run; "+
		"they are not part of the image (ADR 0041).\n")
}
