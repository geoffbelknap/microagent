//go:build linux

package firecracker

// Live, root-gated end-to-end test of egress mediation on a `named` network with
// two member VMs (Tier 2, Phase 4). It is the named-network analogue of the
// rootless pasta egress e2e shell scripts (scripts/dev/microagent-e2e-egress*.sh)
// but exercises the path those scripts cannot: a `named` network runs in the HOST
// netns over a shared host bridge (like `nat`, not pasta), so it needs root, a
// host bridge, host nftables, and the host-global TPROXY prerequisites that
// `microagent host setup-networking` provisions.
//
// The shared provisionEgressMediation helper (and its TCP REDIRECT + UDP TPROXY
// steering) is the same code the live-validated nat/user paths use; the unit and
// structural coverage in Phases 1-3 locks that down. This test locks in the
// named-specific behavior end-to-end:
//
//   - TwoMembersBoot         two named members boot and reach `running`.
//   - EastWestMediated       vmA -> vmB on an allowlisted port succeeds and is
//                            audited egress_allow peer:"vmB"; vmA -> a
//                            non-allowlisted in-subnet address is refused and
//                            audited egress_deny.
//   - ExternalMITMPeerSplice vmA -> an allowlisted external HTTPS host is MITM'd
//                            (guest sees OUR CA, egress_allow mitm:true); vmA ->
//                            a self-signed TLS service on vmB is L4-spliced (guest
//                            trusts vmB's own cert via --cacert, egress_allow
//                            mitm:false peer:"vmB").
//   - TeardownClean          both stop+delete cleanly: no recorded mediator PID is
//                            still alive, every tap's REDIRECT/NAT/TPROXY rules are
//                            gone, the taps are deleted, network.Leave removed both
//                            members, and reapNetworkBridgeIfEmpty deleted the
//                            shared bridge after the last member left.
//
// Gated by MICROAGENT_FIRECRACKER_E2E=1 AND root (named needs host bridge + host
// nftables). It skips cleanly — never fails — when the flag is unset, the process
// is not root, /dev/kvm or the firecracker binary is absent, or the TPROXY host
// prerequisites are missing and cannot be provisioned. Run it on a root host with:
//
//	MICROAGENT_FIRECRACKER_E2E=1 sudo -E go test ./pkg/supervisors/firecracker/ \
//	    -run TestNamedEgressE2E -v

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/network"
	"github.com/google/nftables"
)

const (
	namedE2ENetwork  = "neta"
	namedE2ESubnet   = "10.44.7.0/24"
	namedE2EVMA      = "vma"
	namedE2EVMB      = "vmb"
	namedE2EPeerPort = 8443 // allowlisted east-west port for the vmB TLS service
	namedE2EDenyPort = 9100 // not allowlisted; the refused east-west probe targets it
	// An external HTTPS host the mediator MITMs. example.com is the canonical
	// stable target the sibling egress e2e scripts use.
	namedE2EExternalHost = "example.com"
	// curlimages/curl is alpine-based with curl (TLS), /bin/sh, openssl, and a
	// public CA bundle — the same image the egress-mitm e2e uses.
	namedE2EImage = "docker.io/curlimages/curl:latest"
)

// namedE2EHarness holds the built binaries, the shared state dir, and the per-VM
// supervisor Options the in-package teardown assertions read back through.
type namedE2EHarness struct {
	t          *testing.T
	root       string // repo root
	stateDir   string
	cli        string
	supervisor string
	guestInit  string
	kernel     string
	rootfsSrc  string
	optsA      Options
	optsB      Options
	vmBIP      string // vmB's allocated address on netA
}

func TestNamedEgressE2E(t *testing.T) {
	h := newNamedE2EHarness(t) // skips cleanly when ungated / not root / prereqs absent
	h.buildBinaries()
	h.provisionTProxyPrereqs()
	h.installKernel()
	h.pullRootfs()
	h.createNetwork()
	h.prepareMembers()

	// One boot shared across the named subtests (booting two firecracker VMs is the
	// expensive step). Cleanup is deferred so an early failure still tears the VMs,
	// taps, bridge, and network records down — no host state is left behind.
	h.startMember(namedE2EVMA)
	h.startMember(namedE2EVMB)
	defer h.cleanup()

	h.waitRunning(namedE2EVMA)
	h.waitRunning(namedE2EVMB)

	// vmB serves a self-signed TLS endpoint (for the peer-splice case) and a plain
	// listener on the allowlisted port (for the east-west allow case). Brought up
	// before the probes from vmA run.
	h.startPeerServices()

	t.Run("TwoMembersBoot", h.subtestTwoMembersBoot)
	t.Run("EastWestMediated", h.subtestEastWestMediated)
	t.Run("ExternalMITMPeerSplice", h.subtestExternalMITMPeerSplice)
	t.Run("TeardownClean", h.subtestTeardownClean)
}

