package workspace

import (
	"context"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func TestWorkspaceValidationErrorsAreTyped(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "create name",
			run: func() error {
				_, err := Create(context.Background(), Options{})
				return err
			},
		},
		{
			name: "run command",
			run: func() error {
				_, err := Run(context.Background(), Options{})
				return err
			},
		},
		{
			name: "start name",
			run: func() error {
				_, err := Start(context.Background(), Options{})
				return err
			},
		},
		{
			name: "apply name",
			run: func() error {
				_, err := Apply(context.Background(), Options{}, Spec{})
				return err
			},
		},
		{
			name: "snapshot capture tag",
			run: func() error {
				_, err := Snapshot(context.Background(), Options{Name: "agent-1"}, "../../../planted")
				return err
			},
		},
		{
			name: "snapshot fork tag",
			run: func() error {
				_, err := CreateFromSnapshot(context.Background(), Options{Name: "fork"}, "source", "../../../planted")
				return err
			},
		},
		{
			name: "snapshot restore tag",
			run: func() error {
				_, err := Start(context.Background(), Options{Name: "agent-1", FromSnapshot: "../../../planted"})
				return err
			},
		},
		{name: "workspace name", run: func() error { return ValidateName("../escape") }},
		{name: "snapshot tag", run: func() error { return validateTag("../escape") }},
		{name: "resources", run: func() error { return ValidateResources(Resources{}, true) }},
		{name: "restart policy", run: func() error { return ValidateRestartPolicy("sometimes") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !operation.IsKind(err, operation.ErrorValidation) {
				t.Fatalf("error = %#v, want typed validation error", err)
			}
		})
	}
}
