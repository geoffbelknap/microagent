//go:build windows

package windows_hyperv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestBuildComputeSystemDocumentUsesKernelDirectAndRootVHD(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			MemoryMiB:  768,
			CPUCount:   3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Owner                             string `json:"Owner"`
		ShouldTerminateOnLastHandleClosed bool   `json:"ShouldTerminateOnLastHandleClosed"`
		SchemaVersion                     struct {
			Major int `json:"Major"`
			Minor int `json:"Minor"`
		} `json:"SchemaVersion"`
		VirtualMachine struct {
			Chipset struct {
				LinuxKernelDirect struct {
					KernelFilePath string `json:"KernelFilePath"`
					KernelCmdLine  string `json:"KernelCmdLine"`
				} `json:"LinuxKernelDirect"`
			} `json:"Chipset"`
			ComputeTopology struct {
				Memory struct {
					SizeInMB int `json:"SizeInMB"`
				} `json:"Memory"`
				Processor struct {
					Count int `json:"Count"`
				} `json:"Processor"`
			} `json:"ComputeTopology"`
			Devices struct {
				Scsi map[string]struct {
					Attachments map[string]struct {
						Type     string `json:"Type"`
						Path     string `json:"Path"`
						ReadOnly bool   `json:"ReadOnly"`
					} `json:"Attachments"`
				} `json:"Scsi"`
				ComPorts map[string]struct {
					NamedPipe string `json:"NamedPipe"`
				} `json:"ComPorts"`
			} `json:"Devices"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Owner != "microagent" || doc.SchemaVersion.Major != 2 || doc.SchemaVersion.Minor != 1 {
		t.Fatalf("document header = %#v", doc)
	}
	if doc.ShouldTerminateOnLastHandleClosed {
		t.Fatal("document should keep the compute system alive after create handle close")
	}
	if doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelFilePath != "C:\\microagent\\Image" {
		t.Fatalf("kernel path = %q", doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelFilePath)
	}
	cmdline := doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine
	for _, want := range []string{"root=/dev/sda", "rw", "init=/sbin/microagent-init", "initcall_blacklist=virtio_vsock_init"} {
		if !strings.Contains(cmdline, want) {
			t.Fatalf("kernel cmdline %q missing %q", cmdline, want)
		}
	}
	if len(doc.VirtualMachine.Devices.ComPorts) != 0 {
		t.Fatalf("unexpected com ports without result listener: %#v", doc.VirtualMachine.Devices.ComPorts)
	}
	attachment := doc.VirtualMachine.Devices.Scsi["0"].Attachments["0"]
	if attachment.Type != "VirtualDisk" || attachment.Path != "C:\\microagent\\rootfs.vhd" || attachment.ReadOnly {
		t.Fatalf("root attachment = %#v", attachment)
	}
	if doc.VirtualMachine.ComputeTopology.Memory.SizeInMB != 768 || doc.VirtualMachine.ComputeTopology.Processor.Count != 3 {
		t.Fatalf("topology = %#v", doc.VirtualMachine.ComputeTopology)
	}
}

func TestBuildComputeSystemDocumentEmitsSecretsAndModelCmdline(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath:      "C:\\microagent\\Image",
			RootfsPath:      "C:\\microagent\\rootfs.vhd",
			SecretsPort:     1026,
			OnDemandSecrets: []vmkit.SecretRef{{Name: "DB", Ref: "env:X"}},
			// The control port is snapshot-only (Firecracker); it must NOT
			// reach the cmdline so the guest never starts a listener the
			// host will not dial.
			SecretsControlPort: 1027,
			ModelGuestPort:     18080,
			ModelVsockPort:     1028,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		VirtualMachine struct {
			Chipset struct {
				LinuxKernelDirect struct {
					KernelCmdLine string `json:"KernelCmdLine"`
				} `json:"LinuxKernelDirect"`
			} `json:"Chipset"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	cmdline := doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine
	for _, want := range []string{
		"microagent_secrets_port=1026",
		"microagent_secrets_api=1",
		"microagent_model_fwd=18080:1028",
	} {
		if !strings.Contains(cmdline, want) {
			t.Fatalf("kernel cmdline %q missing %q", cmdline, want)
		}
	}
	if strings.Contains(cmdline, "microagent_secrets_ctl_port") {
		t.Fatalf("kernel cmdline %q must not emit the snapshot-only control port", cmdline)
	}
}

func TestBuildComputeSystemDocumentEmitsGuestNetworkCmdline(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
				IP:      "192.168.127.5/24",
				Gateway: "192.168.127.1",
				DNS:     []string{"192.168.127.1"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		VirtualMachine struct {
			Chipset struct {
				LinuxKernelDirect struct {
					KernelCmdLine string `json:"KernelCmdLine"`
				} `json:"LinuxKernelDirect"`
			} `json:"Chipset"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	cmdline := doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine
	for _, want := range []string{
		"microagent_net_if=eth0",
		"microagent_net_ip=192.168.127.5/24",
		"microagent_net_gw=192.168.127.1",
		"microagent_net_dns=192.168.127.1",
	} {
		if !strings.Contains(cmdline, want) {
			t.Fatalf("kernel cmdline %q missing %q", cmdline, want)
		}
	}

	isolated, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network:    &vmkit.NetworkConfig{Mode: "isolated"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(isolated), "microagent_net_if") {
		t.Fatalf("isolated cmdline must not carry guest network config: %s", isolated)
	}
}

func TestBuildComputeSystemDocumentAttachesConfiguredDisks(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Disks: []vmkit.Disk{
				{Name: "config", Path: "C:\\microagent\\config.vhd", Mountpoint: "/config", Mode: "ro"},
				{Name: "work", Path: "C:\\microagent\\work.vhd", Mountpoint: "/work", Mode: "rw"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		VirtualMachine struct {
			Devices struct {
				Scsi map[string]struct {
					Attachments map[string]struct {
						Type     string `json:"Type"`
						Path     string `json:"Path"`
						ReadOnly bool   `json:"ReadOnly"`
					} `json:"Attachments"`
				} `json:"Scsi"`
			} `json:"Devices"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	attachments := doc.VirtualMachine.Devices.Scsi["0"].Attachments
	if len(attachments) != 3 {
		t.Fatalf("attachments = %#v, want rootfs plus two data disks", attachments)
	}
	if attachments["0"].Path != "C:\\microagent\\rootfs.vhd" {
		t.Fatalf("root attachment = %#v", attachments["0"])
	}
	if got := attachments["1"]; got.Type != "VirtualDisk" || got.Path != "C:\\microagent\\config.vhd" || !got.ReadOnly {
		t.Fatalf("config disk attachment = %#v", got)
	}
	if got := attachments["2"]; got.Type != "VirtualDisk" || got.Path != "C:\\microagent\\work.vhd" || got.ReadOnly {
		t.Fatalf("work disk attachment = %#v", got)
	}
}

func TestBuildComputeSystemDocumentAddsSerialPipeForResultRuns(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Identity: vmkit.Identity{
			RuntimeID: "agent-1",
		},
		Config: vmkit.Config{
			KernelPath:     "C:\\microagent\\Image",
			RootfsPath:     "C:\\microagent\\rootfs.vhd",
			StateDir:       "C:\\state",
			VsockListeners: []vmkit.VsockListener{{Port: 1024, Target: "C:\\state\\agent-1\\result.json"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		VirtualMachine struct {
			Chipset struct {
				LinuxKernelDirect struct {
					KernelCmdLine string `json:"KernelCmdLine"`
				} `json:"LinuxKernelDirect"`
			} `json:"Chipset"`
			Devices struct {
				ComPorts map[string]struct {
					NamedPipe string `json:"NamedPipe"`
				} `json:"ComPorts"`
			} `json:"Devices"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine, "console=ttyS0,115200") {
		t.Fatalf("kernel cmdline = %q", doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine)
	}
	if got := doc.VirtualMachine.Devices.ComPorts["0"].NamedPipe; got != serialPipePath("agent-1") {
		t.Fatalf("serial pipe = %q, want %q", got, serialPipePath("agent-1"))
	}
}

func TestBuildComputeSystemDocumentAddsHvSocketServicesForVsockListeners(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			VsockListeners: []vmkit.VsockListener{
				{Port: 1024, Target: "C:\\state\\agent-1\\result.json"},
				{Port: 2048, Target: "127.0.0.1:9900"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		VirtualMachine struct {
			Devices struct {
				HvSocket struct {
					HvSocketConfig struct {
						ServiceTable map[string]struct {
							AllowWildcardBinds        bool   `json:"AllowWildcardBinds"`
							BindSecurityDescriptor    string `json:"BindSecurityDescriptor"`
							ConnectSecurityDescriptor string `json:"ConnectSecurityDescriptor"`
						} `json:"ServiceTable"`
					} `json:"HvSocketConfig"`
				} `json:"HvSocket"`
			} `json:"Devices"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint32{1024, 2048} {
		serviceID := winio.VsockServiceID(port).String()
		service, ok := doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable[serviceID]
		if !ok {
			t.Fatalf("missing HvSocket service for port %d (%s): %#v", port, serviceID, doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable)
		}
		if !service.AllowWildcardBinds || service.BindSecurityDescriptor == "" || service.ConnectSecurityDescriptor == "" {
			t.Fatalf("service %d config = %#v", port, service)
		}
	}
}

func TestBuildComputeSystemDocumentAddsHvSocketServiceForShellPort(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			ShellPort:  22001,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc computeSystemDocument
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	serviceID := winio.VsockServiceID(22001).String()
	service, ok := doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable[serviceID]
	if !ok {
		t.Fatalf("shell service %s missing from %#v", serviceID, doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable)
	}
	if service.ConnectSecurityDescriptor != "D:P(A;;FA;;;WD)" {
		t.Fatalf("shell service connect security descriptor = %q", service.ConnectSecurityDescriptor)
	}
}

func TestBuildComputeSystemDocumentAddsHvSocketServiceForExecPort(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			ExecPort:   25279,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc computeSystemDocument
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	serviceID := winio.VsockServiceID(25279).String()
	service, ok := doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable[serviceID]
	if !ok {
		t.Fatalf("exec service %s missing from %#v", serviceID, doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable)
	}
	if service.ConnectSecurityDescriptor != "D:P(A;;FA;;;WD)" {
		t.Fatalf("exec service connect security descriptor = %q", service.ConnectSecurityDescriptor)
	}
}

func TestBuildComputeSystemDocumentExecServiceUsesGuestExecPort(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath:    "C:\\microagent\\Image",
			RootfsPath:    "C:\\microagent\\rootfs.vhd",
			ExecPort:      25279,
			GuestExecPort: 42001,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc computeSystemDocument
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	table := doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable
	if serviceID := winio.VsockServiceID(42001).String(); table[serviceID].ConnectSecurityDescriptor == "" {
		t.Fatalf("guest exec service %s missing from %#v", serviceID, table)
	}
	if serviceID := winio.VsockServiceID(25279).String(); table[serviceID].ConnectSecurityDescriptor != "" {
		t.Fatalf("host exec port %s should not be registered when a guest exec port is set", serviceID)
	}
}

func TestBuildComputeSystemDocumentAddsHvSocketServicesForPortForwards(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network: &vmkit.NetworkConfig{
				Mode: "nat",
				PortForwards: []vmkit.PortForward{
					{Protocol: "tcp", Host: "127.0.0.1", HostPort: 18080, GuestPort: 8080},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc computeSystemDocument
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	serviceID := winio.VsockServiceID(18080).String()
	service, ok := doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable[serviceID]
	if !ok {
		t.Fatalf("port forward service %s missing from %#v", serviceID, doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.ServiceTable)
	}
	if service.ConnectSecurityDescriptor != "D:P(A;;FA;;;WD)" {
		t.Fatalf("port forward service connect security descriptor = %q", service.ConnectSecurityDescriptor)
	}
}

func TestBuildComputeSystemDocumentAddsNetworkAdapterForEndpoint(t *testing.T) {
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name:              "agent-1",
		NetworkEndpointID: "endpoint-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		VirtualMachine struct {
			Devices struct {
				NetworkAdapters map[string]struct {
					EndpointID string `json:"EndpointId"`
				} `json:"NetworkAdapters"`
			} `json:"Devices"`
		} `json:"VirtualMachine"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	adapter, ok := doc.VirtualMachine.Devices.NetworkAdapters["0"]
	if !ok || adapter.EndpointID != "endpoint-1" {
		t.Fatalf("network adapters = %#v", doc.VirtualMachine.Devices.NetworkAdapters)
	}
}

func TestDefaultAdapterCreatePassesDocumentToHCSClient(t *testing.T) {
	client := &fakeHCSClient{}
	handle, err := (defaultAdapter{client: client}).Create(context.Background(), computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != "agent-1" || client.createdID != "agent-1" || !json.Valid(client.document) {
		t.Fatalf("handle=%#v client=%#v", handle, client)
	}
}

func TestDefaultAdapterCreateGrantsVMAccessToAttachedVHDs(t *testing.T) {
	client := &fakeHCSClient{
		handle: computeSystemHandle{
			ID:        "agent-1",
			RuntimeID: "11111111-1111-1111-1111-111111111111",
		},
	}
	_, err := (defaultAdapter{client: client}).Create(context.Background(), computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Disks: []vmkit.Disk{
				{Name: "config", Path: "C:\\microagent\\config.vhd", Mountpoint: "/config", Mode: "ro"},
				{Name: "work", Path: "C:\\microagent\\work.vhd", Mountpoint: "/work", Mode: "rw"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"C:\\microagent\\rootfs.vhd", "C:\\microagent\\config.vhd", "C:\\microagent\\work.vhd"}
	if len(client.grants) != len(wantPaths) {
		t.Fatalf("grants = %#v, want paths %#v", client.grants, wantPaths)
	}
	for i, want := range wantPaths {
		if client.grants[i].vmID != "11111111-1111-1111-1111-111111111111" || client.grants[i].path != want {
			t.Fatalf("grant[%d] = %#v, want runtime ID and path %q", i, client.grants[i], want)
		}
	}
}

type fakeHCSClient struct {
	createdID string
	document  []byte
	handle    computeSystemHandle
	grants    []struct {
		vmID string
		path string
	}
}

func (f *fakeHCSClient) CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error) {
	f.createdID = id
	f.document = append([]byte{}, document...)
	if f.handle.ID != "" || f.handle.RuntimeID != "" {
		return f.handle, nil
	}
	return computeSystemHandle{ID: id}, nil
}

func (f *fakeHCSClient) GrantVMAccess(ctx context.Context, vmID, path string) error {
	f.grants = append(f.grants, struct {
		vmID string
		path string
	}{vmID: vmID, path: path})
	return nil
}

func (f *fakeHCSClient) StartComputeSystem(ctx context.Context, id string) error {
	return nil
}

func (f *fakeHCSClient) ShutdownComputeSystem(ctx context.Context, id string) error {
	return nil
}

func (f *fakeHCSClient) KillComputeSystem(ctx context.Context, id string) error {
	return nil
}

func (f *fakeHCSClient) DeleteComputeSystem(ctx context.Context, id string) error {
	return nil
}

func (f *fakeHCSClient) GetComputeSystemStatistics(ctx context.Context, id string) (string, error) {
	return "{}", nil
}

func (f *fakeHCSClient) DescribeComputeSystem(ctx context.Context, id string) (string, error) {
	return "{}", nil
}

func (f *fakeHCSClient) ProbeComputeSystem(ctx context.Context, id string) error {
	return nil
}

func (f *fakeHCSClient) WaitComputeSystem(ctx context.Context, id string) error {
	return nil
}
