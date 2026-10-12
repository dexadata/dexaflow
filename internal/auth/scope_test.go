package auth

import "testing"

func TestHasScope(t *testing.T) {
	u := &User{Scoped: true, Scopes: []string{ScopeRead, ScopeRun}}
	if !u.HasScope(ScopeRun) || !u.HasScope(ScopeRead) {
		t.Error("HasScope misses a granted scope")
	}
	if u.HasScope(ScopeDeploy) {
		t.Error("HasScope grants a scope the token does not carry")
	}
	if (&User{Scoped: true}).HasScope(ScopeRead) {
		t.Error("a scoped user with no scopes has none")
	}
}

func TestScopeValues(t *testing.T) {
	for got, want := range map[string]string{ScopeRead: "dexaflow:read", ScopeRun: "dexaflow:run", ScopeDeploy: "dexaflow:deploy"} {
		if got != want {
			t.Errorf("scope = %q, want %q", got, want)
		}
	}
}
