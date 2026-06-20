//go:build windows

package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/hcsshim/hcn"
	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/network"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	managedNATNetworkName = "microagent-nat"
	// managedNATSubnet is the preferred managed-NAT /24. The actual subnet is
	// chosen at create time from managedNATSubnetCandidates to avoid
	// overlapping an existing HNS network, so the gateway is derived from the
	// chosen subnet rather than fixed here.
	managedNATSubnet          = "192.168.127.0/24"
	managedNATRoute           = "0.0.0.0/0"
	managedNATStartMAC        = "00-15-5D-52-D0-00"
	managedNATEndMAC          = "00-15-5D-52-DF-FF"
	managedEndpointNamePrefix = "microagent-"
	// managedNamedNetworkPrefix marks the HNS network backing a user-defined
	// named network. It is distinct from the shared NAT network so cleanup may
	// reap an empty named network without touching the persistent NAT one.
	managedNamedNetworkPrefix = "microagent-net-"

	// managedMediatedNetworkName is the shared Private (non-NAT) HNS network that
	// backs egress-mediated user/nat workspaces. It hands out an IP, gateway and
	// default route (so the guest's route decision succeeds and the OUTPUT-hook nft
	// REDIRECT still fires) but, being Private, has NO WinNAT uplink to the physical
	// network — so a guest that flushes its own nft rules has no path off-box except
	// the host mediator over hvsock (AF_VSOCK, independent of the NIC). This is the
	// host-enforced no-uplink topology: enforcement is the network type, which the
	// guest cannot change. It is distinct from the NATting microagent-nat network so
	// unmediated workspaces keep their real uplink unchanged.
	managedMediatedNetworkName = "microagent-mediated-egress"
)

// managedNamedNetworkName derives the HNS network name backing a named-network
// registry record.
func managedNamedNetworkName(name string) string {
	return managedNamedNetworkPrefix + name
}

type defaultAdapter struct {
	client hcsClient
}

func (defaultAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return vmkit.HostSupport{
		Backend:                 vmkit.BackendWindowsHyperV,
		Architecture:            runtime.GOARCH,
		FrameworkAvailable:      true,
		VirtualizationSupported: true,
		ConsoleAvailable:        false,
		ConsoleMode:             "unsupported",
	}, nil
}

func (defaultAdapter) Check(ctx context.Context) error {
	return ProbeHCSAccess(ctx)
}

func (a defaultAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	document, err := buildComputeSystemDocument(spec)
	if err != nil {
		return computeSystemHandle{}, err
	}
	client := a.hcsClient()
	handle, err := client.CreateComputeSystem(ctx, spec.Name, document)
	if err != nil {
		return computeSystemHandle{}, err
	}
	if handle.RuntimeID != "" {
		for _, path := range attachedVHDPaths(spec.Config) {
			if err := client.GrantVMAccess(ctx, handle.RuntimeID, path); err != nil {
				return computeSystemHandle{}, err
			}
		}
	}
	return handle, nil
}

func attachedVHDPaths(config vmkit.Config) []string {
	paths := []string{config.RootfsPath}
	for _, disk := range config.Disks {
		paths = append(paths, disk.Path)
	}
	return paths
}

func (a defaultAdapter) PrepareNetwork(ctx context.Context, spec computeSystemSpec) (networkAttachment, error) {
	network := normalizedNetwork(spec.Config.Network)
	switch network.Mode {
	case "isolated":
		return networkAttachment{RuntimeNetwork: &network}, nil
	case "bridged":
		hcnNetwork, err := hcn.GetNetworkByName(network.Interface)
		if err != nil {
			return networkAttachment{}, fmt.Errorf("resolve bridged HNS network %q: %w", network.Interface, err)
		}
		return createNetworkEndpoint(hcnNetwork, spec.Name, network)
	case "user", "nat":
		// Egress-mediated user/nat workspaces attach to the Private (non-NAT)
		// mediated-egress network instead of the NATting one: it hands out the same
		// shape of IP/gateway/default-route (so the guest's route decision succeeds
		// and the OUTPUT-hook nft REDIRECT still fires) but has NO WinNAT uplink, so
		// a guest that flushes its own nft rules has no path off-box except the host
		// mediator over hvsock. Unmediated workspaces keep the real NAT uplink.
		// (Endpoint ACLs cannot do this on a NAT network — HNS routes NAT egress
		// through WinNAT, which the endpoint ACL/VFP layer does not gate; the
		// no-uplink guarantee has to be the network type, which the guest cannot
		// change.)
		ensureNetwork := ensureManagedNATNetwork
		if egressMediationActive(&spec.Config) {
			ensureNetwork = ensureManagedMediatedNetwork
		}
		hcnNetwork, err := ensureNetwork()
		if err != nil {
			return networkAttachment{}, err
		}
		network.Mode = "nat"
		network.Subnet = firstSubnet(hcnNetwork)
		network.Gateway = firstGateway(hcnNetwork)
		if len(network.DNS) == 0 && network.Gateway != "" {
			network.DNS = []string{network.Gateway}
		}
		if len(network.Routes) == 0 {
			network.Routes = []string{managedNATRoute}
		}
		return createNetworkEndpoint(hcnNetwork, spec.Name, network)
	case "named":
		return prepareNamedNetwork(spec, network)
	default:
		return networkAttachment{}, fmt.Errorf("unsupported windows-hyperv network mode %q", network.Mode)
	}
}