// newNamedE2EHarness performs all skip-gating up front. It returns a usable
// harness only when this is a live, root, TPROXY-capable host with the flag set;
// otherwise it t.Skip()s with a clear reason and never returns.
func newNamedE2EHarness(t *testing.T) *namedE2EHarness {
	t.Helper()
	if os.Getenv("MICROAGENT_FIRECRACKER_E2E") != "1" {
		t.Skip("named egress e2e: set MICROAGENT_FIRECRACKER_E2E=1 to run the live two-VM named-network test")
	}
	if os.Geteuid() != 0 {
		t.Skip("named egress e2e: a named network runs in the host netns (shared host bridge + host nftables) and requires root; re-run with sudo -E")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skipf("named egress e2e: /dev/kvm not available: %v", err)
	}
	firecracker := os.Getenv("MICROAGENT_FIRECRACKER")
	if firecracker == "" {
		if p, err := exec.LookPath("firecracker"); err == nil {
			firecracker = p
		}
	}
	if firecracker == "" {
		t.Skip("named egress e2e: the firecracker backend binary is required; install it or set MICROAGENT_FIRECRACKER")
	}
	if st, err := os.Stat(firecracker); err != nil || st.Mode()&0o111 == 0 {
		t.Skipf("named egress e2e: firecracker binary %q is not executable", firecracker)
	}

	root, err := repoRootForTest()
	if err != nil {
		t.Skipf("named egress e2e: could not locate repo root: %v", err)
	}
	stateDir := t.TempDir()
	h := &namedE2EHarness{
		t:          t,
		root:       root,
		stateDir:   stateDir,
		cli:        filepath.Join(stateDir, "microagent"),
		supervisor: filepath.Join(stateDir, "microagent-firecracker-supervisor"),
		guestInit:  filepath.Join(stateDir, "microagent-guestinit-amd64"),
	}
	t.Setenv("MICROAGENT_FIRECRACKER", firecracker)
	t.Setenv("MICROAGENT_FIRECRACKER_SUPERVISOR", h.supervisor)
	h.optsA = Options{Name: namedE2EVMA, StateDir: stateDir}
	h.optsB = Options{Name: namedE2EVMB, StateDir: stateDir}
	return h
}

// repoRootForTest walks up from the test's working directory to the module root
// (the directory holding go.mod) so `go build ./cmd/...` runs against this tree.
func repoRootForTest() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// buildBinaries compiles the CLI, the firecracker supervisor, and the amd64
// guest-init into the state dir — the same set the egress e2e shell scripts build.
func (h *namedE2EHarness) buildBinaries() {
	h.t.Helper()
	h.goBuild([]string{"-buildvcs=false", "-o", h.cli, "./cmd/microagent"}, nil)
	h.goBuild([]string{"-buildvcs=false", "-o", h.supervisor, "./cmd/microagent-firecracker-supervisor"}, nil)
	h.goBuild(
		[]string{"-buildvcs=false", "-o", h.guestInit, "./cmd/microagent-guestinit"},
		[]string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"},
	)
}

func (h *namedE2EHarness) goBuild(args, extraEnv []string) {
	h.t.Helper()
	cmd := exec.Command("go", append([]string{"build"}, args...)...)
	cmd.Dir = h.root
	cmd.Env = append(os.Environ(), extraEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("go build %v failed: %v\n%s", args, err, out)
	}
}

// provisionTProxyPrereqs ensures the host-global TPROXY prerequisites a mediated
// named workspace fail-closes without. It runs `host setup-networking` (we are
// root) and then verifies the kernel modules are present, skipping cleanly if they
// still cannot be provisioned (e.g. a kernel without the modules available).
func (h *namedE2EHarness) provisionTProxyPrereqs() {
	h.t.Helper()
	// Best-effort provisioning: setup-networking is idempotent and may legitimately
	// be a no-op when the host is already prepared, so its exit status is not fatal
	// here — the module readiness check below is the authoritative gate.
	out, err := h.runCLI(2*time.Minute, "host", "setup-networking", "--yes")
	if err != nil {
		h.t.Logf("named egress e2e: host setup-networking returned %v (continuing to module check):\n%s", err, out)
	}
	if missing := missingTProxyModules(); len(missing) != 0 {
		h.t.Skipf("named egress e2e: TPROXY host prerequisites unavailable after setup-networking (missing kernel modules: %s); a mediated/strict named workspace fails closed without them", strings.Join(missing, ", "))
	}
}

