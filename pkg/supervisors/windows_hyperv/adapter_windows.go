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
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const (
	managedNATNetworkName     = "microagent-nat"
	managedNATSubnet          = "192.168.127.0/24"
	managedNATGateway         = "192.168.127.1"
	managedNATRoute           = "0.0.0.0/0"
	managedNATStartMAC        = "00-15-5D-52-D0-00"
	managedNATEndMAC          = "00-15-5D-52-DF-FF"
	managedEndpointNamePrefix = "microagent-"
)

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
		hcnNetwork, err := ensureManagedNATNetwork()
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
	default:
		return networkAttachment{}, fmt.Errorf("unsupported windows-hyperv network mode %q", network.Mode)
	}
}

func (a defaultAdapter) CleanupNetwork(ctx context.Context, state runtimeState) error {
	if strings.TrimSpace(state.NetworkEndpointID) == "" {
		return nil
	}
	endpoint, err := hcn.GetEndpointByID(state.NetworkEndpointID)
	if err != nil {
		if hcn.IsNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("find HNS endpoint %q: %w", state.NetworkEndpointID, err)
	}
	if err := endpoint.Delete(); err != nil && !hcn.IsNotFoundError(err) {
		return fmt.Errorf("delete HNS endpoint %q: %w", state.NetworkEndpointID, err)
	}
	return nil
}

func (a defaultAdapter) Start(ctx context.Context, id string) error {
	return a.hcsClient().StartComputeSystem(ctx, id)
}

func (a defaultAdapter) Shutdown(ctx context.Context, id string) error {
	return a.hcsClient().ShutdownComputeSystem(ctx, id)
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
	// The HNS endpoint assigned the guest its address, but the synthetic
	// NIC (hv_netvsc) comes up unconfigured: tell the guest its static
	// config the same way the Firecracker boot args do. Requires the
	// kernels-6.12.22-r2 artifact (CONFIG_HYPERV_NET).
	if network := spec.Config.Network; network != nil &&
		(network.Mode == "user" || network.Mode == "nat" || network.Mode == "bridged") &&
		network.IP != "" && network.Gateway != "" {
		kernelCmdLine += " microagent_net_if=eth0"
		kernelCmdLine += " microagent_net_ip=" + network.IP
		kernelCmdLine += " microagent_net_gw=" + network.Gateway
		if len(network.DNS) != 0 {
			kernelCmdLine += " microagent_net_dns=" + strings.Join(network.DNS, ",")
		}
		if len(network.Hosts) != 0 {
			kernelCmdLine += " microagent_net_hosts=" + strings.Join(network.Hosts, ",")
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

func ensureManagedNATNetwork() (*hcn.HostComputeNetwork, error) {
	network, err := hcn.GetNetworkByName(managedNATNetworkName)
	if err == nil {
		return network, nil
	}
	if !hcn.IsNotFoundError(err) {
		return nil, fmt.Errorf("find HNS NAT network %q: %w", managedNATNetworkName, err)
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
				IpAddressPrefix: managedNATSubnet,
				Routes: []hcn.Route{{
					NextHop:           managedNATGateway,
					DestinationPrefix: managedNATRoute,
				}},
			}},
		}},
		SchemaVersion: hcn.SchemaVersion{Major: 2, Minor: 0},
	}
	created, err := network.Create()
	if err != nil {
		return nil, fmt.Errorf("create HNS NAT network %q: %w", managedNATNetworkName, err)
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

func (unsupportedHCSClient) KillComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("kill")
}

func (unsupportedHCSClient) DeleteComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("delete")
}

func (unsupportedHCSClient) WaitComputeSystem(ctx context.Context, id string) error {
	return errHCSNotImplemented("wait")
}