// prepareNamedNetwork joins the workspace to a user-defined named network and
// realizes it on the host. A named network is private (matching the firecracker
// private bridge): an isolated HNS network carrying the registry's subnet with
// static IPAM, so members reach each other and the host but have no NAT egress.
// The workspace's registry-allocated member IP is bound to a static endpoint so
// its address is stable across stop/start, and the runtime network config feeds
// the guest the same static config the firecracker named path emits.
func prepareNamedNetwork(spec computeSystemSpec, runtimeNetwork vmkit.NetworkConfig) (networkAttachment, error) {
	name := strings.TrimSpace(runtimeNetwork.Name)
	if name == "" {
		return networkAttachment{}, fmt.Errorf("named network requires network.name")
	}
	workspace := strings.TrimSpace(spec.Identity.RuntimeID)
	if workspace == "" {
		workspace = strings.TrimSpace(spec.Name)
	}
	record, err := network.Get(spec.StateDir, name)
	if err != nil {
		return networkAttachment{}, fmt.Errorf("join named network: %w", err)
	}
	ip, err := network.Join(spec.StateDir, name, workspace)
	if err != nil {
		return networkAttachment{}, fmt.Errorf("allocate address on network %q: %w", name, err)
	}
	// Refresh the record so the /etc/hosts entries include this member.
	record, err = network.Get(spec.StateDir, name)
	if err != nil {
		return networkAttachment{}, err
	}
	prefix, err := subnetPrefixLen(record.Subnet)
	if err != nil {
		return networkAttachment{}, err
	}
	hcnNetwork, err := ensureManagedNamedNetwork(record)
	if err != nil {
		return networkAttachment{}, err
	}
	runtimeNetwork.Mode = "named"
	runtimeNetwork.Name = name
	runtimeNetwork.Subnet = record.Subnet
	runtimeNetwork.Gateway = record.Gateway
	// A private named network has no default route or NAT egress; members talk
	// to each other and the host only.
	runtimeNetwork.Routes = nil
	// /etc/hosts entries let members resolve each other by workspace name, the
	// same way the firecracker named path seeds them.
	runtimeNetwork.Hosts = namedNetworkHosts(record)
	return createStaticNetworkEndpoint(hcnNetwork, spec.Name, ip, prefix, runtimeNetwork)
}

// namedNetworkHosts renders one "name:ip" entry per member for the guest
// /etc/hosts file, matching the firecracker named-network host seeding.
func namedNetworkHosts(record network.Record) []string {
	hosts := make([]string, 0, len(record.Members))
	for _, m := range record.Members {
		hosts = append(hosts, m.Workspace+":"+m.IP)
	}
	return hosts
}

// subnetPrefixLen returns the prefix length of a CIDR subnet.
func subnetPrefixLen(subnet string) (int, error) {
	_, parsed, err := net.ParseCIDR(strings.TrimSpace(subnet))
	if err != nil {
		return 0, fmt.Errorf("parse network subnet %q: %w", subnet, err)
	}
	ones, _ := parsed.Mask.Size()
	return ones, nil
}

func (a defaultAdapter) CleanupNetwork(ctx context.Context, state runtimeState) error {
	if strings.TrimSpace(state.NetworkEndpointID) == "" {
		return nil
	}
	endpoint, err := hcn.GetEndpointByID(state.NetworkEndpointID)
	if err != nil {
		if hcn.IsNotFoundError(err) {
			reapManagedNamedNetworkIfEmpty(state.NetworkID)
			return nil
		}
		return fmt.Errorf("find HNS endpoint %q: %w", state.NetworkEndpointID, err)
	}
	if err := endpoint.Delete(); err != nil && !hcn.IsNotFoundError(err) {
		return fmt.Errorf("delete HNS endpoint %q: %w", state.NetworkEndpointID, err)
	}
	// A named network's private HNS network persists across a single member's
	// stop/start (its registry membership keeps the address), but once the last
	// endpoint is gone the network is an orphan — reap it the way the firecracker
	// backend reaps an empty managed bridge. Recreated lazily on the next start.
	reapManagedNamedNetworkIfEmpty(state.NetworkID)
	return nil
}

// reapManagedNamedNetworkIfEmpty deletes a microagent-managed named HNS network
// once it has no remaining endpoints. Best-effort: any error is non-fatal to
// teardown, and the NAT and operator bridged networks are never touched.
func reapManagedNamedNetworkIfEmpty(networkID string) {
	if strings.TrimSpace(networkID) == "" {
		return
	}
	hcnNetwork, err := hcn.GetNetworkByID(networkID)
	if err != nil || hcnNetwork == nil {
		return
	}
	if !strings.HasPrefix(hcnNetwork.Name, managedNamedNetworkPrefix) {
		return
	}
	endpoints, err := hcn.ListEndpointsOfNetwork(networkID)
	if err != nil || len(endpoints) > 0 {
		return
	}
	_ = hcnNetwork.Delete()
}

