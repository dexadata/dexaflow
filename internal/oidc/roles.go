package oidc

// MapRoles translates the user's IdP group values into Dexaflow role names using
// the configured mapping. It is DEFAULT-DENY: a group with no entry in the map
// contributes no role. The result is de-duplicated and preserves first-seen
// order, so a token's role list is stable regardless of group ordering.
func MapRoles(groups []string, mappings map[string]string) []string {
	if len(mappings) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var roles []string
	for _, g := range groups {
		role, ok := mappings[g]
		if !ok || seen[role] {
			continue
		}
		seen[role] = true
		roles = append(roles, role)
	}
	return roles
}

// ApplyDefaultRole softens the default-deny without weakening it: when the
// mapped set is empty and a default role is configured, the user is granted that
// single fallback role. An empty defaultRole leaves the deny in place (the
// secure default) and a non-empty mapped set is never overridden.
func ApplyDefaultRole(roles []string, defaultRole string) []string {
	if len(roles) == 0 && defaultRole != "" {
		return []string{defaultRole}
	}
	return roles
}
