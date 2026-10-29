package scheduler

import (
	"context"
	"reflect"
	"testing"
)

// reapOncer is the shape of the execution reaper's entrypoint. The scheduler
// used to expose a seam of this shape and drive it from every 1s leader tick.
type reapOncer interface {
	ReapOnce(context.Context) error
}

// TestSchedulerTickHasNoReaperHook pins that the scheduling tick cannot drive
// the execution reaper: the Scheduler carries no field that satisfies the
// reaper's ReapOnce shape and no method that accepts one. The reapers run from
// the leader's maintenance loop, ordered after the pod reconciler's sweep, at
// that loop's cadence — not at 1s from the tick. A func-typed field or
// parameter of the reaper's signature counts too: it is the seam shape this
// codebase prefers (#917). Re-adding a seam here would
// re-open the class of race this closes (a reaper judging state the reconciler
// has not yet recovered), so its absence is asserted rather than assumed.
func TestSchedulerTickHasNoReaperHook(t *testing.T) {
	reaperType := reflect.TypeOf((*reapOncer)(nil)).Elem()
	reapFunc := reflect.TypeOf((func(context.Context) error)(nil))
	// The pointer's element, not Scheduler{}: a value would copy a struct
	// holding atomics.
	st := reflect.TypeOf((*Scheduler)(nil)).Elem()
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if isReapSeam(f.Type, reaperType, reapFunc) {
			t.Errorf("Scheduler.%s holds a ReapOnce-shaped value; the tick must not be able to reap", f.Name)
		}
	}
	pt := reflect.TypeOf(&Scheduler{})
	for i := 0; i < pt.NumMethod(); i++ {
		m := pt.Method(i)
		for j := 1; j < m.Type.NumIn(); j++ { // 0 is the receiver
			in := m.Type.In(j)
			if isReapSeam(in, reaperType, reapFunc) {
				t.Errorf("Scheduler.%s accepts a ReapOnce-shaped value; the tick must not be able to reap", m.Name)
			}
		}
	}
}

// isReapSeam reports whether t can carry the reaper's entrypoint: an interface
// with its ReapOnce method, or a func of its signature.
func isReapSeam(t, reaperType, reapFunc reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface:
		return t.Implements(reaperType)
	case reflect.Func:
		return t == reapFunc
	}
	return false
}