// missingTProxyModules reports which TPROXY kernel modules are neither loaded nor
// built-in, using the shared egressprereq source of truth. An empty result means
// the host can deliver TPROXY-steered datagrams to the mediator.
func missingTProxyModules() []string {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return egressprereq.TProxyModules
	}
	loaded := egressprereq.ParseLoadedModules(data)
	isBuiltin := func(name string) bool {
		// A module compiled into the kernel exposes /sys/module/<name> with no
		// "initstate" file (loadable modules have one). Best-effort.
		if _, statErr := os.Stat(filepath.Join("/sys/module", name)); statErr != nil {
			return false
		}
		_, initErr := os.Stat(filepath.Join("/sys/module", name, "initstate"))
		return initErr != nil
	}
	return egressprereq.MissingModules(loaded, isBuiltin)
}

func (h *namedE2EHarness) installKernel() {
	h.t.Helper()
	out, err := h.runCLI(3*time.Minute, "kernel", "install", "--backend", "firecracker", "--arch", "amd64")
	if err != nil {
		h.t.Fatalf("kernel install failed: %v\n%s", err, out)
	}
	var rec struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &rec); err != nil || rec.Path == "" {
		h.t.Fatalf("kernel install output not parseable (path missing): %v\n%s", err, out)
	}
	h.kernel = rec.Path
}

func (h *namedE2EHarness) pullRootfs() {
	h.t.Helper()
	out, err := h.runCLI(5*time.Minute,
		"image", "pull", namedE2EImage,
		"--state-dir", filepath.Join(h.stateDir, "cache"),
		"--arch", "amd64",
		"--guest-init", h.guestInit,
		"--size-mib", "192",
	)
	if err != nil {
		h.t.Fatalf("image pull failed: %v\n%s", err, out)
	}
	var rec struct {
		OutputPath string `json:"output_path"`
	}
	if err := json.Unmarshal([]byte(out), &rec); err != nil || rec.OutputPath == "" {
		h.t.Fatalf("image pull output not parseable (output_path missing): %v\n%s", err, out)
	}
	h.rootfsSrc = rec.OutputPath
}

// createNetwork registers the named network record via the same package the
// supervisor reads at start (network.Create), exactly as the task specifies.
func (h *namedE2EHarness) createNetwork() {
	h.t.Helper()
	if _, err := network.Create(h.stateDir, namedE2ENetwork, namedE2ESubnet); err != nil {
		h.t.Fatalf("network.Create(%q, %q): %v", namedE2ENetwork, namedE2ESubnet, err)
	}
}

// prepareMembers writes a prepared manifest + event for each VM directly (the
// CLI->manifest plumbing is unit-tested; the e2e scripts do the same), joining
// both to netA in named mode with strict egress. vmA's allowlist names the peer
// "vmB" and the external host; vmB needs no allowlist (it only serves).
func (h *namedE2EHarness) prepareMembers() {
	h.t.Helper()
	// Pre-join so vmB's stable address is known: vmA's roster (record.Members) and
	// peer probes need vmB's IP. Join is idempotent and the supervisor re-joins to
	// the same address at start.
	if _, err := network.Join(h.stateDir, namedE2ENetwork, namedE2EVMB); err != nil {
		h.t.Fatalf("network.Join(%s): %v", namedE2EVMB, err)
	}
	if _, err := network.Join(h.stateDir, namedE2ENetwork, namedE2EVMA); err != nil {
		h.t.Fatalf("network.Join(%s): %v", namedE2EVMA, err)
	}
	rec, err := network.Get(h.stateDir, namedE2ENetwork)
	if err != nil {
		h.t.Fatalf("network.Get: %v", err)
	}
	for _, m := range rec.Members {
		if m.Workspace == namedE2EVMB {
			h.vmBIP = m.IP
		}
	}
	if h.vmBIP == "" {
		h.t.Fatalf("vmB has no allocated address on %s: %+v", namedE2ENetwork, rec.Members)
	}

	// vmA: strict egress, allowlist the peer by name + the external host.
	h.writePreparedManifest(namedE2EVMA, "strict", []string{namedE2EVMB, namedE2EExternalHost})
	// vmB: a passive server. Strict with an empty allowlist is fine — it never
	// initiates egress in this test; it only accepts inbound east-west connections,
	// which the bridge forwards without traversing vmA's mediator.
	h.writePreparedManifest(namedE2EVMB, "strict", nil)
}