func (a defaultAdapter) Start(ctx context.Context, id string) error {
	return a.hcsClient().StartComputeSystem(ctx, id)
}

func (a defaultAdapter) Shutdown(ctx context.Context, id string) error {
	return a.hcsClient().ShutdownComputeSystem(ctx, id)
}

func (a defaultAdapter) Pause(ctx context.Context, id string) error {
	return a.hcsClient().PauseComputeSystem(ctx, id)
}

func (a defaultAdapter) Resume(ctx context.Context, id string) error {
	return a.hcsClient().ResumeComputeSystem(ctx, id)
}

func (a defaultAdapter) Kill(ctx context.Context, id string) error {
	return a.hcsClient().KillComputeSystem(ctx, id)
}

func (a defaultAdapter) Delete(ctx context.Context, id string) error {
	return a.hcsClient().DeleteComputeSystem(ctx, id)
}

// DescribeComputeSystem reports the raw HCS properties of a compute system
// for teardown diagnostics.
func (a defaultAdapter) DescribeComputeSystem(ctx context.Context, id string) (string, error) {
	return a.hcsClient().DescribeComputeSystem(ctx, id)
}

func (a defaultAdapter) Exists(ctx context.Context, id string) (bool, error) {
	err := a.hcsClient().ProbeComputeSystem(ctx, id)
	if err == nil {
		return true, nil
	}
	if isMissingComputeSystem(err) {
		return false, nil
	}
	return false, err
}

func (a defaultAdapter) Wait(ctx context.Context, id string) error {
	return a.hcsClient().WaitComputeSystem(ctx, id)
}

func (a defaultAdapter) hcsClient() hcsClient {
	if a.client != nil {
		return a.client
	}
	return newVMComputeClient()
}

func errHCSNotImplemented(operation string) error {
	return fmt.Errorf("windows-hyperv HCS adapter %s is experimental and not implemented yet", operation)
}

type computeSystemDocument struct {
	Owner                             string          `json:"Owner,omitempty"`
	SchemaVersion                     versionDocument `json:"SchemaVersion"`
	ShouldTerminateOnLastHandleClosed bool            `json:"ShouldTerminateOnLastHandleClosed"`
	VirtualMachine                    virtualMachine  `json:"VirtualMachine"`
}

type versionDocument struct {
	Major int `json:"Major"`
	Minor int `json:"Minor"`
}

type virtualMachine struct {
	StopOnReset     bool            `json:"StopOnReset"`
	Chipset         chipset         `json:"Chipset"`
	ComputeTopology computeTopology `json:"ComputeTopology"`
	Devices         devices         `json:"Devices"`
}

type chipset struct {
	LinuxKernelDirect linuxKernelDirect `json:"LinuxKernelDirect"`
}

type linuxKernelDirect struct {
	KernelFilePath string `json:"KernelFilePath"`
	KernelCmdLine  string `json:"KernelCmdLine"`
}

type computeTopology struct {
	Memory    virtualMachineMemory    `json:"Memory"`
	Processor virtualMachineProcessor `json:"Processor"`
}

type virtualMachineMemory struct {
	SizeInMB        int  `json:"SizeInMB,omitempty"`
	AllowOvercommit bool `json:"AllowOvercommit"`
}

type virtualMachineProcessor struct {
	Count int `json:"Count,omitempty"`
}

type devices struct {
	Scsi            map[string]scsiController `json:"Scsi,omitempty"`
	HvSocket        hvSocket                  `json:"HvSocket"`
	Plan9           map[string]any            `json:"Plan9"`
	ComPorts        map[string]comPort        `json:"ComPorts,omitempty"`
	NetworkAdapters map[string]networkAdapter `json:"NetworkAdapters,omitempty"`
}

type scsiController struct {
	Attachments map[string]attachment `json:"Attachments,omitempty"`
}

type attachment struct {
	Type     string `json:"Type"`
	Path     string `json:"Path"`
	ReadOnly bool   `json:"ReadOnly"`
}

type hvSocket struct {
	HvSocketConfig hvSocketConfig `json:"HvSocketConfig"`
}

type hvSocketConfig struct {
	DefaultBindSecurityDescriptor    string                           `json:"DefaultBindSecurityDescriptor"`
	DefaultConnectSecurityDescriptor string                           `json:"DefaultConnectSecurityDescriptor,omitempty"`
	ServiceTable                     map[string]hvSocketServiceConfig `json:"ServiceTable,omitempty"`
}

type hvSocketServiceConfig struct {
	AllowWildcardBinds        bool   `json:"AllowWildcardBinds"`
	BindSecurityDescriptor    string `json:"BindSecurityDescriptor"`
	ConnectSecurityDescriptor string `json:"ConnectSecurityDescriptor"`
}

type comPort struct {
	NamedPipe string `json:"NamedPipe"`
}

