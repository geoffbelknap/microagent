package workspace

import "fmt"

type WorkspaceNotFoundError struct {
	Name string
}

func (e WorkspaceNotFoundError) Error() string {
	return fmt.Sprintf("workspace %s not found", e.Name)
}

func (e WorkspaceNotFoundError) Is(target error) bool {
	_, ok := target.(WorkspaceNotFoundError)
	return ok
}
