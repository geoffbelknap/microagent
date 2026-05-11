//go:build windows

package windows_hyperv

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	hcsOperationPending          = syscall.Errno(0xC0370103)
	hcsComputeSystemDoesNotExist = syscall.Errno(0xC037010E)
)

type vmcomputeClient struct {
	api vmcomputeAPI
}

type vmcomputeAPI interface {
	CreateComputeSystem(ctx context.Context, id, document string) (uintptr, string, error)
	OpenComputeSystem(ctx context.Context, id string) (uintptr, string, error)
	CloseComputeSystem(ctx context.Context, handle uintptr) error
	StartComputeSystem(ctx context.Context, handle uintptr, options string) (string, error)
	ShutdownComputeSystem(ctx context.Context, handle uintptr, options string) (string, error)
	TerminateComputeSystem(ctx context.Context, handle uintptr, options string) (string, error)
}

func newVMComputeClient() vmcomputeClient {
	return vmcomputeClient{api: windowsVMComputeAPI{}}
}

func ProbeHCSAccess(ctx context.Context) error {
	api := newVMComputeClient().vmcomputeAPI()
	handle, result, err := api.OpenComputeSystem(ctx, "__microagent_hcs_access_probe__")
	if handle != 0 {
		_ = api.CloseComputeSystem(ctx, handle)
	}
	if err == nil || errors.Is(err, hcsComputeSystemDoesNotExist) {
		return nil
	}
	return hcsCallError("probe access", result, err)
}

func (c vmcomputeClient) vmcomputeAPI() vmcomputeAPI {
	if c.api != nil {
		return c.api
	}
	return windowsVMComputeAPI{}
}

func (c vmcomputeClient) CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error) {
	api := c.vmcomputeAPI()
	handle, result, err := api.CreateComputeSystem(ctx, id, string(document))
	if err != nil && !errors.Is(err, hcsOperationPending) {
		return computeSystemHandle{}, hcsCallError("create", result, err)
	}
	if handle != 0 {
		if closeErr := api.CloseComputeSystem(ctx, handle); closeErr != nil {
			return computeSystemHandle{}, hcsCallError("close after create", "", closeErr)
		}
	}
	return computeSystemHandle{ID: id}, nil
}

func (c vmcomputeClient) StartComputeSystem(ctx context.Context, id string) error {
	return c.withComputeSystem(ctx, id, "start", func(handle uintptr) (string, error) {
		return c.vmcomputeAPI().StartComputeSystem(ctx, handle, "{}")
	})
}

func (c vmcomputeClient) ShutdownComputeSystem(ctx context.Context, id string) error {
	return c.withComputeSystem(ctx, id, "shutdown", func(handle uintptr) (string, error) {
		return c.vmcomputeAPI().ShutdownComputeSystem(ctx, handle, "{}")
	})
}

func (c vmcomputeClient) KillComputeSystem(ctx context.Context, id string) error {
	return c.terminateComputeSystem(ctx, id, "kill")
}

func (c vmcomputeClient) DeleteComputeSystem(ctx context.Context, id string) error {
	return c.terminateComputeSystem(ctx, id, "delete")
}

func (c vmcomputeClient) terminateComputeSystem(ctx context.Context, id, operation string) error {
	return c.withComputeSystem(ctx, id, operation, func(handle uintptr) (string, error) {
		return c.vmcomputeAPI().TerminateComputeSystem(ctx, handle, "{}")
	})
}

func (c vmcomputeClient) withComputeSystem(ctx context.Context, id, operation string, fn func(uintptr) (string, error)) error {
	api := c.vmcomputeAPI()
	handle, result, err := api.OpenComputeSystem(ctx, id)
	if err != nil {
		return hcsCallError(operation+" open", result, err)
	}
	if handle == 0 {
		return fmt.Errorf("windows-hyperv HCS %s open returned an empty handle", operation)
	}
	defer api.CloseComputeSystem(ctx, handle)
	result, err = fn(handle)
	if err != nil && !errors.Is(err, hcsOperationPending) {
		return hcsCallError(operation, result, err)
	}
	return nil
}