func (h *namedE2EHarness) writePreparedManifest(name, egressMode string, egressAllow []string) {
	h.t.Helper()
	wsDir := filepath.Join(h.stateDir, "workspaces", name)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		h.t.Fatalf("mkdir workspace dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(h.stateDir, name), 0o755); err != nil {
		h.t.Fatalf("mkdir state dir: %v", err)
	}
	// Each VM needs its own rootfs copy (the supervisor mutates it).
	if err := copyFileForTest(h.rootfsSrc, filepath.Join(wsDir, "rootfs.ext4")); err != nil {
		h.t.Fatalf("copy rootfs for %s: %v", name, err)
	}

	manifest := map[string]any{
		"name":        name,
		"profile":     "small",
		"restart":     "never",
		"resources":   map[string]any{"memory_mib": 512, "cpu_count": 2, "size_mib": 192},
		"network":     map[string]any{"mode": "named", "name": namedE2ENetwork},
		"egress_mode": egressMode,
	}
	if len(egressAllow) != 0 {
		manifest["egress_allow"] = egressAllow
	}
	writeJSON(h.t, filepath.Join(wsDir, "workspace.json"), manifest)

	event := map[string]any{
		"identity": map[string]any{
			"requestID": name + "-prepared",
			"runtimeID": name,
			"role":      "workload",
			"backend":   "firecracker",
		},
		"state":      "prepared",
		"detail":     "prepared for named egress e2e",
		"observedAt": time.Now().UTC().Format(time.RFC3339),
	}
	writeJSON(h.t, filepath.Join(h.stateDir, name, "event.json"), event)
}

func (h *namedE2EHarness) startMember(name string) {
	h.t.Helper()
	out, err := h.runCLI(90*time.Second, "start", name, "--state-dir", h.stateDir, "--kernel", h.kernel)
	if err != nil {
		h.t.Fatalf("start %s failed: %v\n%s", name, err, out)
	}
}

func (h *namedE2EHarness) waitRunning(name string) {
	h.t.Helper()
	deadline := time.Now().Add(75 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := h.runCLI(20*time.Second, "status", name, "--state-dir", h.stateDir)
		if err == nil {
			var rec struct {
				Event struct {
					State string `json:"state"`
				} `json:"event"`
			}
			if json.Unmarshal([]byte(out), &rec) == nil {
				last = rec.Event.State
				switch last {
				case "running":
					return
				case "failed":
					h.t.Fatalf("workspace %s reached failed:\n%s", name, out)
				}
			}
		}
		time.Sleep(time.Second)
	}
	h.t.Fatalf("workspace %s did not reach running (last state=%q)", name, last)
}

