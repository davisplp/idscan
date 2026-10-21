package kcauth

import "testing"

func TestHMSGroup(t *testing.T) {
	tests := []struct {
		name   string
		groups []string
		roles  []string
		want   string
		wantOK bool
	}{
		{"single group with leading slash", []string{"/facilitator"}, nil, "facilitator", true},
		{"most privileged group wins", []string{"instructor", "manager"}, nil, "manager", true},
		{"groups take precedence over roles", []string{"facilitator"}, []string{"admin"}, "facilitator", true},
		{"role fallback with hms- prefix", nil, []string{"offline_access", "hms-coordinator"}, "coordinator", true},
		{"most privileged role wins", nil, []string{"instructor", "admin"}, "admin", true},
		{"unknown groups and roles", []string{"students"}, []string{"default-roles-hms"}, "", false},
		{"no claims", nil, nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := hmsGroup(tt.groups, tt.roles)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("hmsGroup(%v, %v) = %q, %v; want %q, %v", tt.groups, tt.roles, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestAllHubsGroups(t *testing.T) {
	for _, g := range []string{"super-admin", "admin", "super-instructor"} {
		if !allHubsGroups(g) {
			t.Errorf("allHubsGroups(%q) = false; want true", g)
		}
	}
	for _, g := range []string{"manager", "coordinator", "instructor", "facilitator", "tot-staff", "application-reviewer"} {
		if allHubsGroups(g) {
			t.Errorf("allHubsGroups(%q) = true; want false (needs a user_hubs assignment)", g)
		}
	}
}