func hcsCallError(operation, result string, err error) error {
	if result != "" {
		return fmt.Errorf("windows-hyperv HCS %s: %w: %s", operation, err, result)
	}
	return fmt.Errorf("windows-hyperv HCS %s: %w", operation, err)
}

type windowsVMComputeAPI struct{}

var vmcomputeDLL = windows.NewLazySystemDLL("vmcompute.dll")

var (
	procHcsCreateComputeSystem    = vmcomputeDLL.NewProc("HcsCreateComputeSystem")
	procHcsOpenComputeSystem      = vmcomputeDLL.NewProc("HcsOpenComputeSystem")
	procHcsCloseComputeSystem     = vmcomputeDLL.NewProc("HcsCloseComputeSystem")
	procHcsStartComputeSystem     = vmcomputeDLL.NewProc("HcsStartComputeSystem")
	procHcsShutdownComputeSystem  = vmcomputeDLL.NewProc("HcsShutdownComputeSystem")
	procHcsTerminateComputeSystem = vmcomputeDLL.NewProc("HcsTerminateComputeSystem")
)

func (windowsVMComputeAPI) CreateComputeSystem(ctx context.Context, id, document string) (uintptr, string, error) {
	idPtr, err := syscall.UTF16PtrFromString(id)
	if err != nil {
		return 0, "", err
	}
	documentPtr, err := syscall.UTF16PtrFromString(document)
	if err != nil {
		return 0, "", err
	}
	var handle uintptr
	var result *uint16
	err = callHRESULT(procHcsCreateComputeSystem, uintptr(unsafe.Pointer(idPtr)), uintptr(unsafe.Pointer(documentPtr)), 0, uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&result)))
	return handle, convertAndFreeCoTaskMemString(result), err
}

func (windowsVMComputeAPI) OpenComputeSystem(ctx context.Context, id string) (uintptr, string, error) {
	idPtr, err := syscall.UTF16PtrFromString(id)
	if err != nil {
		return 0, "", err
	}
	var handle uintptr
	var result *uint16
	err = callHRESULT(procHcsOpenComputeSystem, uintptr(unsafe.Pointer(idPtr)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&result)))
	return handle, convertAndFreeCoTaskMemString(result), err
}

func (windowsVMComputeAPI) CloseComputeSystem(ctx context.Context, handle uintptr) error {
	return callHRESULT(procHcsCloseComputeSystem, handle)
}

func (windowsVMComputeAPI) StartComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	return callComputeSystemOperation(procHcsStartComputeSystem, handle, options)
}

func (windowsVMComputeAPI) ShutdownComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	return callComputeSystemOperation(procHcsShutdownComputeSystem, handle, options)
}

func (windowsVMComputeAPI) TerminateComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	return callComputeSystemOperation(procHcsTerminateComputeSystem, handle, options)
}

func callComputeSystemOperation(proc *windows.LazyProc, handle uintptr, options string) (string, error) {
	optionsPtr, err := syscall.UTF16PtrFromString(options)
	if err != nil {
		return "", err
	}
	var result *uint16
	err = callHRESULT(proc, handle, uintptr(unsafe.Pointer(optionsPtr)), uintptr(unsafe.Pointer(&result)))
	return convertAndFreeCoTaskMemString(result), err
}

func callHRESULT(proc *windows.LazyProc, args ...uintptr) error {
	if err := proc.Find(); err != nil {
		return err
	}
	r0, _, _ := syscall.SyscallN(proc.Addr(), args...)
	if int32(r0) < 0 {
		if r0&0x1fff0000 == 0x00070000 {
			r0 &= 0xffff
		}
		return syscall.Errno(r0)
	}
	return nil
}

func convertAndFreeCoTaskMemString(value *uint16) string {
	if value == nil {
		return ""
	}
	out := windows.UTF16PtrToString(value)
	windows.CoTaskMemFree(unsafe.Pointer(value))
	return out
}

var _ hcsClient = vmcomputeClient{}
