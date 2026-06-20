//go:build windows

package windows_hyperv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/geoffbelknap/microagent/pkg/network"
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

func TestBuildComputeSystemDocumentEmitsNamedNetworkStaticCmdline(t *testing.T) {
	// A named-network member boots with the registry-allocated static member IP
	// on its synthetic NIC, the network gateway, and /etc/hosts entries so it
	// resolves the other members by name — the same static path nat/user use.
	document, err := buildComputeSystemDocument(computeSystemSpec{
		Name:              "agent-1",
		NetworkEndpointID: "endpoint-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network: &vmkit.NetworkConfig{
				Mode:    "named",
				Name:    "devnet",
				IP:      "10.44.71.2/24",
				Gateway: "10.44.71.1",
				Hosts:   []string{"web:10.44.71.2", "db:10.44.71.3"},
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
		"microagent_net_ip=10.44.71.2/24",
		"microagent_net_gw=10.44.71.1",
		"microagent_net_hosts=web:10.44.71.2,db:10.44.71.3",
	} {
		if !strings.Contains(cmdline, want) {
			t.Fatalf("named cmdline %q missing %q", cmdline, want)
		}
	}
	// A private named network has no NAT egress, so the static path must not
	// fall back to DHCP.
	if strings.Contains(cmdline, "ip=dhcp") {
		t.Fatalf("named cmdline %q must not request DHCP", cmdline)
	}
}

func TestBuildComputeSystemDocumentEmitsEgressMediatorCmdline(t *testing.T) {
	// When egress mediation is active for the workspace, the guest must be told
	// to start its transparent forwarder and which hvsock port to dial — the same
	// way the shell/exec/model ports are passed on the kernel command line. The
	// value is the shared egress.DefaultMediatorVsockPort (1032), so the guest
	// forwarder and the host mediator front-end cannot drift.
	mediated, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath:  "C:\\microagent\\Image",
			RootfsPath:  "C:\\microagent\\rootfs.vhd",
			EgressMode:  vmkit.EgressModeMediated,
			CACertPort:  1030,
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
				IP:      "192.168.127.5/24",
				Gateway: "192.168.127.1",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "microagent_egress_mediator_port=1032"; !strings.Contains(string(mediated), want) {
		t.Fatalf("mediated cmdline missing %q: %s", want, mediated)
	}
	if want := "microagent_ca_cert_port=1030"; !strings.Contains(string(mediated), want) {
		t.Fatalf("mediated cmdline missing %q: %s", want, mediated)
	}

	// Mediation OFF (default egress mode): the forwarder param must be absent so
	// an unmediated guest never installs the capture/forwarder.
	unmediated, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
				IP:      "192.168.127.5/24",
				Gateway: "192.168.127.1",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unmediated), "microagent_egress_mediator_port") {
		t.Fatalf("unmediated cmdline must not emit the egress mediator port: %s", unmediated)
	}
	if strings.Contains(string(unmediated), "microagent_ca_cert_port") {
		t.Fatalf("unmediated cmdline must not emit the ca cert port: %s", unmediated)
	}

	// Mediation requested but network mode does not mediate (bridged): no mediator
	// runs, so the guest must not be told to forward to a port nothing serves.
	bridged, err := buildComputeSystemDocument(computeSystemSpec{
		Name: "agent-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			EgressMode: vmkit.EgressModeStrict,
			Network:    &vmkit.NetworkConfig{Mode: "bridged"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bridged), "microagent_egress_mediator_port") {
		t.Fatalf("bridged (unmediated) cmdline must not emit the egress mediator port: %s", bridged)
	}
	if strings.Contains(string(bridged), "microagent_ca_cert_port") {
		t.Fatalf("bridged (unmediated) cmdline must not emit the ca cert port: %s", bridged)
	}
}