type networkAdapter struct {
	EndpointID string `json:"EndpointId,omitempty"`
}

func buildComputeSystemDocument(spec computeSystemSpec) ([]byte, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, fmt.Errorf("compute system name is required")
	}
	if strings.TrimSpace(spec.Config.KernelPath) == "" {
		return nil, fmt.Errorf("kernel path is required")
	}
	if strings.TrimSpace(spec.Config.RootfsPath) == "" {
		return nil, fmt.Errorf("rootfs path is required")
	}
	memoryMiB := spec.Config.MemoryMiB
	if memoryMiB == 0 {
		memoryMiB = 512
	}
	cpuCount := spec.Config.CPUCount
	if cpuCount == 0 {
		cpuCount = 2
	}
	serviceTable := map[string]hvSocketServiceConfig{}
	for _, listener := range spec.Config.VsockListeners {
		serviceTable[winio.VsockServiceID(listener.Port).String()] = hvSocketServiceConfig{
			AllowWildcardBinds:        true,
			BindSecurityDescriptor:    "D:P(A;;FA;;;WD)",
			ConnectSecurityDescriptor: "D:P(A;;FA;;;SY)(A;;FA;;;BA)",
		}
	}
	if spec.Config.Network != nil {
		for _, forward := range spec.Config.Network.PortForwards {
			serviceTable[winio.VsockServiceID(uint32(forward.HostPort)).String()] = hvSocketServiceConfig{
				AllowWildcardBinds:        true,
				BindSecurityDescriptor:    "D:P(A;;FA;;;WD)",
				ConnectSecurityDescriptor: "D:P(A;;FA;;;WD)",
			}
		}
	}
	if spec.Config.ShellPort != 0 {
		serviceTable[winio.VsockServiceID(uint32(spec.Config.ShellPort)).String()] = hvSocketServiceConfig{
			AllowWildcardBinds:        true,
			BindSecurityDescriptor:    "D:P(A;;FA;;;WD)",
			ConnectSecurityDescriptor: "D:P(A;;FA;;;WD)",
		}
	}
	if spec.Config.ExecPort != 0 {
		serviceTable[winio.VsockServiceID(uint32(guestExecPort(spec.Config))).String()] = hvSocketServiceConfig{
			AllowWildcardBinds:        true,
			BindSecurityDescriptor:    "D:P(A;;FA;;;WD)",
			ConnectSecurityDescriptor: "D:P(A;;FA;;;WD)",
		}
	}
	kernelCmdLine := "root=/dev/sda rw rootwait init=/sbin/microagent-init initcall_blacklist=virtio_vsock_init pci=off"
	// The guest listens on its own vsock ports, which differ from the ports
	// baked into the rootfs run config for a cloned workspace (the copy keeps
	// the source's baked ports). Tell the guest its runtime ports the same way
	// the Firecracker boot args do; guestinit's kernel-config override wins.
	if port := guestShellPort(spec.Config); port != 0 {
		kernelCmdLine += fmt.Sprintf(" microagent_shell_port=%d", port)
	}
	if port := guestExecPort(spec.Config); port != 0 {
		kernelCmdLine += fmt.Sprintf(" microagent_exec_port=%d", port)
	}
	if spec.Config.SecretsPort != 0 {
		kernelCmdLine += fmt.Sprintf(" microagent_secrets_port=%d", spec.Config.SecretsPort)
	}
	if len(spec.Config.OnDemandSecrets) != 0 {
		kernelCmdLine += " microagent_secrets_api=1"
	}
	// microagent_secrets_ctl_port is intentionally not emitted: the control
	// listener exists for snapshot purge/rehydrate, and snapshots are
	// Firecracker-only. The host never dials the guest control port here.
	if spec.Config.ModelGuestPort != 0 && spec.Config.ModelVsockPort != 0 {
		kernelCmdLine += fmt.Sprintf(" microagent_model_fwd=%d:%d", spec.Config.ModelGuestPort, spec.Config.ModelVsockPort)
	}
	if spec.Config.MaintenanceBoot {
		kernelCmdLine += " microagent_maintenance=1"
	}
	// Egress mediation: when this workspace runs a mediator (egress mode on AND a
	// network mode that routes through it), tell the guest to start its transparent
	// forwarder and which per-VM hvsock service port to dial — the same way the
	// shell/exec/model ports are passed. The port is the shared
	// egress.DefaultMediatorVsockPort the host front-end binds (listeners_windows.go),
	// so the guest forwarder and the host service cannot drift. Absent when
	// mediation is not active, so an unmediated guest installs no capture/forwarder.
	if egressMediationActive(&spec.Config) {
		kernelCmdLine += fmt.Sprintf(" microagent_egress_mediator_port=%d", egress.DefaultMediatorVsockPort)
	}
	// The HNS endpoint assigned the guest its address, but the synthetic
	// NIC (hv_netvsc) comes up unconfigured: tell the guest its static
	// config the same way the Firecracker boot args do. Requires the
	// kernels-6.12.22-r2 artifact (CONFIG_HYPERV_NET).
	if network := spec.Config.Network; network != nil &&
		(network.Mode == "user" || network.Mode == "nat" || network.Mode == "bridged" || network.Mode == "named") {
		switch {
		case network.IP != "" && network.Gateway != "":
			kernelCmdLine += " microagent_net_if=eth0"
			kernelCmdLine += " microagent_net_ip=" + network.IP
			kernelCmdLine += " microagent_net_gw=" + network.Gateway
			if len(network.DNS) != 0 {
				kernelCmdLine += " microagent_net_dns=" + strings.Join(network.DNS, ",")
			}
			if len(network.Hosts) != 0 {
				kernelCmdLine += " microagent_net_hosts=" + strings.Join(network.Hosts, ",")
			}
		case network.Mode == "bridged" && strings.TrimSpace(spec.NetworkEndpointID) != "":
			// Bridged to a network that does not statically allocate an
			// endpoint IP at attach time (an external vSwitch, or an ICS
			// switch like the built-in Default Switch, which serves addresses
			// over DHCP at guest boot). The synthetic NIC is present but has
			// no address to apply, so ask the guest to DHCP on it — the same
			// in-guest udhcpc path the kernel-cmdline DHCP modes already use.
			kernelCmdLine += " ip=dhcp"
		}
	}
	comPorts := map[string]comPort(nil)
	if hasResultListener(spec) {
		kernelCmdLine += " 8250_core.nr_uarts=1 8250_core.skip_txen_test=1 console=ttyS0,115200"
		comPorts = map[string]comPort{"0": {NamedPipe: serialPipePath(spec.Identity.RuntimeID)}}
	} else {
		kernelCmdLine += " 8250_core.nr_uarts=0 panic=-1 quiet"
	}
	networkAdapters := map[string]networkAdapter(nil)
	if strings.TrimSpace(spec.NetworkEndpointID) != "" {
		networkAdapters = map[string]networkAdapter{"0": {EndpointID: spec.NetworkEndpointID}}
	}
	scsiAttachments := map[string]attachment{
		"0": {Type: "VirtualDisk", Path: spec.Config.RootfsPath, ReadOnly: false},
	}
	for i, disk := range spec.Config.Disks {
		scsiAttachments[strconv.Itoa(i+1)] = attachment{
			Type:     "VirtualDisk",
			Path:     disk.Path,
			ReadOnly: disk.Mode == "ro",
		}
	}
	doc := computeSystemDocument{
		Owner:                             "microagent",
		SchemaVersion:                     versionDocument{Major: 2, Minor: 1},
		ShouldTerminateOnLastHandleClosed: false,
		VirtualMachine: virtualMachine{
			StopOnReset: true,
			Chipset: chipset{LinuxKernelDirect: linuxKernelDirect{
				KernelFilePath: spec.Config.KernelPath,
				KernelCmdLine:  kernelCmdLine,
			}},
			ComputeTopology: computeTopology{
				Memory:    virtualMachineMemory{SizeInMB: memoryMiB, AllowOvercommit: true},
				Processor: virtualMachineProcessor{Count: cpuCount},
			},
			Devices: devices{
				Scsi: map[string]scsiController{
					"0": {Attachments: scsiAttachments},
				},
				HvSocket: hvSocket{HvSocketConfig: hvSocketConfig{
					DefaultBindSecurityDescriptor:    "D:P(A;;FA;;;SY)(A;;FA;;;BA)",
					DefaultConnectSecurityDescriptor: "D:P(A;;FA;;;SY)(A;;FA;;;BA)",
					ServiceTable:                     serviceTable,
				}},
				Plan9:           map[string]any{},
				ComPorts:        comPorts,
				NetworkAdapters: networkAdapters,
			},
		},
	}
	return json.Marshal(doc)
}