// startPeerServices brings up two listeners inside vmB: a self-signed TLS server
// on the allowlisted east-west port (for the peer-splice case) and exports its CA
// cert to a guest path vmA reads via shared expectations. Both run with busybox
// httpd-free tooling: openssl s_server for TLS. The plain east-west allow case
// reuses the same TLS port (a successful TLS connect proves L4 reachability).
func (h *namedE2EHarness) startPeerServices() {
	h.t.Helper()
	// Generate a self-signed cert+key for vmB's own IP, start an openssl TLS echo
	// server on the allowlisted port in the background, and print the PEM cert so
	// vmA can pin it. The server is detached (setsid + &) so the connect --send call
	// returns; the VM teardown reaps it.
	script := strings.Join([]string{
		"set -e",
		"cd /tmp",
		fmt.Sprintf("openssl req -x509 -newkey rsa:2048 -keyout vmb.key -out vmb.crt -days 1 -nodes -subj /CN=%s -addext subjectAltName=IP:%s >/dev/null 2>&1", namedE2EVMB, h.vmBIP),
		// Serve the cert content over the TLS port: s_server returns it to any client
		// after handshake. -www makes it answer HTTP-over-TLS with a small page.
		// Wrap the backgrounded server in a subshell so the "; " join below does not
		// produce "&; sleep 1" — a bare "&;" is a POSIX shell syntax error (busybox
		// /bin/sh rejects it with `syntax error: unexpected ";"`). "( ... & )" keeps
		// the detachment while letting the next "; " separator be valid.
		fmt.Sprintf("( setsid sh -c 'while true; do openssl s_server -quiet -accept %d -cert vmb.crt -key vmb.key -www >/dev/null 2>&1; done' >/dev/null 2>&1 & )", namedE2EPeerPort),
		"sleep 1",
		"echo PEER_TLS_UP=yes",
		"echo PEER_CERT_BEGIN",
		"cat vmb.crt",
		"echo PEER_CERT_END",
		"sync",
	}, "; ")
	out := h.connect(namedE2EVMB, 40*time.Second, script)
	if !strings.Contains(out, "PEER_TLS_UP=yes") {
		h.t.Fatalf("vmB peer TLS service did not start:\n%s", out)
	}
	cert := extractBetween(out, "PEER_CERT_BEGIN", "PEER_CERT_END")
	if !strings.Contains(cert, "BEGIN CERTIFICATE") {
		h.t.Fatalf("vmB did not emit a usable cert:\n%s", out)
	}
	// Stash vmB's cert PEM on the host so vmA can install it as --cacert.
	if err := os.WriteFile(filepath.Join(h.stateDir, "vmb-cert.pem"), []byte(cert), 0o600); err != nil {
		h.t.Fatalf("write vmB cert: %v", err)
	}
}

// ---- subtests ----

// subtestTwoMembersBoot asserts both members are members of netA with stable
// addresses and reached running (already waited above; this records the gate and
// confirms each VM's egress mediator PID was provisioned and is alive).
func (h *namedE2EHarness) subtestTwoMembersBoot(t *testing.T) {
	for _, opts := range []Options{h.optsA, h.optsB} {
		st, err := readRuntimeState(opts)
		if err != nil {
			t.Fatalf("readRuntimeState(%s): %v", opts.Name, err)
		}
		if st.Event.State != "running" {
			t.Fatalf("%s runtime state = %q, want running", opts.Name, st.Event.State)
		}
		if st.EgressMediatorPID <= 0 {
			t.Fatalf("%s has no recorded egress mediator PID (egress not provisioned for the named member)", opts.Name)
		}
		if !processAlive(st.EgressMediatorPID) {
			t.Fatalf("%s egress mediator PID %d is not alive while running", opts.Name, st.EgressMediatorPID)
		}
	}
	rec, err := network.Get(h.stateDir, namedE2ENetwork)
	if err != nil {
		t.Fatalf("network.Get: %v", err)
	}
	if !memberPresent(rec, namedE2EVMA) || !memberPresent(rec, namedE2EVMB) {
		t.Fatalf("both members must be on %s: %+v", namedE2ENetwork, rec.Members)
	}
}

// subtestEastWestMediated proves the mediator polices VM<->VM flows by peer name
// under default-deny: vmA reaching vmB on the allowlisted port succeeds and is
// audited egress_allow peer:"vmB"; vmA reaching a non-allowlisted in-subnet
// address is refused and audited egress_deny.
func (h *namedE2EHarness) subtestEastWestMediated(t *testing.T) {
	denyIP := siblingSubnetAddr(h.vmBIP) // an in-subnet address that is not an allowlisted peer

	script := strings.Join([]string{
		// Allowed peer flow: TLS connect to vmB on the allowlisted port. A clean TLS
		// handshake (exit 0) proves the mediator spliced the east-west flow.
		fmt.Sprintf("curl -sS -m 12 --cacert /dev/null -k https://%s:%d/ -o /dev/null -w 'PEER_CODE=%%{http_code}\\n' 2>/dev/null; echo PEER_EXIT=$?", namedE2EVMB, namedE2EPeerPort),
		// Denied east-west flow: a non-allowlisted in-subnet address. strict
		// default-deny must refuse it (non-zero curl exit, egress_deny in the audit).
		fmt.Sprintf("curl -sS -m 8 http://%s:%d/ -o /dev/null 2>/dev/null; echo DENY_EXIT=$?", denyIP, namedE2EDenyPort),
		"sync",
	}, "; ")
	out := h.connect(namedE2EVMA, 50*time.Second, script)

	if peerExit := field(out, "PEER_EXIT"); peerExit != "0" {
		t.Fatalf("east-west peer flow vmA->vmB:%d was not allowed (PEER_EXIT=%s):\n%s", namedE2EPeerPort, peerExit, out)
	}
	if denyExit := field(out, "DENY_EXIT"); denyExit == "0" {
		t.Fatalf("non-allowlisted east-west flow vmA->%s:%d was NOT refused (DENY_EXIT=%s):\n%s", denyIP, namedE2EDenyPort, denyExit, out)
	}

	events := h.auditEvents(namedE2EVMA)
	if !hasEvent(events, "egress_allow", map[string]any{"peer": namedE2EVMB, "mitm": false}) {
		t.Fatalf("missing egress_allow peer:%q mitm:false for the east-west peer flow:\n%s", namedE2EVMB, dumpEvents(events))
	}
	// The deny is recorded as egress_deny for the in-subnet address (a peer/IP-only
	// east-west destination), with peer_ip stamped so the flow stays legible.
	if !hasEvent(events, "egress_deny", map[string]any{"peer_ip": denyIP}) {
		t.Fatalf("missing egress_deny peer_ip:%q for the non-allowlisted east-west flow:\n%s", denyIP, dumpEvents(events))
	}
}

