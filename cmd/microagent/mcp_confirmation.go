package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func requireConfirmedMCPHostMutation(name string, args map[string]any) (map[string]any, error) {
	if !mcpHostMutationTool(name) {
		return nil, nil
	}
	if (name == "workspace.kill" || name == "workspace.quarantine") && strings.TrimSpace(stringArg(args, "reason")) == "" {
		return nil, operation.New(operation.ErrorValidation, "%s requires a non-empty reason", name)
	}
	token := mcpConfirmationToken(name, args)
	if boolArg(args, "preview") {
		return mcpSuccessEnvelope(map[string]any{
			"preview":            true,
			"tool":               name,
			"actions":            mcpHostMutationActions(name, args),
			"confirmation_token": token,
			"confirm_with":       "call the same tool with confirm_token set to confirmation_token and preview omitted or false",
		}, mcpZeroMeta(args)), nil
	}
	if stringArg(args, "confirm_token") != token {
		return nil, operation.New(operation.ErrorPolicyDenied, "%s requires preview confirmation; call with preview=true and retry with the returned confirm_token", name)
	}
	return nil, nil
}

func mcpHostMutationTool(name string) bool {
	if operation, ok := vmkit.OperationForMCPTool(name); ok {
		return operation.Confirmation == vmkit.OperationConfirmationPreview
	}
	return false
}

func mcpHostMutationActions(name string, args map[string]any) []string {
	switch name {
	case "workspace.kill":
		return []string{"force-stop workspace runtime", "discard guest memory, processes, and live connections"}
	case "workspace.quarantine":
		return []string{"freeze workspace execution", "sever network, brokers, published ports, and host-side authority", "capture forensic evidence while frozen", "stop into durable containment custody"}
	case "kernel.install":
		return []string{"download or copy kernel artifact", "write kernel artifact to host path", "verify sha256 when supplied or defaulted"}
	case "rootfs.build":
		return []string{"pull OCI image layers when needed", "build ext4 rootfs", "write rootfs output path"}
	default:
		return nil
	}
}

func mcpConfirmationToken(name string, args map[string]any) string {
	clean := map[string]any{}
	for key, value := range args {
		switch key {
		case "preview", "confirm_token", "idempotency_key", "principal":
			continue
		default:
			clean[key] = value
		}
	}
	payload, _ := json.Marshal(map[string]any{"tool": name, "arguments": clean})
	sum := sha256.Sum256(payload)
	return "mcp-confirm-" + hex.EncodeToString(sum[:8])
}

func previewDestructiveMCPTool(name string, args map[string]any) map[string]any {
	if !boolArg(args, "preview") {
		return nil
	}
	switch name {
	case "workspace.delete":
		force := boolArg(args, "force")
		action := "delete"
		if force {
			action = "force-delete"
		}
		return mcpSuccessEnvelope(map[string]any{
			"preview":   true,
			"tool":      name,
			"workspace": stringArg(args, "name"),
			"actions":   []string{action, "remove workspace disk and state"},
		}, mcpZeroMeta(args))
	case "volume.delete":
		return mcpSuccessEnvelope(map[string]any{
			"preview": true,
			"tool":    name,
			"name":    stringArg(args, "name"),
			"actions": []string{"delete " + strings.TrimSuffix(name, ".delete")},
			"force":   boolArg(args, "force"),
		}, mcpZeroMeta(args))
	case "snapshot.delete":
		return mcpSuccessEnvelope(map[string]any{
			"preview": true,
			"tool":    name,
			"name":    stringArg(args, "name"),
			"tag":     stringArg(args, "tag"),
			"actions": []string{"delete snapshot"},
		}, mcpZeroMeta(args))
	case "workspace.resize":
		// Growing is not destructive; only preview (and thereby require an
		// explicit non-preview follow-up call) when the request is a shrink.
		target := int64Arg(args, "size_mib")
		current, ok := currentWorkspaceRootfsSizeMiB(args)
		if !ok || target >= current {
			return nil
		}
		return mcpSuccessEnvelope(map[string]any{
			"preview":       true,
			"tool":          name,
			"workspace":     stringArg(args, "name"),
			"from_size_mib": current,
			"to_size_mib":   target,
			"actions":       []string{"shrink rootfs disk"},
		}, mcpZeroMeta(args))
	case "volume.resize":
		target := int64Arg(args, "size_mib")
		record, err := volume.Get(mcpStateDirFrom(args), stringArg(args, "name"))
		if err != nil || target >= record.SizeMiB {
			return nil
		}
		return mcpSuccessEnvelope(map[string]any{
			"preview":       true,
			"tool":          name,
			"name":          stringArg(args, "name"),
			"from_size_mib": record.SizeMiB,
			"to_size_mib":   target,
			"actions":       []string{"shrink volume disk"},
		}, mcpZeroMeta(args))
	case "images.delete", "images.prune":
		actions := []string{"delete stale image records"}
		if name == "images.delete" {
			actions = []string{"delete image record"}
		}
		if boolArg(args, "delete_files") {
			actions = append(actions, "delete cached rootfs files")
		}
		return mcpSuccessEnvelope(map[string]any{
			"preview":      true,
			"tool":         name,
			"image":        stringArg(args, "image"),
			"delete_files": boolArg(args, "delete_files"),
			"actions":      actions,
		}, mcpZeroMeta(args))
	default:
		return nil
	}
}

func mcpStateDirFrom(args map[string]any) string {
	if stateDir := stringArg(args, "state_dir"); stateDir != "" {
		return stateDir
	}
	return defaultStateDir()
}

func currentWorkspaceRootfsSizeMiB(args map[string]any) (int64, bool) {
	manifest, err := workspace.ReadManifest(mcpStateDirFrom(args), stringArg(args, "name"))
	if err != nil {
		return 0, false
	}
	return manifest.Resources.SizeMiB, true
}