func TestMediatedUserNatSelectsPrivateNoUplinkNetwork(t *testing.T) {
	// The keystone: a mediated user/nat workspace attaches to the Private (non-NAT)
	// mediated-egress network rather than the NATting microagent-nat one. The
	// Private network hands out an IP, gateway and default route (so the guest's
	// route decision succeeds and the OUTPUT-hook nft REDIRECT fires) but has no
	// WinNAT uplink, so a guest that flushes its own nft rules has no path off-box
	// except the host mediator over hvsock. Endpoint ACLs cannot do this on a NAT
	// network — HNS routes NAT egress through WinNAT, below the ACL/VFP layer.
	//
	// The network choice is gated purely by egressMediationActive, so assert that
	// predicate drives the selection for the mediated and unmediated user/nat paths.
	mediatedUser := vmkit.Config{
		EgressMode: vmkit.EgressModeMediated,
		Network:    &vmkit.NetworkConfig{Mode: "user"},
	}
	if !egressMediationActive(&mediatedUser) {
		t.Fatal("a mediated user workspace must select the Private no-uplink network")
	}
	strictNAT := vmkit.Config{
		EgressMode: vmkit.EgressModeStrict,
		Network:    &vmkit.NetworkConfig{Mode: "nat"},
	}
	if !egressMediationActive(&strictNAT) {
		t.Fatal("a strict nat workspace must select the Private no-uplink network")
	}

	// Unmediated (default egress): keep the real NAT uplink — the working path is
	// untouched.
	unmediated := vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "user"}}
	if egressMediationActive(&unmediated) {
		t.Fatal("an unmediated user workspace must keep the NATting network (real uplink)")
	}

	// Mediation requested but the network mode does not route through the mediator
	// (bridged): no mediator runs, so it must NOT be moved onto the no-uplink
	// network or it would be stranded.
	bridged := vmkit.Config{
		EgressMode: vmkit.EgressModeStrict,
		Network:    &vmkit.NetworkConfig{Mode: "bridged"},
	}
	if egressMediationActive(&bridged) {
		t.Fatal("a bridged (unmediated) workspace must not be moved onto the no-uplink network")
	}

	// The Private mediated-egress network must be a distinct, dedicated network so
	// it never disturbs the NATting microagent-nat network unmediated workspaces use.
	if managedMediatedNetworkName == managedNATNetworkName {
		t.Fatal("the mediated-egress network must be distinct from the NAT network")
	}
}

func TestPickFreeSubnetAvoidsOverlap(t *testing.T) {
	candidates := []string{"192.168.127.0/24", "192.168.214.0/24", "10.71.214.0/24"}

	// The Default Switch's ICS /20 (192.168.112.0/20) spans 192.168.112-127, so
	// it covers the preferred 192.168.127.0/24 — the fix must skip to the next
	// clear candidate. This is the exact collision that fails NAT creation with
	// a misleading 0x34 "duplicate name" error.
	subnet, gateway, err := pickFreeSubnet(candidates, []string{"192.168.112.0/20"})
	if err != nil {
		t.Fatalf("pickFreeSubnet: %v", err)
	}
	if subnet != "192.168.214.0/24" || gateway != "192.168.214.1" {
		t.Fatalf("pickFreeSubnet = %q gw %q, want 192.168.214.0/24 gw 192.168.214.1", subnet, gateway)
	}

	// No conflicts: the preferred candidate and its .1 gateway are used.
	subnet, gateway, err = pickFreeSubnet(candidates, nil)
	if err != nil || subnet != "192.168.127.0/24" || gateway != "192.168.127.1" {
		t.Fatalf("pickFreeSubnet(nil) = %q gw %q err %v, want the preferred candidate", subnet, gateway, err)
	}

	// Every candidate conflicts: fail closed rather than create an overlapping
	// network HNS would reject.
	if _, _, err := pickFreeSubnet(candidates, []string{"0.0.0.0/0"}); err == nil {
		t.Fatal("pickFreeSubnet must fail closed when every candidate overlaps")
	}
}

func TestCIDROverlapsAny(t *testing.T) {
	if !cidrOverlapsAny("192.168.127.0/24", []string{"10.0.0.0/8", "192.168.112.0/20"}) {
		t.Fatal("192.168.127.0/24 must overlap 192.168.112.0/20")
	}
	if cidrOverlapsAny("192.168.214.0/24", []string{"192.168.112.0/20", "10.0.0.0/8"}) {
		t.Fatal("192.168.214.0/24 must not overlap 192.168.112.0/20 or 10.0.0.0/8")
	}
	if cidrOverlapsAny("192.168.127.0/24", nil) {
		t.Fatal("no existing prefixes means no overlap")
	}
}

