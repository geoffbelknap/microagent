//go:build windows

package windows_hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/Microsoft/hcsshim/hcn"
	"golang.org/x/sys/windows"
)

const (
	hcsOperationPending          = syscall.Errno(0xC0370103)
	hcsComputeSystemDoesNotExist = syscall.Errno(0xC037010E)
)

const (
	hcsNotificationSystemExited          hcsNotification = 0x00000001
	hcsNotificationSystemCreateCompleted hcsNotification = 0x00000002
	hcsNotificationSystemStartCompleted  hcsNotification = 0x00000003
	hcsNotificationServiceDisconnect     hcsNotification = 0x01000000
)

type hcsNotification uint32

func (n hcsNotification) String() string {
	switch n {
	case hcsNotificationSystemExited:
		return "SystemExited"
	case hcsNotificationSystemCreateCompleted:
		return "SystemCreateCompleted"
	case hcsNotificationSystemStartCompleted:
		return "SystemStartCompleted"
	case hcsNotificationServiceDisconnect:
		return "ServiceDisconnect"
	default:
		return fmt.Sprintf("Unknown:%d", n)
	}
}

type hcsCallbackRegistration struct {
	number   uintptr
	handle   uintptr
	channels map[hcsNotification]chan error
}

var (
	nextHCSCallbackNumber uintptr
	hcsCallbackMap        = map[uintptr]*hcsCallbackRegistration{}
	hcsCallbackMapLock    sync.RWMutex
	hcsNotificationCB     = syscall.NewCallback(notificationWatcher)
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
	GetComputeSystemProperties(ctx context.Context, handle uintptr, query string) (string, string, error)
	RegisterComputeSystemCallback(ctx context.Context, handle, callback, callbackContext uintptr) (uintptr, error)
	UnregisterComputeSystemCallback(ctx context.Context, callbackHandle uintptr) error
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

func ProbeHCNAccess(ctx context.Context) error {
	_, err := hcn.ListNetworks()
	if err != nil {
		return fmt.Errorf("probe HCN/HNS access: %w", err)
	}
	return nil
}

func ProbeHvSocketAccess(ctx context.Context) error {
	return nil
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
		if errors.Is(err, hcsOperationPending) {
			reg, regErr := registerComputeSystemCallback(ctx, api, handle)
			if regErr != nil {
				_ = api.CloseComputeSystem(ctx, handle)
				return computeSystemHandle{}, hcsCallError("register create callback", "", regErr)
			}
			waitErr := reg.wait(ctx, hcsNotificationSystemCreateCompleted)
			unregisterErr := reg.unregister(ctx, api)
			if waitErr != nil {
				_ = api.CloseComputeSystem(ctx, handle)
				return computeSystemHandle{}, hcsCallError("create wait", result, waitErr)
			}
			if unregisterErr != nil {
				_ = api.CloseComputeSystem(ctx, handle)
				return computeSystemHandle{}, hcsCallError("unregister create callback", "", unregisterErr)
			}
		}
		runtimeID, propErr := computeSystemRuntimeID(ctx, api, handle)
		if propErr != nil {
			_ = api.CloseComputeSystem(ctx, handle)
			return computeSystemHandle{}, hcsCallError("get properties after create", "", propErr)
		}
		if closeErr := api.CloseComputeSystem(ctx, handle); closeErr != nil {
			return computeSystemHandle{}, hcsCallError("close after create", "", closeErr)
		}
		return computeSystemHandle{ID: id, RuntimeID: runtimeID}, nil
	}
	return computeSystemHandle{ID: id}, nil
}

func (c vmcomputeClient) GrantVMAccess(ctx context.Context, vmID, path string) error {
	if vmID == "" {
		return fmt.Errorf("windows-hyperv HCS grant vm access requires vm ID")
	}
	if path == "" {
		return fmt.Errorf("windows-hyperv HCS grant vm access requires path")
	}
	vmIDPtr, err := syscall.UTF16PtrFromString(vmID)
	if err != nil {
		return hcsCallError("grant vm access", "", err)
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return hcsCallError("grant vm access", "", err)
	}
	if err := callHRESULT(procGrantVMAccess, uintptr(unsafe.Pointer(vmIDPtr)), uintptr(unsafe.Pointer(pathPtr))); err != nil {
		return hcsCallError("grant vm access", "", err)
	}
	return nil
}

