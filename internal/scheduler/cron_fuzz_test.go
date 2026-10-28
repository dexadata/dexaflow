package scheduler

import (
	"testing"
	"time"
)

// FuzzScheduledSlots drives the scheduler's cron planning (nextScheduledRun,
// dueScheduledSlots) with arbitrary expressions and times. The expression is
// stored from an author's dag.json and re-read on every tick, so a panic here
// stops scheduling for every tenant. Whatever the input, no slot may lie in
// the future, the slots must be strictly increasing, and the cap must hold.
func FuzzScheduledSlots(f *testing.F) {
	for _, s := range []string{
		"*/5 * * * *", "@daily", "@hourly", "@every 90s",
		"CRON_TZ=America/Sao_Paulo 0 3 * * *", "0 0 29 2 *", "bogus",
	} {
		f.Add(s, int64(1760256000), int64(3600), int64(86400), true, 5)
		f.Add(s, int64(1760256000), int64(0), int64(-1), false, 1)
	}
	f.Fuzz(func(t *testing.T, expr string, nowUnix, sinceLast, sinceStart int64, catchup bool, maxSlots int) {
		// Keep times within the years a database can hold, so the test
		// exercises the planner rather than time.Time overflow.
		const span = int64(200 * 365 * 24 * 3600)
		if nowUnix < 0 || nowUnix > span || sinceLast < -span || sinceLast > span || sinceStart < -span || sinceStart > span {
			t.Skip("time out of range")
		}
		if maxSlots < 0 || maxSlots > 1000 {
			t.Skip("cap out of range")
		}
		now := time.Unix(nowUnix, 0).UTC()
		var last, start *time.Time
		if sinceLast > 0 {
			l := now.Add(-time.Duration(sinceLast) * time.Second)
			last = &l
		}
		if sinceStart != 0 {
			s := now.Add(-time.Duration(sinceStart) * time.Second)
			start = &s
		}

		if logical, due := nextScheduledRun(expr, last, now); due && logical.After(now) {
			t.Fatalf("nextScheduledRun(%q) = %v, after now %v", expr, logical, now)
		}
		slots := dueScheduledSlots(expr, last, start, now, catchup, maxSlots)
		if maxSlots > 0 && len(slots) > maxSlots {
			t.Fatalf("dueScheduledSlots(%q) returned %d slots, cap %d", expr, len(slots), maxSlots)
		}
		for i, s := range slots {
			if s.After(now) {
				t.Fatalf("dueScheduledSlots(%q) slot %v after now %v", expr, s, now)
			}
			if i > 0 && !s.After(slots[i-1]) {
				t.Fatalf("dueScheduledSlots(%q) slots not increasing: %v", expr, slots)
			}
		}
	})
}
