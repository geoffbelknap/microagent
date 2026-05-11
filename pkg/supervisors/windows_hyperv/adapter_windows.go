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
	"github.com/geoffbelknap/microagent/pkg/vmkit"
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
	return a.hcsClient().CreateComputeSystem(ctx, spec.Name, document)
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
	Scsi     map[string]scsiController `json:"Scsi,omitempty"`
	HvSocket hvSocket                  `json:"HvSocket"`
	Plan9    map[string]any            `json:"Plan9"`
	ComPorts map[string]comPort        `json:"ComPorts,omitempty"`
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
	kernelCmdLine := "root=/dev/sda ro rootwait init=/sbin/microagent-init initcall_blacklist=virtio_vsock_init pci=off"
	comPorts := map[string]comPort(nil)
	if hasResultListener(spec) {
		kernelCmdLine += " 8250_core.nr_uarts=1 8250_core.skip_txen_test=1 console=ttyS0,115200"
		comPorts = map[string]comPort{"0": {NamedPipe: serialPipePath(spec.Identity.RuntimeID)}}
	} else {
		kernelCmdLine += " 8250_core.nr_uarts=0 panic=-1 quiet"
	}
	doc := computeSystemDocument{
		Owner:                             "microagent",
		SchemaVersion:                     versionDocument{Major: 2, Minor: 1},
		ShouldTerminateOnLastHandleClosed: true,
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
				Plan9:    map[string]any{},
				ComPorts: comPorts,
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

type unsupportedHCSClient struct{}

func (unsupportedHCSClient) CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error) {
	return computeSystemHandle{}, errHCSNotImplemented("create")
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