func computeSystemRuntimeID(ctx context.Context, api vmcomputeAPI, handle uintptr) (string, error) {
	query := `{}`
	properties, result, err := api.GetComputeSystemProperties(ctx, handle, query)
	if err != nil {
		return "", hcsCallError("get properties", result, err)
	}
	var decoded struct {
		RuntimeID string `json:"RuntimeId"`
	}
	if err := json.Unmarshal([]byte(properties), &decoded); err != nil {
		return "", err
	}
	return decoded.RuntimeID, nil
}

func (c vmcomputeClient) StartComputeSystem(ctx context.Context, id string) error {
	return c.withComputeSystem(ctx, id, "start", hcsNotificationSystemStartCompleted, func(handle uintptr) (string, error) {
		return c.vmcomputeAPI().StartComputeSystem(ctx, handle, "")
	})
}

func (c vmcomputeClient) ShutdownComputeSystem(ctx context.Context, id string) error {
	return c.withComputeSystem(ctx, id, "shutdown", 0, func(handle uintptr) (string, error) {
		return c.vmcomputeAPI().ShutdownComputeSystem(ctx, handle, "{}")
	})
}

func (c vmcomputeClient) KillComputeSystem(ctx context.Context, id string) error {
	return c.terminateComputeSystem(ctx, id, "kill")
}

func (c vmcomputeClient) DeleteComputeSystem(ctx context.Context, id string) error {
	return c.terminateComputeSystem(ctx, id, "delete")
}

func (c vmcomputeClient) WaitComputeSystem(ctx context.Context, id string) error {
	return c.withComputeSystem(ctx, id, "wait", hcsNotificationSystemExited, func(handle uintptr) (string, error) {
		return "", hcsOperationPending
	})
}

// ProbeComputeSystem opens and closes the compute system, reporting the
// missing-system error when HCS no longer knows it.
func (c vmcomputeClient) ProbeComputeSystem(ctx context.Context, id string) error {
	return c.withComputeSystem(ctx, id, "probe", 0, func(handle uintptr) (string, error) {
		return "", nil
	})
}

func (c vmcomputeClient) terminateComputeSystem(ctx context.Context, id, operation string) error {
	return c.withComputeSystem(ctx, id, operation, 0, func(handle uintptr) (string, error) {
		return c.vmcomputeAPI().TerminateComputeSystem(ctx, handle, "")
	})
}

func (c vmcomputeClient) withComputeSystem(ctx context.Context, id, operation string, pendingNotification hcsNotification, fn func(uintptr) (string, error)) error {
	api := c.vmcomputeAPI()
	handle, result, err := api.OpenComputeSystem(ctx, id)
	if err != nil {
		return hcsCallError(operation+" open", result, err)
	}
	if handle == 0 {
		return fmt.Errorf("windows-hyperv HCS %s open returned an empty handle", operation)
	}
	defer func() { _ = api.CloseComputeSystem(ctx, handle) }()
	var reg *hcsCallbackRegistration
	if pendingNotification != 0 {
		reg, err = registerComputeSystemCallback(ctx, api, handle)
		if err != nil {
			return hcsCallError(operation+" register callback", "", err)
		}
		defer func() { _ = reg.unregister(ctx, api) }()
	}
	result, err = fn(handle)
	if err != nil && !errors.Is(err, hcsOperationPending) {
		return hcsCallError(operation, result, err)
	}
	if errors.Is(err, hcsOperationPending) && pendingNotification != 0 {
		if waitErr := reg.wait(ctx, pendingNotification); waitErr != nil {
			return hcsCallError(operation+" wait", result, waitErr)
		}
	}
	return nil
}

func registerComputeSystemCallback(ctx context.Context, api vmcomputeAPI, handle uintptr) (*hcsCallbackRegistration, error) {
	reg := &hcsCallbackRegistration{
		channels: map[hcsNotification]chan error{
			hcsNotificationServiceDisconnect:     make(chan error, 1),
			hcsNotificationSystemExited:          make(chan error, 1),
			hcsNotificationSystemCreateCompleted: make(chan error, 1),
			hcsNotificationSystemStartCompleted:  make(chan error, 1),
		},
	}
	hcsCallbackMapLock.Lock()
	reg.number = nextHCSCallbackNumber
	nextHCSCallbackNumber++
	hcsCallbackMap[reg.number] = reg
	hcsCallbackMapLock.Unlock()

	callbackHandle, err := api.RegisterComputeSystemCallback(ctx, handle, hcsNotificationCB, reg.number)
	if err != nil {
		hcsCallbackMapLock.Lock()
		delete(hcsCallbackMap, reg.number)
		hcsCallbackMapLock.Unlock()
		return nil, err
	}
	reg.handle = callbackHandle
	return reg, nil
}

