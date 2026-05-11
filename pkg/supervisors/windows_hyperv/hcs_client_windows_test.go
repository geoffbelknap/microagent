//go:build windows

package windows_hyperv

import (
	"context"
	"testing"
)

func TestVMComputeClientCreatePassesDocumentAndClosesHandle(t *testing.T) {
	api := &fakeVMComputeAPI{nextHandle: 42}
	client := vmcomputeClient{api: api}

	handle, err := client.CreateComputeSystem(context.Background(), "agent-1", []byte(`{"Owner":"microagent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != "agent-1" {
		t.Fatalf("handle.ID = %q", handle.ID)
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

type fakeVMComputeAPI struct {
	nextHandle      uintptr
	createdID       string
	createdDocument string
	openedID        string
	operations      []string
	closedHandles   []uintptr
}

func (f *fakeVMComputeAPI) CreateComputeSystem(ctx context.Context, id, document string) (uintptr, string, error) {
	f.createdID = id
	f.createdDocument = document
	return f.nextHandle, "", nil
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
	return "", nil
}

func (f *fakeVMComputeAPI) ShutdownComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	f.operations = append(f.operations, "shutdown")
	return "", nil
}

func (f *fakeVMComputeAPI) TerminateComputeSystem(ctx context.Context, handle uintptr, options string) (string, error) {
	f.operations = append(f.operations, "terminate")
	return "", nil
}
