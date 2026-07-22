package main

import "testing"

func TestRegistryLookup(t *testing.T) {
	for name, want := range map[string]string{
		"run": "run", "ls": "list", "log": "logs", "rm": "delete", "inspect": "status",
	} {
		spec, ok := lookupCommand(name)
		if !ok || spec.Name != want {
			t.Errorf("lookupCommand(%q) = %v, %v; want spec %q", name, spec, ok, want)
		}
	}
	if _, ok := lookupCommand("frobnicate"); ok {
		t.Error("lookupCommand should miss unknown names")
	}
}

func TestRegistryWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, spec := range commandRegistry {
		if spec.Summary == "" || spec.Group == "" {
			t.Errorf("%s: missing Summary or Group", spec.Name)
		}
		if spec.Hidden && spec.HiddenReason == "" {
			t.Errorf("%s: Hidden requires HiddenReason", spec.Name)
		}
		if spec.Run == nil {
			t.Errorf("%s: nil Run", spec.Name)
		}
		for _, n := range append([]string{spec.Name}, spec.Aliases...) {
			if prev, dup := seen[n]; dup {
				t.Errorf("name/alias %q claimed by both %s and %s", n, prev, spec.Name)
			}
			seen[n] = spec.Name
		}
	}
}
