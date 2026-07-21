package vmkit

import (
	"reflect"
	"strings"
	"testing"
)

// TestEgressConfigFieldsAreRegisteredForDatapathParity fails when an Egress*
// field is added to Config without a corresponding EgressDatapathFields entry.
// That entry is what the mediator/datapath parity tests key off, so an
// unregistered field is one that can silently reach only one backend — the B1/
// B22/B23 fail-open class. Registering it forces both datapaths to forward it.
func TestEgressConfigFieldsAreRegisteredForDatapathParity(t *testing.T) {
	registered := map[string]bool{}
	for _, f := range EgressDatapathFields() {
		if f.ConfigField != "" {
			registered[f.ConfigField] = true
		}
	}
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		name := ct.Field(i).Name
		if !strings.HasPrefix(name, "Egress") {
			continue
		}
		if !registered[name] {
			t.Errorf("vmkit.Config.%s is an egress field but is not in EgressDatapathFields(); "+
				"register it so the mediator and Apple VF datapaths both forward it (an unforwarded "+
				"egress control silently fails open on one backend)", name)
		}
	}
}

func TestEgressDatapathFieldsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range EgressDatapathFields() {
		if f.MediatorFlag == "" || f.DatapathFlag == "" {
			t.Errorf("egress field %+v has an empty flag name", f)
		}
		key := f.MediatorFlag + "|" + f.DatapathFlag
		if seen[key] {
			t.Errorf("duplicate egress field entry %+v", f)
		}
		seen[key] = true
	}
}