func hasResultListener(spec computeSystemSpec) bool {
	if strings.TrimSpace(spec.Config.StateDir) == "" || strings.TrimSpace(spec.Identity.RuntimeID) == "" {
		return false
	}
	target := filepath.Join(spec.Config.StateDir, spec.Identity.RuntimeID, "result.json")
	for _, listener := range spec.Config.VsockListeners {
		if listener.Target == target {
			return true
		}
	}
	return false
}

func serialPipePath(runtimeID string) string {
	return `\\.\pipe\microagent-` + runtimeID + `-serial`
}

func normalizedNetwork(network *vmkit.NetworkConfig) vmkit.NetworkConfig {
	if network == nil {
		return vmkit.NetworkConfig{Mode: "user"}
	}
	normalized := *network
	normalized.Mode = strings.TrimSpace(normalized.Mode)
	if normalized.Mode == "" {
		normalized.Mode = "user"
	}
	normalized.Interface = strings.TrimSpace(normalized.Interface)
	return normalized
}

// managedNATSubnetCandidates are the /24 subnets the managed NAT network may
// use, in preference order. The historical default is first; the rest are
// fallbacks for hosts where it overlaps an existing HNS network. They are
// spread across the private ranges so at least one is almost always clear of
// the Default Switch's dynamic subnet.
var managedNATSubnetCandidates = []string{
	managedNATSubnet, // 192.168.127.0/24 — historical default, preferred
	"192.168.214.0/24",
	"10.71.214.0/24",
	"10.99.127.0/24",
	"172.28.127.0/24",
	"172.31.214.0/24",
}

