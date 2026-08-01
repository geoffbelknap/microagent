package main

import (
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "help" || arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func shouldUseHighLevelCreate(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if wantsHelp(args) {
		return true
	}
	if hasFlagValue(args, "dry-run") && !hasFlagValue(args, "rootfs") && !hasFlagValue(args, "request-json") && !hasFlagValue(args, "vsock") {
		return true
	}
	if hasLowLevelCreateFlag(args) {
		return false
	}
	if hasFlagValue(args, "image") || hasPositionalWorkspaceName(args) {
		return true
	}
	return hasFlagValue(args, "file") || hasFlagValue(args, "name") || hasFlagValue(args, "id") || hasFlagValue(args, "setup") || hasFlagValue(args, "setup-file") || hasFlagValue(args, "entrypoint") || hasFlagValue(args, "shell") || hasFlagValue(args, "hostname") || hasFlagValue(args, "env") || hasFlagValue(args, "e") || hasFlagValue(args, "disk") || hasFlagValue(args, "bundle") || hasFlagValue(args, "volume") || hasFlagValue(args, "v") || hasFlagValue(args, "output")
}

func hasLowLevelCreateFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--rootfs", "-rootfs", "--request-json", "-request-json", "--dry-run", "-dry-run", "--request-id", "-request-id", "--role", "-role", "--vsock", "-vsock":
			return true
		}
		if strings.HasPrefix(arg, "--rootfs=") ||
			strings.HasPrefix(arg, "-rootfs=") ||
			strings.HasPrefix(arg, "--request-json=") ||
			strings.HasPrefix(arg, "-request-json=") ||
			strings.HasPrefix(arg, "--request-id=") ||
			strings.HasPrefix(arg, "-request-id=") ||
			strings.HasPrefix(arg, "--role=") ||
			strings.HasPrefix(arg, "-role=") ||
			strings.HasPrefix(arg, "--vsock=") ||
			strings.HasPrefix(arg, "-vsock=") {
			return true
		}
	}
	return false
}

func hostBackend() string {
	return workspace.HostBackend()
}

func defaultGuestArch() string {
	return workspace.GuestArch()
}

func defaultWorkspaceImage(arch string) string {
	return workspace.DefaultImage(arch)
}

func defaultStateDir() string {
	return workspace.StateDir()
}

func defaultKernelPath(backend, arch string) string {
	return workspace.KernelPath(backend, arch)
}

func defaultAppleVFSupervisorPath() string {
	return workspace.AppleVFSupervisorPath()
}

func defaultSupervisorPath(backend string) string {
	if backend == vmkit.BackendAppleVF {
		return defaultAppleVFSupervisorPath()
	}
	return ""
}

func defaultPackagedKernelPathFromExecutable(executable, backend, arch string) string {
	return workspace.PackagedKernelPathFromExecutable(executable, backend, arch)
}

func defaultMke2fsPath() string {
	return workspace.Mke2fsPath()
}

func defaultDebugFSPath() string {
	path, _ := workspace.LookupE2fsprogsTool("debugfs")
	return path
}

func defaultE2fsckPath() string {
	path, _ := workspace.LookupE2fsprogsTool("e2fsck")
	return path
}

func defaultResize2fsPath() string {
	return workspace.Resize2fsPath()
}

func defaultGuestInitPath(arch string) string {
	return workspace.GuestInitPath(arch)
}

func hasFlagValue(args []string, name string) bool {
	_, ok := flagValue(args, name)
	return ok
}

func flagValue(args []string, name string) (string, bool) {
	long := "--" + name
	short := "-" + name
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == long || arg == short {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(arg, long+"=") {
			return strings.TrimPrefix(arg, long+"="), true
		}
		if strings.HasPrefix(arg, short+"=") {
			return strings.TrimPrefix(arg, short+"="), true
		}
	}
	return "", false
}

func hasPositionalWorkspaceName(args []string) bool {
	// --request-json (any of --request-json/-request-json, "=" or
	// space-separated) always means the low-level request-file path owns
	// this invocation. Without this check, the naive scan below doesn't
	// know --request-json takes a value, so it walks straight into that
	// value (a bare file path with no "-" prefix) and misreads it as the
	// positional workspace name.
	if hasFlagValue(args, "request-json") {
		return false
	}
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			return true
		}
		if arg == "--json" || arg == "-json" || arg == "--rootfs" || arg == "-rootfs" || arg == "--kernel" || arg == "-kernel" || arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" || arg == "--file" || arg == "-file" || arg == "--entrypoint" || arg == "-entrypoint" || arg == "--shell" || arg == "-shell" || arg == "--hostname" || arg == "-hostname" || arg == "--env" || arg == "-env" {
			return false
		}
	}
	return false
}

func hasWorkspaceStateTarget(args []string) bool {
	// See hasPositionalWorkspaceName: --request-json's value (a bare file
	// path) looks like an unqualified positional target to the scan below,
	// which only knows to skip over --name/--id's value and otherwise treats
	// any non-flag token as the workspace name/id. Rule out --request-json
	// up front so its value never gets misread as a workspace-state target.
	if hasFlagValue(args, "request-json") {
		return false
	}
	for i, arg := range args {
		if arg == "--json" || arg == "-json" {
			return false
		}
		if arg == "--name" || arg == "-name" || arg == "--id" || arg == "-id" {
			return i+1 < len(args)
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return true
	}
	return false
}

func workspaceCommand(opts workspaceOptions) string {
	return workspace.Command(opts)
}

func shellSingleQuote(value string) string {
	return workspace.ShellSingleQuote(value)
}
