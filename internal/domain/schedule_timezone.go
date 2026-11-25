package domain

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	// The scheduler reads cron fields in the DAG's zone (#2453). Embedding the
	// zone database keeps that working on a host or image without one.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// fixedOffset matches the zone the parser emits for a start_date whose tzinfo
// has no IANA name, only an offset: "+05:30", "-03:00".
var fixedOffset = regexp.MustCompile(`^([+-])(\d{2}):(\d{2})$`)

// locations caches ScheduleLocation: time.LoadLocation reads and parses the
// zone file on every call, and the scheduler asks for the zone of every due
// DAG on every tick.
var locations sync.Map // string -> *time.Location

// ScheduleLocation returns the location a DAG's cron schedule is evaluated in,
// from its schedule_timezone (#2453): empty or "UTC" is UTC, an IANA name such
// as "America/Sao_Paulo" is that zone, daylight saving time included, and
// "+HH:MM" or "-HH:MM" is a fixed offset. "Local" is refused: the server's own
// zone is not a property of the DAG.
func ScheduleLocation(tz string) (*time.Location, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" || tz == "UTC" {
		return time.UTC, nil
	}
	if cached, ok := locations.Load(tz); ok {
		if loc, ok := cached.(*time.Location); ok {
			return loc, nil
		}
	}
	loc, err := loadScheduleLocation(tz)
	if err != nil {
		return nil, err
	}
	locations.Store(tz, loc)
	return loc, nil
}

// twoDigits is the value of a two-digit decimal string the fixedOffset
// pattern already matched.
func twoDigits(s string) int {
	return int(s[0]-'0')*10 + int(s[1]-'0')
}

func loadScheduleLocation(tz string) (*time.Location, error) {
	if m := fixedOffset.FindStringSubmatch(tz); m != nil {
		hours, minutes := twoDigits(m[2]), twoDigits(m[3])
		if hours > 23 || minutes > 59 {
			return nil, fmt.Errorf("schedule time zone %q: offset out of range", tz)
		}
		offset := hours*3600 + minutes*60
		if m[1] == "-" {
			offset = -offset
		}
		if offset == 0 {
			return time.UTC, nil
		}
		return time.FixedZone(tz, offset), nil
	}
	if tz == "Local" {
		return nil, fmt.Errorf("schedule time zone %q: name a zone such as \"Europe/Lisbon\" or an offset such as \"-03:00\"", tz)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("schedule time zone %q: %w", tz, err)
	}
	return loc, nil
}

// ParseCronIn is ParseCron with the cron fields read in the zone tz, the DAG's
// schedule_timezone (#2453), so "0 6 * * *" with a start_date in
// America/Sao_Paulo fires at 06:00 there. Next then answers the same instant
// whatever the location of its argument, and returns it in that location. A
// CRON_TZ= or TZ= prefix in the expression wins over tz. An @every schedule has
// no wall clock and is returned as parsed.
func ParseCronIn(expr, tz string) (cron.Schedule, error) {
	loc, err := ScheduleLocation(tz)
	if err != nil {
		return nil, err
	}
	schedule, err := ParseCron(expr)
	if err != nil {
		return nil, err
	}
	// ParseStandard leaves Location at time.Local unless the expression names
	// a zone; Next then reads the fields in its argument's location.
	spec, ok := schedule.(*cron.SpecSchedule)
	if !ok || spec.Location != time.Local {
		return schedule, nil
	}
	spec.Location = time.UTC
	if loc == time.UTC || coversEveryHour(spec) {
		if loc != time.UTC {
			spec.Location = loc
		}
		return spec, nil
	}
	return wallClockSchedule{spec: spec, loc: loc}, nil
}

// allHours is the Hour field of a cron that fires in every hour of the day.
const allHours = 1<<24 - 1

// coversEveryHour reports whether spec fires in every hour, like "0 * * * *".
// Such a schedule keeps a steady cadence in absolute time across a daylight
// saving change, as Airflow does: it fires in both passes of a repeated hour
// and not in a skipped one.
func coversEveryHour(spec *cron.SpecSchedule) bool {
	return spec.Hour&allHours == allHours
}

// wallClockSchedule reads a cron's fields as wall-clock times in loc, the way
// Airflow does for a DAG with a time zone (#2453): each slot is a local date
// and time, so a daily "30 1 * * *" fires once on the day the clocks go back
// (at the first 01:30) and, on the day they go forward past 02:30, a
// "30 2 * * *" fires at 03:30, the instant the skipped time maps to. Read in
// absolute time instead, robfig/cron fires the first twice and skips the
// second.
type wallClockSchedule struct {
	spec *cron.SpecSchedule // Location is UTC: the fields are read on naive local times
	loc  *time.Location
}

// maxWallClockSteps bounds the slots Next skips because they map at or before
// its argument: only the slots of a repeated hour do, so two are enough.
const maxWallClockSteps = 4

// Next returns the first slot strictly after t, in t's location, or the zero
// time when the schedule never fires again.
func (w wallClockSchedule) Next(t time.Time) time.Time {
	local := t.In(w.loc)
	cursor := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(),
		local.Second(), local.Nanosecond(), time.UTC)
	for range maxWallClockSteps {
		cursor = w.spec.Next(cursor)
		if cursor.IsZero() {
			return time.Time{}
		}
		at := wallClockInstant(cursor, w.loc)
		if at.After(t) {
			return at.In(t.Location())
		}
	}
	return time.Time{}
}

// wallClockInstant returns the instant the naive wall-clock time naive (a
// time in UTC whose fields are read as local ones) has in loc. Of the two
// instants of a repeated time it is the first. A time the clocks skip maps
// with the offset in force before the change, one gap later on the clock:
// 02:30 on the day New York goes from 02:00 EST to 03:00 EDT is 03:30 EDT, as
// pendulum and so Airflow make it. time.Date leaves both cases unspecified.
func wallClockInstant(naive time.Time, loc *time.Location) time.Time {
	_, before := naive.Add(-12 * time.Hour).In(loc).Zone()
	_, after := naive.Add(12 * time.Hour).In(loc).Zone()
	first := naive.Add(-time.Duration(max(before, after)) * time.Second)
	second := naive.Add(-time.Duration(min(before, after)) * time.Second)
	for _, at := range []time.Time{first, second} {
		if sameWallClock(at.In(loc), naive) {
			return at
		}
	}
	// Skipped: the offset before the change.
	return naive.Add(-time.Duration(before) * time.Second)
}

func sameWallClock(local, naive time.Time) bool {
	y1, m1, d1 := local.Date()
	y2, m2, d2 := naive.Date()
	return y1 == y2 && m1 == m2 && d1 == d2 && local.Hour() == naive.Hour() &&
		local.Minute() == naive.Minute() && local.Second() == naive.Second()
}