func (r *hcsCallbackRegistration) wait(ctx context.Context, expected hcsNotification) error {
	expectedChannel := r.channels[expected]
	if expectedChannel == nil {
		return fmt.Errorf("unsupported HCS notification wait: %s", expected)
	}
	select {
	case err := <-expectedChannel:
		return err
	case err := <-r.channels[hcsNotificationSystemExited]:
		if expected == hcsNotificationSystemExited {
			return err
		}
		return fmt.Errorf("unexpected HCS notification %s: %w", hcsNotificationSystemExited, err)
	case err := <-r.channels[hcsNotificationServiceDisconnect]:
		return fmt.Errorf("unexpected HCS notification %s: %w", hcsNotificationServiceDisconnect, err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *hcsCallbackRegistration) unregister(ctx context.Context, api vmcomputeAPI) error {
	if r == nil || r.handle == 0 {
		return nil
	}
	if err := api.UnregisterComputeSystemCallback(ctx, r.handle); err != nil {
		return err
	}
	hcsCallbackMapLock.Lock()
	delete(hcsCallbackMap, r.number)
	hcsCallbackMapLock.Unlock()
	for _, ch := range r.channels {
		close(ch)
	}
	r.handle = 0
	return nil
}

func notificationWatcher(notification hcsNotification, callbackNumber uintptr, notificationStatus uintptr, notificationData *uint16) uintptr {
	hcsCallbackMapLock.RLock()
	reg := hcsCallbackMap[callbackNumber]
	hcsCallbackMapLock.RUnlock()
	if reg == nil {
		return 0
	}
	ch := reg.channels[notification]
	if ch == nil {
		return 0
	}
	var err error
	if int32(notificationStatus) < 0 {
		err = syscall.Errno(notificationStatus)
	}
	ch <- err
	return 0
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
	procHcsCreateComputeSystem             = vmcomputeDLL.NewProc("HcsCreateComputeSystem")
	procHcsOpenComputeSystem               = vmcomputeDLL.NewProc("HcsOpenComputeSystem")
	procHcsCloseComputeSystem              = vmcomputeDLL.NewProc("HcsCloseComputeSystem")
	procHcsGetComputeSystemProperties      = vmcomputeDLL.NewProc("HcsGetComputeSystemProperties")
	procHcsStartComputeSystem              = vmcomputeDLL.NewProc("HcsStartComputeSystem")
	procHcsShutdownComputeSystem           = vmcomputeDLL.NewProc("HcsShutdownComputeSystem")
	procHcsTerminateComputeSystem          = vmcomputeDLL.NewProc("HcsTerminateComputeSystem")
	procHcsRegisterComputeSystemCallback   = vmcomputeDLL.NewProc("HcsRegisterComputeSystemCallback")
	procHcsUnregisterComputeSystemCallback = vmcomputeDLL.NewProc("HcsUnregisterComputeSystemCallback")
	procGrantVMAccess                      = vmcomputeDLL.NewProc("GrantVmAccess")
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

func (windowsVMComputeAPI) GetComputeSystemProperties(ctx context.Context, handle uintptr, query string) (string, string, error) {
	queryPtr, err := syscall.UTF16PtrFromString(query)
	if err != nil {
		return "", "", err
	}
	var properties *uint16
	var result *uint16
	err = callHRESULT(procHcsGetComputeSystemProperties, handle, uintptr(unsafe.Pointer(queryPtr)), uintptr(unsafe.Pointer(&properties)), uintptr(unsafe.Pointer(&result)))
	return convertAndFreeCoTaskMemString(properties), convertAndFreeCoTaskMemString(result), err
}

func (windowsVMComputeAPI) RegisterComputeSystemCallback(ctx context.Context, handle, callback, callbackContext uintptr) (uintptr, error) {
	var callbackHandle uintptr
	err := callHRESULT(procHcsRegisterComputeSystemCallback, handle, callback, callbackContext, uintptr(unsafe.Pointer(&callbackHandle)))
	return callbackHandle, err
}

func (windowsVMComputeAPI) UnregisterComputeSystemCallback(ctx context.Context, callbackHandle uintptr) error {
	return callHRESULT(procHcsUnregisterComputeSystemCallback, callbackHandle)
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