// pickManagedNATSubnet returns the first candidate /24 (and its first-host
// gateway) that does not overlap any existing HNS network. It fails closed
// when every candidate conflicts rather than letting HNS reject the create
// with a misleading error.
func pickManagedNATSubnet() (string, string, error) {
	return pickFreeSubnet(managedNATSubnetCandidates, existingHNSNetworkPrefixes())
}

// pickFreeSubnet returns the first candidate that does not overlap any of the
// existing prefixes, plus its first-host gateway. Pure, so the selection logic
// is unit-tested without HNS.
func pickFreeSubnet(candidates, existing []string) (string, string, error) {
	for _, candidate := range candidates {
		if cidrOverlapsAny(candidate, existing) {
			continue
		}
		gateway, err := firstHostAddress(candidate)
		if err != nil {
			continue
		}
		return candidate, gateway, nil
	}
	return "", "", fmt.Errorf("no free /24 for the managed NAT network; existing HNS subnets: %s", strings.Join(existing, ", "))
}

// existingHNSNetworkPrefixes collects the IPv4 subnet prefixes of every HNS
// network on the host, so a new managed network can avoid overlapping them.
func existingHNSNetworkPrefixes() []string {
	networks, err := hcn.ListNetworks()
	if err != nil {
		return nil
	}
	var prefixes []string
	for i := range networks {
		for _, ipam := range networks[i].Ipams {
			for _, subnet := range ipam.Subnets {
				if p := strings.TrimSpace(subnet.IpAddressPrefix); p != "" {
					prefixes = append(prefixes, p)
				}
			}
		}
	}
	return prefixes
}

// cidrOverlapsAny reports whether cidr overlaps any of the other CIDRs. An
// unparseable target is treated as conflicting so it is skipped.
func cidrOverlapsAny(cidr string, others []string) bool {
	_, target, err := net.ParseCIDR(cidr)
	if err != nil {
		return true
	}
	for _, other := range others {
		_, parsed, err := net.ParseCIDR(strings.TrimSpace(other))
		if err != nil {
			continue
		}
		if target.Contains(parsed.IP) || parsed.Contains(target.IP) {
			return true
		}
	}
	return false
}

// firstHostAddress returns the first usable host (the .1) of an IPv4 CIDR.
func firstHostAddress(cidr string) (string, error) {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", fmt.Errorf("subnet %q is not IPv4", cidr)
	}
	host := make(net.IP, len(ip4))
	copy(host, ip4)
	host[3] = 1
	return host.String(), nil
}

func ensureManagedNATNetwork() (*hcn.HostComputeNetwork, error) {
	network, err := hcn.GetNetworkByName(managedNATNetworkName)
	if err == nil {
		return network, nil
	}
	if !hcn.IsNotFoundError(err) {
		return nil, fmt.Errorf("find HNS NAT network %q: %w", managedNATNetworkName, err)
	}
	// The NAT subnet must not overlap an existing HNS network or HNS rejects
	// the create with a misleading "duplicate name exists" (0x34). The Windows
	// Default Switch (ICS) is the usual culprit: its dynamically assigned /20
	// can span the historical default 192.168.127.0/24. Pick the first
	// candidate /24 that is clear of every existing HNS network.
	subnet, gateway, err := pickManagedNATSubnet()
	if err != nil {
		return nil, err
	}
	network = &hcn.HostComputeNetwork{
		Type: hcn.NAT,
		Name: managedNATNetworkName,
		MacPool: hcn.MacPool{Ranges: []hcn.MacRange{{
			StartMacAddress: managedNATStartMAC,
			EndMacAddress:   managedNATEndMAC,
		}}},
		Ipams: []hcn.Ipam{{
			Type: "Static",
			Subnets: []hcn.Subnet{{
				IpAddressPrefix: subnet,
				Routes: []hcn.Route{{
					NextHop:           gateway,
					DestinationPrefix: managedNATRoute,
				}},
			}},
		}},
		SchemaVersion: hcn.SchemaVersion{Major: 2, Minor: 0},
	}
	created, err := network.Create()
	if err != nil {
		return nil, fmt.Errorf("create HNS NAT network %q (subnet %s): %w", managedNATNetworkName, subnet, err)
	}
	return created, nil
}

