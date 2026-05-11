//go:build windows

package windows_hyperv

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVMComputeClientCreatePassesDocumentAndClosesHandle(t *testing.T) {
	api := &fakeVMComputeAPI{
		nextHandle:         42,
		propertiesResponse: `{"RuntimeId":"11111111-1111-1111-1111-111111111111"}`,
	}
	client := vmcomputeClient{api: api}

	handle, err := client.CreateComputeSystem(context.Background(), "agent-1", []byte(`{"Owner":"microagent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != "agent-1" {
		t.Fatalf("handle.ID = %q", handle.ID)
	}
	if handle.RuntimeID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("handle.RuntimeID = %q", handle.RuntimeID)
	}
	if api.createdID != "agent-1" || api.createdDocument != `{"Owner":"microagent"}` {
		t.Fatalf("create id=%q document=%q", api.createdID, api.createdDocument)
	}
	if len(api.closedHandles) != 1 || api.closedHandles[0] != 42 {
		t.Fatalf("closed handles = %#v", api.closedHandles)
	}
}

func TestVMComputeClientControlCommandsOpenOperateAndClose(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *vmcomputeClient) error
		want string
	}{
		{name: "start", run: func(ctx context.Context, c *vmcomputeClient) error { return c.StartComputeSystem(ctx, "agent-1") }, want: "start"},
		{name: "shutdown", run: func(ctx context.Context, c *vmcomputeClient) error { return c.ShutdownComputeSystem(ctx, "agent-1") }, want: "shutdown"},
		{name: "kill", run: func(ctx context.Context, c *vmcomputeClient) error { return c.KillComputeSystem(ctx, "agent-1") }, want: "terminate"},
		{name: "delete", run: func(ctx context.Context, c *vmcomputeClient) error { return c.DeleteComputeSystem(ctx, "agent-1") }, want: "terminate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeVMComputeAPI{nextHandle: 77}
			client := &vmcomputeClient{api: api}

			if err := tt.run(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			if api.openedID != "agent-1" {
				t.Fatalf("openedID = %q", api.openedID)
			}
			if len(api.operations) != 1 || api.operations[0] != tt.want {
				t.Fatalf("operations = %#v, want %q", api.operations, tt.want)
			}
			if len(api.closedHandles) != 1 || api.closedHandles[0] != 77 {
				t.Fatalf("closed handles = %#v", api.closedHandles)
			}
		})
	}
}

func TestVMComputeClientWaitsForSystemExitNotification(t *testing.T) {
	api := &fakeVMComputeAPI{nextHandle: 77}
	client := vmcomputeClient{api: api}

	done := make(chan error, 1)
	go func() {
		done <- client.WaitComputeSystem(context.Background(), "agent-1")
	}()

	select {
	case err := <-done:
		t.Fatalf("WaitComputeSystem returned before exit notification: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if api.registers != 1 {
		t.Fatalf("registers = %d, want 1", api.registers)
	}

	api.notify(hcsNotificationSystemExited, nil)
	if err := <-done; err != nil {
		t.Fatalf("WaitComputeSystem: %v", err)
	}
	if api.unregisters != 1 {
		t.Fatalf("unregisters = %d, want 1", api.unregisters)
	}
}

func TestVMComputeClientWaitsForPendingCreateNotification(t *testing.T) {
	api := &fakeVMComputeAPI{
		nextHandle:         42,
		createErr:          hcsOperationPending,
		propertiesResponse: `{"RuntimeId":"11111111-1111-1111-1111-111111111111"}`,
	}
	client := vmcomputeClient{api: api}

	done := make(chan error, 1)
	go func() {
		_, err := client.CreateComputeSystem(context.Background(), "agent-1", []byte(`{"Owner":"microagent"}`))
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("CreateComputeSystem returned before completion notification: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if api.registers != 1 {
		t.Fatalf("registers = %d, want 1", api.registers)
	}

	api.notify(hcsNotificationSystemCreateCompleted, nil)
	if err := <-done; err != nil {
		t.Fatalf("CreateComputeSystem: %v", err)
	}
	if api.unregisters != 1 {
		t.Fatalf("unregisters = %d, want 1", api.unregisters)
	}
}

func TestVMComputeClientWaitsForPendingStartNotification(t *testing.T) {
	api := &fakeVMComputeAPI{
		nextHandle: 77,
		startErr:   hcsOperationPending,
	}
	client := vmcomputeClient{api: api}

	done := make(chan error, 1)
	go func() {
		done <- client.StartComputeSystem(context.Background(), "agent-1")
	}()

	select {
	case err := <-done:
		t.Fatalf("StartComputeSystem returned before completion notification: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if api.registers != 1 {
		t.Fatalf("registers = %d, want 1", api.registers)
	}

	api.notify(hcsNotificationSystemStartCompleted, nil)
	if err := <-done; err != nil {
		t.Fatalf("StartComputeSystem: %v", err)
	}
	if api.unregisters != 1 {
		t.Fatalf("unregisters = %d, want 1", api.unregisters)
	}
}

func TestVMComputeClientPendingStartFailsOnUnexpectedExit(t *testing.T) {
	api := &fakeVMComputeAPI{
		nextHandle: 77,
		startErr:   hcsOperationPending,
	}
	client := vmcomputeClient{api: api}

	done := make(chan error, 1)
	go func() {
		done <- client.StartComputeSystem(context.Background(), "agent-1")
	}()
	time.Sleep(50 * time.Millisecond)

	api.notify(hcsNotificationSystemExited, errors.New("guest exited"))
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "unexpected HCS notification SystemExited") {
		t.Fatalf("StartComputeSystem err = %v", err)
	}
}

type fakeVMComputeAPI struct {
	nextHandle         uintptr
	createdID          string
	createdDocument    string
	openedID           string
	operations         []string
	closedHandles      []uintptr
	createErr          error
	startErr           error
	propertiesResponse string
	callbackContext    uintptr
	registers          int
	unregisters        int
}

func (f *fakeVMComputeAPI) CreateComputeSystem(ctx context.Context, id, document string) (uintptr, string, error) {
	f.createdID = id
	f.createdDocument = document
	return f.nextHandle, "", f.createErr
}

func (f *fakeVMComputeAPI) OpenComputeSystem(ctx context.Context, id string) (uintptr, string, error) {
	f.openedID = id
	return f.nextHandle, "", nil
}

func (f *fakeVMComputeAPI) CloseComputeSystem(ctx context.Context, handle uintptr) error {
	f.closedHandles = append(f.closedHandles, handle)
	return nil
}

func (f *fakeVMComputeAPI) StartComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	f.operations = append(f.operations, "start")
	return "", f.startErr
}

func (f *fakeVMComputeAPI) ShutdownComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	f.operations = append(f.operations, "shutdown")
	return "", nil
}

func (f *fakeVMComputeAPI) TerminateComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	f.operations = append(f.operations, "terminate")
	return "", nil
}

func (f *fakeVMComputeAPI) GetComputeSystemProperties(ctx context.Context, handle uintptr, query string) (string, string, error) {
	return f.propertiesResponse, "", nil
}

func (f *fakeVMComputeAPI) RegisterComputeSystemCallback(ctx context.Context, handle, callback, callbackContext uintptr) (uintptr, error) {
	f.registers++
	f.callbackContext = callbackContext
	return 99, nil
}

func (f *fakeVMComputeAPI) UnregisterComputeSystemCallback(ctx context.Context, callbackHandle uintptr) error {
	f.unregisters++
	return nil
}

func (f *fakeVMComputeAPI) notify(notification hcsNotification, err error) {
	notificationWatcher(notification, f.callbackContext, errnoForTest(err), nil)
}

func errnoForTest(err error) uintptr {
	if err == nil {
		return 0
	}
	return uintptr(0x80004005)
}