func TestBuildComputeSystemDocumentBridgedWithoutStaticIPRequestsDHCP(t *testing.T) {
	// A bridged endpoint on a network that does not statically allocate an
	// address (external vSwitch, or the ICS Default Switch serving DHCP) has a
	// NIC but no IP. The guest must be told to DHCP rather than left with an
	// unconfigured, down interface.
	cmdlineOf := func(t *testing.T, document []byte) string {
		t.Helper()
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
		return doc.VirtualMachine.Chipset.LinuxKernelDirect.KernelCmdLine
	}

	dhcp, err := buildComputeSystemDocument(computeSystemSpec{
		Name:              "agent-1",
		NetworkEndpointID: "endpoint-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network:    &vmkit.NetworkConfig{Mode: "bridged", Interface: "Default Switch"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmdline := cmdlineOf(t, dhcp)
	if !strings.Contains(cmdline, "ip=dhcp") {
		t.Fatalf("bridged-without-static-IP cmdline %q must request DHCP", cmdline)
	}
	if strings.Contains(cmdline, "microagent_net_ip") {
		t.Fatalf("bridged-without-static-IP cmdline %q must not carry a static IP", cmdline)
	}

	// A bridged endpoint that did get a static address keeps the static path
	// and does not also request DHCP.
	static, err := buildComputeSystemDocument(computeSystemSpec{
		Name:              "agent-1",
		NetworkEndpointID: "endpoint-1",
		Config: vmkit.Config{
			KernelPath: "C:\\microagent\\Image",
			RootfsPath: "C:\\microagent\\rootfs.vhd",
			Network:    &vmkit.NetworkConfig{Mode: "bridged", IP: "192.168.5.10/24", Gateway: "192.168.5.1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	staticCmdline := cmdlineOf(t, static)
	if !strings.Contains(staticCmdline, "microagent_net_ip=192.168.5.10/24") {
		t.Fatalf("static bridged cmdline %q must carry the static IP", staticCmdline)
	}
	if strings.Contains(staticCmdline, "ip=dhcp") {
		t.Fatalf("static bridged cmdline %q must not also request DHCP", staticCmdline)
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
	pauses    int
	pauseID   string
	resumes   int
	resumeID  string
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

func (f *fakeHCSClient) PauseComputeSystem(ctx context.Context, id string) error {
	f.pauses++
	f.pauseID = id
	return nil
}

func (f *fakeHCSClient) ResumeComputeSystem(ctx context.Context, id string) error {
	f.resumes++
	f.resumeID = id
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

func TestManagedNamedNetworkName(t *testing.T) {
	if got := managedNamedNetworkName("devnet"); got != "microagent-net-devnet" {
		t.Fatalf("managedNamedNetworkName = %q", got)
	}
}

func TestSubnetPrefixLen(t *testing.T) {
	got, err := subnetPrefixLen("10.44.71.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if got != 24 {
		t.Fatalf("prefix = %d, want 24", got)
	}
	if _, err := subnetPrefixLen("not-a-subnet"); err == nil {
		t.Fatal("expected error for an invalid subnet")
	}
}

func TestNamedNetworkHosts(t *testing.T) {
	record := network.Record{Members: []network.Member{
		{Workspace: "web", IP: "10.44.71.2"},
		{Workspace: "db", IP: "10.44.71.3"},
	}}
	got := namedNetworkHosts(record)
	want := []string{"web:10.44.71.2", "db:10.44.71.3"}
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hosts[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPrepareNamedNetworkRequiresName(t *testing.T) {
	_, err := prepareNamedNetwork(computeSystemSpec{
		StateDir: t.TempDir(),
		Identity: vmkit.Identity{RuntimeID: "web"},
	}, vmkit.NetworkConfig{Mode: "named"})
	if err == nil || !strings.Contains(err.Error(), "network.name") {
		t.Fatalf("err = %v, want a missing-name error", err)
	}
}

func TestPrepareNamedNetworkFailsClosedOnUnknownNetwork(t *testing.T) {
	// An unknown network is a registry miss, surfaced before any HNS call.
	_, err := prepareNamedNetwork(computeSystemSpec{
		StateDir: t.TempDir(),
		Identity: vmkit.Identity{RuntimeID: "web"},
	}, vmkit.NetworkConfig{Mode: "named", Name: "missing"})
	if err == nil || !strings.Contains(err.Error(), "join named network") {
		t.Fatalf("err = %v, want a join-named-network error", err)
	}
}

func TestPrepareNamedNetworkJoinsRegistryBeforeHostRealization(t *testing.T) {
	// Joining the registry must allocate a stable member IP from the network's
	// subnet. The HNS realization that follows needs a real host, so the test
	// only asserts the registry-side allocation: the member is recorded and the
	// address lies inside the subnet. A nil/empty member IP here would mean the
	// adapter tried to realize the host network before the address existed.
	dir := t.TempDir()
	if _, err := network.Create(dir, "devnet", "10.44.71.0/24"); err != nil {
		t.Fatal(err)
	}
	// Realization will fail without HCS, but the join must have happened first.
	_, _ = prepareNamedNetwork(computeSystemSpec{
		StateDir: dir,
		Identity: vmkit.Identity{RuntimeID: "web"},
	}, vmkit.NetworkConfig{Mode: "named", Name: "devnet"})
	record, err := network.Get(dir, "devnet")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Members) != 1 || record.Members[0].Workspace != "web" {
		t.Fatalf("members = %#v, want web joined", record.Members)
	}
	if !strings.HasPrefix(record.Members[0].IP, "10.44.71.") {
		t.Fatalf("member IP = %q, want an address in 10.44.71.0/24", record.Members[0].IP)
	}
}
