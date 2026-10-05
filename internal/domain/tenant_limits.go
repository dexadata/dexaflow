package domain

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// TenantLimits are optional caps on what one tenant may do, set by the operator
// through the service API. A zero field means unlimited, which is also what a
// tenant created without limits has.
type TenantLimits struct {
	// MaxDags caps how many DAGs the tenant may register. New versions of a DAG
	// it already has are always accepted.
	MaxDags int
	// MaxRunsPerDay caps how many DAG runs, manual and scheduled alike, the
	// tenant may create in one UTC calendar day.
	MaxRunsPerDay int
	// MinScheduleIntervalSeconds is the shortest gap, in seconds, a DAG's
	// schedule may leave between two consecutive runs (MinScheduleInterval).
	MinScheduleIntervalSeconds int
}

// TenantLimitsUpdate changes some of a tenant's limits: a nil field leaves the
// stored value as it is, and a pointer to 0 sets that limit back to unlimited.
type TenantLimitsUpdate struct {
	MaxDags                    *int
	MaxRunsPerDay              *int
	MinScheduleIntervalSeconds *int
}

// IsZero reports whether the update changes nothing.
func (u TenantLimitsUpdate) IsZero() bool {
	return u.MaxDags == nil && u.MaxRunsPerDay == nil && u.MinScheduleIntervalSeconds == nil
}

const minutesPerDay = 24 * 60

// dayScanFrom and dayScanTo bound the search for the days a cron schedule
// fires on. Weekdays and dates line up the same way every 28 years between 1901
// and 2099, so this window holds every pairing of day-of-month, month and
// day-of-week a 5-field cron can express, leap days included.
var (
	dayScanFrom = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	dayScanTo   = dayScanFrom.AddDate(28, 0, 0)
)

// MinScheduleInterval returns the shortest gap between two consecutive fire
// times of a DAG schedule: a 5-field cron, a preset such as @hourly, or
// "@every <duration>". ok is false when the schedule never fires on a cron
// (empty, @once, @continuous) or does not parse.
//
// For a cron it is exact over the wall clock. The minutes and hours a cron
// fires at are the same on every day it fires, so the gaps inside a day are
// fixed; the only other gaps run from the last time of one firing day to the
// first time of the next, which is shortest across the closest two firing
// days. Those days come from a scan of one 28-year calendar cycle that walks
// firing days only, so even "* * * * *" costs a few thousand steps at most.
// The scan is in UTC; a schedule with CRON_TZ can be one daylight saving
// shift shorter or longer on the two days a year the clocks change.
func MinScheduleInterval(expr string) (time.Duration, bool) {
	expr = strings.TrimSpace(expr)
	if IsCronlessSchedule(expr) {
		return 0, false
	}
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return 0, false
	}
	switch s := schedule.(type) {
	case cron.ConstantDelaySchedule:
		return s.Delay, true
	case *cron.SpecSchedule:
		return specMinInterval(s)
	default:
		return 0, false
	}
}

// specMinInterval is MinScheduleInterval for a parsed 5-field cron.
func specMinInterval(s *cron.SpecSchedule) (time.Duration, bool) {
	times := dayMinutes(s.Hour, s.Minute)
	if len(times) == 0 {
		return 0, false
	}
	gap := -1
	for i := 1; i < len(times); i++ {
		if d := times[i] - times[i-1]; gap < 0 || d < gap {
			gap = d
		}
	}
	if days, ok := closestFiringDays(s); ok {
		if d := days*minutesPerDay - times[len(times)-1] + times[0]; gap < 0 || d < gap {
			gap = d
		}
	}
	if gap < 0 {
		return 0, false
	}
	return time.Duration(gap) * time.Minute, true
}

// dayMinutes lists, in ascending order, the minutes of the day (hour*60+minute)
// a cron fires at, from its hour and minute bit sets.
func dayMinutes(hours, minutes uint64) []int {
	var out []int
	for h := range 24 {
		if hours&(1<<uint(h)) == 0 {
			continue
		}
		for m := range 60 {
			if minutes&(1<<uint(m)) != 0 {
				out = append(out, h*60+m)
			}
		}
	}
	return out
}

// closestFiringDays returns the fewest calendar days between two consecutive
// days the cron fires on, over one 28-year cycle; ok is false when it fires on
// fewer than two days in it. A copy of the schedule that fires once a day, at
// midnight UTC, lets cron's own day matching (day-of-month OR day-of-week, as
// cron defines it) find each firing day in one Next call.
func closestFiringDays(s *cron.SpecSchedule) (int, bool) {
	daily := *s
	daily.Second, daily.Minute, daily.Hour = 1, 1, 1
	daily.Location = time.UTC
	best := -1
	prev := time.Time{}
	for t := daily.Next(dayScanFrom.Add(-time.Second)); !t.IsZero() && t.Before(dayScanTo); t = daily.Next(t) {
		if !prev.IsZero() {
			if d := int(t.Sub(prev).Hours()) / 24; best < 0 || d < best {
				best = d
			}
			if best == 1 {
				break
			}
		}
		prev = t
	}
	return best, best > 0
}