// ensureManagedNamedNetwork lazily creates the private HNS network backing a
// named-network record, carrying the registry's subnet and gateway with static
// IPAM. It is idempotent so concurrent member starts and restarts converge on
// the same network. Unlike the NAT network it installs no default route, so the
// guests have no egress — only intra-network and host reachability.
func ensureManagedNamedNetwork(record network.Record) (*hcn.HostComputeNetwork, error) {
	name := managedNamedNetworkName(record.Name)
	existing, err := hcn.GetNetworkByName(name)
	if err == nil {
		return existing, nil
	}
	if !hcn.IsNotFoundError(err) {
		return nil, fmt.Errorf("find HNS named network %q: %w", name, err)
	}
	// HNS requires a subnet route carrying the gateway even for a Private
	// network: it assigns the gateway to the host vNIC so members reach the host
	// and each other. The network type stays Private, so this is host-internal
	// reachability only — there is no NAT egress behind the gateway.
	hcnNetwork := &hcn.HostComputeNetwork{
		Type: hcn.Private,
		Name: name,
		Ipams: []hcn.Ipam{{
			Type: "Static",
			Subnets: []hcn.Subnet{{
				IpAddressPrefix: record.Subnet,
				Routes: []hcn.Route{{
					NextHop:           record.Gateway,
					DestinationPrefix: managedNATRoute,
				}},
			}},
		}},
		SchemaVersion: hcn.SchemaVersion{Major: 2, Minor: 0},
	}
	created, err := hcnNetwork.Create()
	if err != nil {
		return nil, fmt.Errorf("create HNS named network %q: %w", name, err)
	}
	return created, nil
}

// createStaticNetworkEndpoint creates an HNS endpoint bound to an explicit
// static IP — the registry-allocated member address — rather than letting HNS
// pick one from the IPAM pool. This gives each named-network member the stable
// address the registry recorded, matching the firecracker named path.
func createStaticNetworkEndpoint(hcnNetwork *hcn.HostComputeNetwork, runtimeID, ip string, prefix int, runtimeNetwork vmkit.NetworkConfig) (networkAttachment, error) {
	name := managedEndpointNamePrefix + runtimeID
	if old, err := hcn.GetEndpointByName(name); err == nil {
		_ = old.Delete()
	} else if !hcn.IsNotFoundError(err) {
		return networkAttachment{}, fmt.Errorf("find stale HNS endpoint %q: %w", name, err)
	}
	endpoint := &hcn.HostComputeEndpoint{
		Name:               name,
		HostComputeNetwork: hcnNetwork.Id,
		SchemaVersion:      hcn.V2SchemaVersion(),
		IpConfigurations: []hcn.IpConfig{{
			IpAddress:    ip,
			PrefixLength: uint8(prefix),
		}},
	}
	created, err := endpoint.Create()
	if err != nil {
		return networkAttachment{}, fmt.Errorf("create static HNS endpoint %q: %w", name, err)
	}
	runtimeNetwork.IP = fmt.Sprintf("%s/%d", ip, prefix)
	return networkAttachment{
		NetworkID:         hcnNetwork.Id,
		NetworkEndpointID: created.Id,
		RuntimeNetwork:    &runtimeNetwork,
	}, nil
}

// ensureManagedMediatedNetwork lazily creates the shared Private (non-NAT) HNS
// network that backs egress-mediated user/nat workspaces. It mirrors the managed
// NAT network's addressing — a free /24 picked the same way, carrying the gateway
// and a 0.0.0.0/0 default route — so the guest's static config and route decision
// are identical to the NAT path and its OUTPUT-hook nft REDIRECT still fires. The
// difference is the network TYPE: Private installs no WinNAT, so the default route
// leads nowhere off-box. That is the host-enforced no-uplink guarantee: a guest
// that flushes its own nft rules cannot reach the physical network over the NIC;
// only the host mediator over hvsock (AF_VSOCK, independent of the NIC) remains.
// Idempotent so concurrent mediated starts converge on the one shared network.
func ensureManagedMediatedNetwork() (*hcn.HostComputeNetwork, error) {
	existing, err := hcn.GetNetworkByName(managedMediatedNetworkName)
	if err == nil {
		return existing, nil
	}
	if !hcn.IsNotFoundError(err) {
		return nil, fmt.Errorf("find HNS mediated network %q: %w", managedMediatedNetworkName, err)
	}
	// Avoid overlapping any existing HNS network (including the managed NAT one),
	// the same fail-closed selection the NAT network uses.
	subnet, gateway, err := pickManagedNATSubnet()
	if err != nil {
		return nil, err
	}
	// HNS assigns the gateway to the host vNIC for a Private network, so the guest
	// reaches the gateway (route forms) and the host — but there is no NAT behind
	// it, so no physical egress. The 0.0.0.0/0 route is what makes the guest's
	// route decision succeed so the REDIRECT capture fires.
	hcnNetwork := &hcn.HostComputeNetwork{
		Type: hcn.Private,
		Name: managedMediatedNetworkName,
		Ipams: []hcn.Ipam{{
			Type: "Static",
			Subnets: []hcn.Subnet{{
				IpAddressPrefix: subnet,
				Routes: []hcn.Route{{
					NextHop:           gateway,
					DestinationPrefix: managedNATRoute,
				}},
			}},
		}},
		SchemaVersion: hcn.SchemaVersion{Major: 2, Minor: 0},
	}
	created, err := hcnNetwork.Create()
	if err != nil {
		return nil, fmt.Errorf("create HNS mediated network %q (subnet %s): %w", managedMediatedNetworkName, subnet, err)
	}
	return created, nil
}