// subtestExternalMITMPeerSplice proves the external-vs-peer TLS split: an
// allowlisted external HTTPS host is MITM'd (the guest sees OUR egress CA), while a
// peer's self-signed TLS service is L4-spliced (the guest trusts vmB's own cert,
// not the egress CA) and audited mitm:false.
func (h *namedE2EHarness) subtestExternalMITMPeerSplice(t *testing.T) {
	certPEM, err := os.ReadFile(filepath.Join(h.stateDir, "vmb-cert.pem"))
	if err != nil {
		t.Fatalf("read vmB cert: %v", err)
	}
	// Deliver vmB's cert into vmA via a heredoc so curl can pin it with --cacert.
	script := strings.Join([]string{
		"set +e",
		// External MITM: allowlisted HTTPS host. The guest trusts our egress CA via
		// the vsock-delivered bundle, so the MITM'd leaf verifies and the issuer is
		// "microagent egress".
		"echo EXT_BUNDLE=$([ -f /etc/microagent/egress-ca-bundle.pem ] && echo yes || echo no)",
		fmt.Sprintf("curl -sS -m 15 https://%s -o /dev/null -w 'EXT_CODE=%%{http_code}\\n' 2>/dev/null; echo EXT_EXIT=$?", namedE2EExternalHost),
		fmt.Sprintf("echo EXT_ISSUER=$(curl -sS -m 15 -v https://%s 2>&1 | sed -n 's/^\\* *issuer: *//p' | head -1)", namedE2EExternalHost),
		// Peer splice: write vmB's cert and curl vmB over TLS with --cacert. Success
		// means the guest validated vmB's OWN self-signed cert end-to-end — proof the
		// mediator L4-spliced (no MITM: an interposed cert would fail this verify).
		"cat > /tmp/vmb-cert.pem <<'CERTEOF'",
		strings.TrimRight(string(certPEM), "\n"),
		"CERTEOF",
		fmt.Sprintf("curl -sS -m 12 --cacert /tmp/vmb-cert.pem https://%s:%d/ -o /dev/null -w 'PEER_TLS_CODE=%%{http_code}\\n' 2>/dev/null; echo PEER_TLS_EXIT=$?", h.vmBIP, namedE2EPeerPort),
		"sync",
	}, "\n")
	out := h.connect(namedE2EVMA, 60*time.Second, script)

	if field(out, "EXT_BUNDLE") != "yes" {
		t.Fatalf("egress CA bundle missing in vmA guest:\n%s", out)
	}
	if code := field(out, "EXT_CODE"); code != "200" {
		t.Fatalf("allowlisted external host %s not served (EXT_CODE=%s):\n%s", namedE2EExternalHost, code, out)
	}
	if issuer := fieldRest(out, "EXT_ISSUER"); !strings.Contains(strings.ToLower(issuer), "microagent egress") {
		t.Fatalf("external host %s was NOT MITM'd (issuer=%q must be our egress CA):\n%s", namedE2EExternalHost, issuer, out)
	}
	if exit := field(out, "PEER_TLS_EXIT"); exit != "0" {
		t.Fatalf("peer TLS splice vmA->vmB:%d failed verification with vmB's own cert (PEER_TLS_EXIT=%s) — east-west TLS must be L4-spliced, not MITM'd:\n%s", namedE2EPeerPort, exit, out)
	}

	events := h.auditEvents(namedE2EVMA)
	if !hasEvent(events, "egress_allow", map[string]any{"host": namedE2EExternalHost, "mitm": true}) {
		t.Fatalf("missing egress_allow host:%q mitm:true (external MITM) in vmA audit:\n%s", namedE2EExternalHost, dumpEvents(events))
	}
	if !hasEvent(events, "egress_allow", map[string]any{"peer": namedE2EVMB, "mitm": false}) {
		t.Fatalf("missing egress_allow peer:%q mitm:false (peer L4 splice, NO MITM) in vmA audit:\n%s", namedE2EVMB, dumpEvents(events))
	}
}

