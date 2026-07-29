package vmkit

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// These tests are plain source scans, so they run (and fail) on every
// platform's CI — the point is that a contract change on one side cannot
// merge without the registry, and therefore the other side, being updated.

const (
	appleVFMainSwiftPath    = "../../supervisors/applevf/Sources/microagent-applevf-supervisor/main.swift"
	firecrackerBootArgsPath = "../supervisors/firecracker/config_linux.go"
	guestInitMainPath       = "../../cmd/microagent-guestinit/main.go"
)

var bootParamPattern = regexp.MustCompile(`microagent_[a-z_]+`)

func scanBootParams(t *testing.T, path string, filter func(line string) bool) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	keys := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if filter != nil && !filter(line) {
			continue
		}
		for _, key := range bootParamPattern.FindAllString(line, -1) {
			keys[key] = true
		}
	}
	return keys
}

// emissionLine keeps only lines that emit a key (`microagent_x=`), so keys in
// comments or log strings do not count as emitted.
func emissionLine(line string) bool {
	return strings.Contains(line, "microagent_") && strings.Contains(line, "=")
}

// parseLine keeps only guest-init lines that read a key from the parsed
// cmdline values map (both the pre-parsed `values` map and direct
// microagentCmdlineValues(...)["key"] lookups).
func parseLine(line string) bool {
	return strings.Contains(line, `values["microagent_`) ||
		strings.Contains(line, `)["microagent_`)
}

func registryKeys(pred func(GuestBootParam) bool) map[string]bool {
	keys := map[string]bool{}
	for _, param := range GuestBootParams() {
		if pred(param) {
			keys[param.Key] = true
		}
	}
	return keys
}

func diffKeySets(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	for key := range got {
		if !want[key] {
			t.Errorf("%s: %s present in source but not in the registry decision — register it in GuestBootParams()", label, key)
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("%s: registry says %s but the source does not have it — fix the source or the registry", label, key)
		}
	}
}

func TestGuestBootParamRegistryMatchesGuestInit(t *testing.T) {
	parsed := scanBootParams(t, guestInitMainPath, parseLine)
	diffKeySets(t, "guest init", parsed, registryKeys(func(GuestBootParam) bool { return true }))
}

func TestGuestBootParamRegistryMatchesFirecrackerBuilder(t *testing.T) {
	emitted := scanBootParams(t, firecrackerBootArgsPath, emissionLine)
	diffKeySets(t, "firecracker boot args", emitted, registryKeys(func(p GuestBootParam) bool { return p.Firecracker }))
}

func TestGuestBootParamRegistryMatchesAppleVFBuilder(t *testing.T) {
	emitted := scanBootParams(t, appleVFMainSwiftPath, emissionLine)
	diffKeySets(t, "apple-vf kernel cmdline", emitted, registryKeys(func(p GuestBootParam) bool { return p.AppleVF }))
}

func TestGuestBootParamAsymmetriesCarryReasons(t *testing.T) {
	for _, param := range GuestBootParams() {
		if param.Firecracker != param.AppleVF && strings.TrimSpace(param.Reason) == "" {
			t.Errorf("%s: asymmetric emission requires a documented reason", param.Key)
		}
	}
}

// appleVFSwiftConfigFields extracts the property names of the supervisor's
// `struct Config: Codable` — with Codable's default keys, property name ==
// JSON key, matching vmkit.Config's tags.
func appleVFSwiftConfigFields(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(appleVFMainSwiftPath)
	if err != nil {
		t.Fatalf("read %s: %v", appleVFMainSwiftPath, err)
	}
	src := string(data)
	start := strings.Index(src, "struct Config: Codable {")
	if start < 0 {
		t.Fatal("struct Config: Codable not found in main.swift")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("struct Config closing brace not found")
	}
	fields := map[string]bool{}
	varPattern := regexp.MustCompile(`^\s+var ([A-Za-z0-9_]+)\s*:`)
	for _, line := range strings.Split(src[start:start+end], "\n") {
		if m := varPattern.FindStringSubmatch(line); m != nil {
			fields[m[1]] = true
		}
	}
	if len(fields) == 0 {
		t.Fatal("no fields extracted from struct Config")
	}
	return fields
}

func vmkitConfigJSONFields() map[string]bool {
	fields := map[string]bool{}
	configType := reflect.TypeOf(Config{})
	for i := 0; i < configType.NumField(); i++ {
		tag := configType.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		fields[name] = true
	}
	return fields
}

// TestAppleVFSupervisorDecodesConfig is the guard for the TimeoutSeconds
// class: every vmkit.Config field must be decoded by the Swift supervisor or
// explicitly registered as undecoded with a reason. A field that is neither
// is silently dropped at boot AND erased from persisted state on macOS (the
// supervisor re-encodes its own Config into runtime.json).
func TestAppleVFSupervisorDecodesConfig(t *testing.T) {
	swiftFields := appleVFSwiftConfigFields(t)
	undecoded := AppleVFUndecodedConfigFields()
	for field := range vmkitConfigJSONFields() {
		_, excused := undecoded[field]
		if swiftFields[field] && excused {
			t.Errorf("%s: listed in AppleVFUndecodedConfigFields but the Swift Config decodes it — remove the stale entry", field)
		}
		if !swiftFields[field] && !excused {
			t.Errorf("%s: not decoded by the apple-vf supervisor and not registered in AppleVFUndecodedConfigFields — decode it or register the decision", field)
		}
	}
	goFields := vmkitConfigJSONFields()
	for field := range undecoded {
		if !goFields[field] {
			t.Errorf("AppleVFUndecodedConfigFields lists %q, which is not a vmkit.Config JSON field", field)
		}
	}
}
