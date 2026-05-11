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
		Owner         string `json:"Owner"`
		SchemaVersion struct {
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
	if doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelFilePath != "C:\\microagent\\Image" {
		t.Fatalf("kernel path = %q", doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelFilePath)
	}
	cmdline := doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine
	for _, want := range []string{"root=/dev/sda", "init=/sbin/microagent-init", "initcall_blacklist=virtio_vsock_init"} {
		if !strings.Contains(cmdline, want) {
			t.Fatalf("kernel cmdline %q missing %q", cmdline, want)
		}
	}
	if len(doc.VirtualMachine.Devices.ComPorts) != 0 {
		t.Fatalf("unexpected com ports without result listener: %#v", doc.VirtualMachine.Devices.ComPorts)
	}
	attachment := doc.VirtualMachine.Devices.Scsi["0"].Attachments["0"]
	if attachment.Type != "VirtualDisk" || attachment.Path != "C:\\microagent\\rootfs.vhd" || !attachment.ReadOnly {
		t.Fatalf("root attachment = %#v", attachment)
	}
	if doc.VirtualMachine.ComputeTopology.Memory.SizeInMB != 768 || doc.VirtualMachine.ComputeTopology.Processor.Count != 3 {
		t.Fatalf("topology = %#v", doc.VirtualMachine.ComputeTopology)
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

type fakeHCSClient struct {
	createdID string
	document  []byte
}

func (f *fakeHCSClient) CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error) {
	f.createdID = id
	f.document = append([]byte{}, document...)
	return computeSystemHandle{ID: id}, nil
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
