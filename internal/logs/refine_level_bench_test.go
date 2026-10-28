package logs

import "testing"

// BenchmarkRefineLevel times the per-line level refinement that log ingestion
// runs on every line an agent streams. A plain line is the hot case: it must
// not pay for the dbt summary match (#870), which only a line containing
// "PASS=" can satisfy.
func BenchmarkRefineLevel(b *testing.B) {
	cases := []struct{ name, line string }{
		{"plain", "2026-10-26 12:00:00 running model orders_daily, 1532 rows written to the warehouse"},
		{"logger_info", "2026-10-26 12:00:00,123 INFO airflow.task: Task started"},
		{"dbt_summary", "Done. PASS=24 WARN=0 ERROR=0 SKIP=0 TOTAL=24"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = RefineLevel(c.line, "info")
			}
		})
	}
}
