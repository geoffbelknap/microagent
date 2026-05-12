//go:build windows

package windows_hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
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
		if err := client.GrantVMAccess(ctx, handle.RuntimeID, spec.Config.RootfsPath); err != nil {
			return computeSystemHandle{}, err
		}
	}
	return handle, nil
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
	kernelCmdLine := "root=/dev/sda ro rootwait init=/sbin/microagent-init initcall_blacklist=virtio_vsock_init pci=off"
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
					"0": {Attachments: map[string]attachment{
						"0": {Type: "VirtualDisk", Path: spec.Config.RootfsPath, ReadOnly: true},
					}},
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
	runtimeNetwork.IP = firstEndpointIP(created)
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

func firstEndpointIP(endpoint *hcn.HostComputeEndpoint) string {
	if endpoint == nil || len(endpoint.IpConfigurations) == 0 {
		return ""
	}
	return endpoint.IpConfigurations[0].IpAddress
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