// subtestTeardownClean stops + deletes both members and asserts a clean teardown:
// no recorded mediator PID still alive, every tap's REDIRECT/NAT/TPROXY rules
// gone, taps deleted, network.Leave removed both members, the shared bridge reaped
// after the last member, and delete succeeds.
func (h *namedE2EHarness) subtestTeardownClean(t *testing.T) {
	// Capture each member's recorded mediator PID and tap BEFORE teardown.
	type member struct {
		opts Options
		pid  int
		tap  string
	}
	var members []member
	for _, opts := range []Options{h.optsA, h.optsB} {
		st, err := readRuntimeState(opts)
		if err != nil {
			t.Fatalf("readRuntimeState(%s): %v", opts.Name, err)
		}
		members = append(members, member{opts: opts, pid: st.EgressMediatorPID, tap: tapName(opts)})
	}
	bridge := bridgeName(namedE2ENetwork)

	for _, opts := range []Options{h.optsA, h.optsB} {
		if out, err := h.runCLI(60*time.Second, "stop", opts.Name, "--state-dir", h.stateDir); err != nil {
			t.Fatalf("stop %s: %v\n%s", opts.Name, err, out)
		}
	}
	for _, opts := range []Options{h.optsA, h.optsB} {
		if out, err := h.runCLI(60*time.Second, "delete", opts.Name, "--yes", "--state-dir", h.stateDir); err != nil {
			t.Fatalf("delete %s: %v\n%s", opts.Name, err, out)
		}
	}

	conn := &nftables.Conn{}
	table := microagentNFTTable()
	for _, m := range members {
		// No recorded mediator PID still alive.
		if m.pid > 0 && processAlive(m.pid) {
			t.Fatalf("%s egress mediator PID %d still alive after teardown (orphan)", m.opts.Name, m.pid)
		}
		// Every per-tap steering rule is gone.
		ruleChecks := []struct{ chain, kind string }{
			{nftNATPreroutingChain, "egress-redirect"},
			{nftManglePreroutingChain, "egress-tproxy"},
			{nftNATPostroutingChain, "masquerade"},
			{nftForwardChain, "forward-out"},
			{nftForwardChain, "forward-established"},
		}
		for _, rc := range ruleChecks {
			chain := &nftables.Chain{Name: rc.chain, Table: table}
			exists, err := nftRuleExists(conn, table, chain, nftRuleComment(m.tap, rc.kind))
			if err != nil {
				// A missing chain (whole table torn down) is a clean state, not a failure.
				continue
			}
			if exists {
				t.Fatalf("%s rule (%s/%s) for tap %s still present after teardown", m.opts.Name, rc.chain, rc.kind, m.tap)
			}
		}
		// Tap device deleted.
		if _, err := os.Stat(filepath.Join("/sys/class/net", m.tap)); err == nil {
			t.Fatalf("%s tap %s still present after teardown", m.opts.Name, m.tap)
		}
	}

	// network.Leave removed both members (delete drives LeaveAll).
	rec, err := network.Get(h.stateDir, namedE2ENetwork)
	if err != nil {
		t.Fatalf("network.Get after teardown: %v", err)
	}
	if memberPresent(rec, namedE2EVMA) || memberPresent(rec, namedE2EVMB) {
		t.Fatalf("members not removed from %s after delete: %+v", namedE2ENetwork, rec.Members)
	}

	// reapNetworkBridgeIfEmpty deleted the shared bridge once the last member left.
	if _, err := os.Stat(filepath.Join("/sys/class/net", bridge)); err == nil {
		t.Fatalf("shared bridge %s not reaped after the last member left", bridge)
	}
}

// ---- harness plumbing ----

