package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestTenantSuspensionValidate: hold, drain and stop are the modes, and the
// reason is bounded because it is shown back in refusals and logs.
func TestTenantSuspensionValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     TenantSuspension
		valid bool
		wants string
	}{
		{name: "hold without a reason", s: TenantSuspension{Mode: SuspensionHold}, valid: true},
		{name: "hold with a reason", s: TenantSuspension{Mode: SuspensionHold, Reason: "contract ended"}, valid: true},
		{name: "drain", s: TenantSuspension{Mode: SuspensionDrain}, valid: true},
		{name: "stop", s: TenantSuspension{Mode: SuspensionStop, Reason: "abuse"}, valid: true},
		{name: "unknown mode", s: TenantSuspension{Mode: "pause"}, wants: `use "hold", "drain" or "stop"`},
		{name: "empty mode", s: TenantSuspension{}, wants: `use "hold"`},
		{name: "reason too long", s: TenantSuspension{Mode: SuspensionHold, Reason: strings.Repeat("x", MaxSuspensionReasonLength+1)}, wants: "above the limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			err := tc.s.Validate()

			// Assert
			if tc.valid {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("Validate = %v, want ErrValidation naming %q", err, tc.wants)
			}
		})
	}
}

// TestTenantSuspensionRefusalError: the refusal is ErrTenantSuspended and
// carries the operator's reason when one was given.
func TestTenantSuspensionRefusalError(t *testing.T) {
	// Arrange
	withReason := TenantSuspension{Mode: SuspensionHold, Reason: "contract ended"}
	bare := TenantSuspension{Mode: SuspensionHold}

	// Act
	got, gotBare := withReason.RefusalError(), bare.RefusalError()

	// Assert
	if msg := safeMessage(got); !errors.Is(got, ErrTenantSuspended) || msg != "the tenant is suspended: contract ended" {
		t.Errorf("RefusalError = %q (message %q), want the reason after the prefix", got, msg)
	}
	if msg := safeMessage(gotBare); !errors.Is(gotBare, ErrTenantSuspended) || msg != "the tenant is suspended" {
		t.Errorf("RefusalError without a reason = %q (message %q)", gotBare, msg)
	}
	if (TenantSuspension{}).Active() || !bare.Active() {
		t.Error("Active must be false only for the zero value")
	}
}

func safeMessage(err error) string {
	var se *SafeError
	if errors.As(err, &se) {
		return se.Message
	}
	return ""
}

// TestSuspensionModeHoldsDispatch: drain and stop dispatch nothing of the
// tenant; hold lets its ready tasks go.
func TestSuspensionModeHoldsDispatch(t *testing.T) {
	for mode, want := range map[SuspensionMode]bool{"": false, SuspensionHold: false, SuspensionDrain: true, SuspensionStop: true} {
		if got := mode.HoldsDispatch(); got != want {
			t.Errorf("%q.HoldsDispatch() = %v, want %v", mode, got, want)
		}
	}
}
