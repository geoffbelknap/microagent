package windows_hyperv

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestHostResponseReportsBackend(t *testing.T) {
	resp := HostResponse()
	if resp.Backend != vmkit.BackendWindowsHyperV || resp.Host == nil || resp.Kernel == nil {
		t.Fatalf("HostResponse = %#v", resp)
	}
	if resp.Host.Backend != vmkit.BackendWindowsHyperV || resp.Kernel.Backend != vmkit.BackendWindowsHyperV {
		t.Fatalf("backend fields = %#v %#v", resp.Host, resp.Kernel)
	}
	if runtime.GOOS == "windows" && !resp.OK {
		t.Fatalf("windows host response not OK: %#v", resp)
	}
	if runtime.GOOS != "windows" && resp.OK {
		t.Fatalf("non-windows host response OK: %#v", resp)
	}
}

func TestCheckCommandFailsClosedOffWindows(t *testing.T) {
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/Image",
			RootfsPath: "/tmp/rootfs.vhd",
			StateDir:   t.TempDir(),
		},
	}
	resp, err := (Supervisor{}).Do(context.Background(), req)
	if runtime.GOOS == "windows" {
		if err != nil || !resp.OK {
			t.Fatalf("windows check resp=%#v err=%v", resp, err)
		}
		return
	}
	if err == nil || resp.OK || !strings.Contains(resp.Error, "only supported on windows") {
		t.Fatalf("non-windows check resp=%#v err=%v", resp, err)
	}
}

func TestLifecycleCommandsFailClosedBeforeHCSImplementation(t *testing.T) {
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/Image",
			RootfsPath: "/tmp/rootfs.vhd",
			StateDir:   t.TempDir(),
		},
	}
	resp, err := (Supervisor{}).Do(context.Background(), req)
	if err == nil || resp.OK {
		t.Fatalf("run resp=%#v err=%v, want fail-closed error", resp, err)
	}
	if runtime.GOOS == "windows" && !strings.Contains(resp.Error, "not implemented yet") {
		t.Fatalf("windows error = %q", resp.Error)
	}
	if runtime.GOOS != "windows" && !strings.Contains(resp.Error, "only supported on windows") {
		t.Fatalf("non-windows error = %q", resp.Error)
	}
}

func TestSupervisorUsesInjectedAdapterForHostAndCheck(t *testing.T) {
	adapter := &fakeAdapter{
		host: vmkit.HostSupport{
			Backend:                 vmkit.BackendWindowsHyperV,
			Architecture:            "testarch",
			FrameworkAvailable:      true,
			VirtualizationSupported: true,
			ConsoleAvailable:        true,
			ConsoleMode:             "interactive",
		},
	}
	supervisor := Supervisor{adapter: adapter}
	resp, err := supervisor.Do(context.Background(), vmkit.Request{Command: "host"})
	if err != nil || !resp.OK || resp.Host == nil || resp.Host.Architecture != "testarch" {
		t.Fatalf("host resp=%#v err=%v", resp, err)
	}
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendWindowsHyperV,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/Image",
			RootfsPath: "/tmp/rootfs.vhd",
			StateDir:   t.TempDir(),
		},
	}
	resp, err = supervisor.Do(context.Background(), req)
	if err != nil || !resp.OK || adapter.checks != 1 {
		t.Fatalf("check resp=%#v err=%v checks=%d", resp, err, adapter.checks)
	}
}

type fakeAdapter struct {
	host   vmkit.HostSupport
	checks int
}

func (f *fakeAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return f.host, nil
}

func (f *fakeAdapter) Check(ctx context.Context) error {
	f.checks++
	return nil
}

func (f *fakeAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	return computeSystemHandle{ID: "fake"}, nil
}

func (f *fakeAdapter) Start(ctx context.Context, id string) error {
	return nil
}

func (f *fakeAdapter) Shutdown(ctx context.Context, id string) error {
	return nil
}

func (f *fakeAdapter) Kill(ctx context.Context, id string) error {
	return nil
}

func (f *fakeAdapter) Delete(ctx context.Context, id string) error {
	return nil
}

var _ runtimeAdapter = (*fakeAdapter)(nil)

func TestInjectedAdapterHostErrorsBecomeStructuredResponses(t *testing.T) {
	supervisor := Supervisor{adapter: failingAdapter{}}
	resp, err := supervisor.Do(context.Background(), vmkit.Request{Command: "host"})
	if err == nil || resp.OK || !strings.Contains(resp.Error, "host unavailable") {
		t.Fatalf("host error resp=%#v err=%v", resp, err)
	}
}

type failingAdapter struct{}

func (failingAdapter) Host(ctx context.Context) (vmkit.HostSupport, error) {
	return vmkit.HostSupport{Backend: vmkit.BackendWindowsHyperV, Architecture: "testarch"}, fmt.Errorf("host unavailable")
}

func (failingAdapter) Check(ctx context.Context) error {
	return fmt.Errorf("check unavailable")
}

func (failingAdapter) Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error) {
	return computeSystemHandle{}, fmt.Errorf("create unavailable")
}

func (failingAdapter) Start(ctx context.Context, id string) error {
	return fmt.Errorf("start unavailable")
}

func (failingAdapter) Shutdown(ctx context.Context, id string) error {
	return fmt.Errorf("shutdown unavailable")
}

func (failingAdapter) Kill(ctx context.Context, id string) error {
	return fmt.Errorf("kill unavailable")
}

func (failingAdapter) Delete(ctx context.Context, id string) error {
	return fmt.Errorf("delete unavailable")
}