func createNetworkEndpoint(network *hcn.HostComputeNetwork, runtimeID string, runtimeNetwork vmkit.NetworkConfig) (networkAttachment, error) {
	name := managedEndpointNamePrefix + runtimeID
	if old, err := hcn.GetEndpointByName(name); err == nil {
		_ = old.Delete()
	} else if !hcn.IsNotFoundError(err) {
		return networkAttachment{}, fmt.Errorf("find stale HNS endpoint %q: %w", name, err)
	}
	endpoint := &hcn.HostComputeEndpoint{
		Name:               name,
		HostComputeNetwork: network.Id,
		SchemaVersion:      hcn.V2SchemaVersion(),
	}
	created, err := endpoint.Create()
	if err != nil {
		return networkAttachment{}, fmt.Errorf("create HNS endpoint %q: %w", name, err)
	}
	runtimeNetwork.IP = firstEndpointIP(created, network)
	if runtimeNetwork.Subnet == "" {
		runtimeNetwork.Subnet = firstSubnet(network)
	}
	if runtimeNetwork.Gateway == "" {
		runtimeNetwork.Gateway = firstGateway(network)
	}
	if len(runtimeNetwork.DNS) == 0 {
		runtimeNetwork.DNS = append([]string{}, created.Dns.ServerList...)
		if len(runtimeNetwork.DNS) == 0 && network.Dns.ServerList != nil {
			runtimeNetwork.DNS = append([]string{}, network.Dns.ServerList...)
		}
	}
	if len(runtimeNetwork.Routes) == 0 {
		runtimeNetwork.Routes = routesFromEndpoint(created)
		if len(runtimeNetwork.Routes) == 0 {
			runtimeNetwork.Routes = routesFromNetwork(network)
		}
	}
	return networkAttachment{
		NetworkID:         network.Id,
		NetworkEndpointID: created.Id,
		RuntimeNetwork:    &runtimeNetwork,
	}, nil
}

// firstEndpointIP returns the endpoint's first IPv4 address in CIDR form —
// the shape the guest's static network config expects (and the same shape
// the Firecracker backend records). The prefix length comes from the
// endpoint, falling back to the network subnet.
func firstEndpointIP(endpoint *hcn.HostComputeEndpoint, network *hcn.HostComputeNetwork) string {
	if endpoint == nil || len(endpoint.IpConfigurations) == 0 {
		return ""
	}
	config := endpoint.IpConfigurations[0]
	if config.IpAddress == "" {
		return ""
	}
	if strings.Contains(config.IpAddress, "/") {
		return config.IpAddress
	}
	prefix := int(config.PrefixLength)
	if prefix == 0 {
		if _, subnet, err := net.ParseCIDR(firstSubnet(network)); err == nil {
			prefix, _ = subnet.Mask.Size()
		}
	}
	if prefix == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d", config.IpAddress, prefix)
}

func firstSubnet(network *hcn.HostComputeNetwork) string {
	if network == nil {
		return ""
	}
	for _, ipam := range network.Ipams {
		for _, subnet := range ipam.Subnets {
			if subnet.IpAddressPrefix != "" {
				return subnet.IpAddressPrefix
			}
		}
	}
	return ""
}

func firstGateway(network *hcn.HostComputeNetwork) string {
	if network == nil {
		return ""
	}
	for _, ipam := range network.Ipams {
		for _, subnet := range ipam.Subnets {
			for _, route := range subnet.Routes {
				if route.DestinationPrefix == managedNATRoute && route.NextHop != "" {
					return route.NextHop
				}
			}
		}
	}
	return ""
}

func routesFromEndpoint(endpoint *hcn.HostComputeEndpoint) []string {
	if endpoint == nil {
		return nil
	}
	routes := make([]string, 0, len(endpoint.Routes))
	for _, route := range endpoint.Routes {
		if route.DestinationPrefix != "" {
			routes = append(routes, route.DestinationPrefix)
		}
	}
	return routes
}

func routesFromNetwork(network *hcn.HostComputeNetwork) []string {
	if network == nil {
		return nil
	}
	var routes []string
	for _, ipam := range network.Ipams {
		for _, subnet := range ipam.Subnets {
			for _, route := range subnet.Routes {
				if route.DestinationPrefix != "" {
					routes = append(routes, route.DestinationPrefix)
				}
			}
		}
	}
	return routes
}

type unsupportedHCSClient struct{}

func (unsupportedHCSClient) CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error) {
	return computeSystemHandle{}, errHCSNotImplemented("create")
}

func (unsupportedHCSClient) GrantVMAccess(ctx context.Context, vmID, path string) error {
	return errHCSNotImplemented("grant vm access")
}

func (unsupportedHCSClient) StartComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("start")
}

func (unsupportedHCSClient) ShutdownComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("shutdown")
}

func (unsupportedHCSClient) PauseComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("pause")
}

func (unsupportedHCSClient) ResumeComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("resume")
}

func (unsupportedHCSClient) KillComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("kill")
}

func (unsupportedHCSClient) DeleteComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("delete")
}

func (unsupportedHCSClient) WaitComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("wait")
}
