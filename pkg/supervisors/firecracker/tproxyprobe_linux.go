package firecracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/nftables"
)

// ErrTProxyProbeUnavailable marks a TPROXY probe that could not run at all
// (missing unshare or supervisor binary) as opposed to one that ran and was
// refused by the kernel. Callers fall back to the module-presence heuristic
// on the former and report the latter as a real not-ready.
var ErrTProxyProbeUnavailable = errors.New("tproxy probe unavailable")

// RunEgressTProxyProbe is the --tproxy-selfcheck handler: it installs the
// same table, chain, and TPROXY steering rule a mediated boot installs, and
// exits by whether the kernel accepted them. It must run inside a fresh
// user+net namespace (ProbeEgressTProxySupport arranges that), where the
// scratch netns both grants CAP_NET_ADMIN and discards everything on exit —
// there is nothing to tear down.
//
// This is the probe-the-artifact answer to "will UDP mediation work here":
// module-presence checks lie in both directions (the kernel autoloads
// nft_tproxy on rule install, and a built-in without parameters never appears
// under /sys/module), but the rule install is the exact operation a boot
// performs, so its verdict cannot drift from the enforced path.
// tproxySelfCheckMarker prefixes every outcome the self-check itself
// produces. A supervisor too old to know --tproxy-selfcheck fails without the
// marker (it falls through to its serve path and dies on an empty request),
// and the parent classifies that unmarked failure as probe-unavailable
// instead of a kernel refusal — version skew must not read as a broken host.
const tproxySelfCheckMarker = "tproxy-selfcheck:"

func RunEgressTProxyProbe() error {
	conn := &nftables.Conn{}
	if err := ensureEgressMangleChain(conn); err != nil {
		return fmt.Errorf("%s prepare mangle chain: %w", tproxySelfCheckMarker, err)
	}
	// The rule mirrors a real boot's steering rule; the interface, subnet, and
	// mediator address are placeholders that never see traffic — the scratch
	// netns has no guest, only the kernel's answer to the tproxy expression.
	rule, err := buildEgressTProxyRule("lo", "169.254.128.0/24", egressTProxyMark, netip.MustParseAddrPort("127.0.0.1:1"))
	if err != nil {
		return fmt.Errorf("%s build tproxy probe rule: %w", tproxySelfCheckMarker, err)
	}
	table := nftRuleTable(rule.transientFirewallRule)
	chain := &nftables.Chain{Name: rule.Chain, Table: table}
	conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: rule.Exprs, UserData: nftRuleUserData(rule.Comment)})
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("%s install tproxy probe rule: %w", tproxySelfCheckMarker, err)
	}
	return nil
}

// ProbeEgressTProxySupport verifies, by re-executing the supervisor's
// --tproxy-selfcheck under `unshare --map-root-user --net`, that this host
// can install the TPROXY steering rule the way a mediated boot does: a fresh
// user namespace owning a fresh network namespace, the same environment the
// pasta-mode supervisor installs rules in. nil means the kernel accepted the
// rule (loading modules on demand if it needed to). An error wrapping
// ErrTProxyProbeUnavailable means the probe could not run; any other error is
// the kernel's refusal, which a mediated boot would hit the same way.
func ProbeEgressTProxySupport(supervisorPath string) error {
	unsharePath, err := exec.LookPath("unshare")
	if err != nil {
		return fmt.Errorf("%w: unshare binary not found (util-linux)", ErrTProxyProbeUnavailable)
	}
	if _, err := os.Stat(supervisorPath); err != nil {
		return fmt.Errorf("%w: supervisor not found at %s", ErrTProxyProbeUnavailable, supervisorPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, unsharePath, "--map-root-user", "--net", "--", supervisorPath, "--tproxy-selfcheck")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		if !strings.Contains(message, tproxySelfCheckMarker) {
			// The self-check never spoke: an older supervisor without the
			// handler, or unshare itself failing before the handler ran.
			return fmt.Errorf("%w: %s", ErrTProxyProbeUnavailable, message)
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}