func (h *namedE2EHarness) cleanup() {
	// Best-effort: stop+delete anything still around so no VM, tap, bridge, or
	// network record outlives the test. Errors are ignored — TeardownClean already
	// asserted the happy path; this is the safety net for an early failure.
	for _, name := range []string{namedE2EVMA, namedE2EVMB} {
		_, _ = h.runCLI(30*time.Second, "halt", name, "--state-dir", h.stateDir)
		_, _ = h.runCLI(30*time.Second, "delete", name, "--yes", "--state-dir", h.stateDir)
	}
	// Drop network membership + record so a re-run starts clean (t.TempDir is fresh
	// each run, but LeaveAll keeps the registry tidy if the dir were reused).
	_ = network.LeaveAll(h.stateDir, namedE2EVMA)
	_ = network.LeaveAll(h.stateDir, namedE2EVMB)
	_ = network.Remove(h.stateDir, namedE2ENetwork, true)
}

func (h *namedE2EHarness) runCLI(timeout time.Duration, args ...string) (string, error) {
	cmd := exec.Command(h.cli, args...)
	cmd.Env = os.Environ()
	out, err := runWithTimeout(cmd, timeout)
	return out, err
}

// connect drives `microagent connect --send` and returns the captured output. It
// fails the test on a transport error (a non-zero in-guest command is the caller's
// concern, surfaced through the parsed fields).
func (h *namedE2EHarness) connect(name string, timeout time.Duration, script string) string {
	h.t.Helper()
	out, err := h.runCLI(timeout+20*time.Second,
		"connect", name, "--state-dir", h.stateDir,
		"--ready-timeout", "30", "--timeout", fmt.Sprintf("%d", int(timeout.Seconds())),
		"--send", script,
	)
	if err != nil {
		h.t.Fatalf("connect %s failed: %v\n%s", name, err, out)
	}
	return out
}

// auditEvents parses a member's egress-access.jsonl audit log into a slice of
// decoded JSON objects.
func (h *namedE2EHarness) auditEvents(name string) []map[string]any {
	h.t.Helper()
	path := filepath.Join(h.stateDir, name, "egress-access.jsonl")
	f, err := os.Open(path)
	if err != nil {
		h.t.Fatalf("open audit log %s: %v", path, err)
	}
	defer f.Close()
	var events []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil {
			events = append(events, ev)
		}
	}
	return events
}

// ---- pure helpers ----

func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) (string, error) {
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return buf.String(), fmt.Errorf("command timed out after %s", timeout)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// signal 0 probes existence without delivering a signal.
	return syscall.Kill(pid, 0) == nil
}

func memberPresent(rec network.Record, workspace string) bool {
	for _, m := range rec.Members {
		if m.Workspace == workspace {
			return true
		}
	}
	return false
}

// siblingSubnetAddr returns an in-subnet address distinct from ref by flipping the
// last octet to a high value unlikely to be a member, so an east-west probe to it
// is an in-range but non-allowlisted destination.
func siblingSubnetAddr(ref string) string {
	i := strings.LastIndex(ref, ".")
	if i < 0 {
		return ref
	}
	return ref[:i+1] + "250"
}

func copyFileForTest(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// field returns the first NAME=VALUE token's single-word value from connect
// output (the guest shell prefixes lines, so we match anywhere).
func field(out, name string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `=(\S+)`)
	if m := re.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// fieldRest returns NAME=... through end-of-line (for values that contain spaces,
// e.g. a cert issuer DN).
func fieldRest(out, name string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `=(.*)`)
	if m := re.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func extractBetween(out, begin, end string) string {
	i := strings.Index(out, begin)
	if i < 0 {
		return ""
	}
	rest := out[i+len(begin):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// hasEvent reports whether any audit event matches the given event name and all
// of the supplied field equalities. JSON numbers decode to float64 and bools to
// bool, so equality is value-compared after a light normalization.
func hasEvent(events []map[string]any, name string, want map[string]any) bool {
	for _, ev := range events {
		if ev["event"] != name {
			continue
		}
		ok := true
		for k, v := range want {
			if !valuesEqual(ev[k], v) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func valuesEqual(got, want any) bool {
	switch w := want.(type) {
	case string:
		s, ok := got.(string)
		return ok && s == w
	case bool:
		b, ok := got.(bool)
		return ok && b == w
	default:
		return fmt.Sprint(got) == fmt.Sprint(want)
	}
}

func dumpEvents(events []map[string]any) string {
	data, _ := json.MarshalIndent(events, "", "  ")
	return string(data)
}
