#!/usr/bin/env python3
"""Check docs against generated CLI help and Go package help."""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
DOCS_CLI = ROOT / "docs" / "cli"
GO_DOC = ROOT / "docs" / "library" / "go.md"

GLOBAL_FLAGS = {"--json", "--text", "--output", "--supervisor"}
UNDOCUMENTED_HELP_COMMANDS = {"help", "exec"}
COMMAND_DOC_ALIASES = {
    "inspect": "status",
    "rm": "delete",
    "rootfs build": "rootfs",
    "kernel install": "kernel",
    "kernel verify": "kernel",
    "host setup-networking": "host",
    "artifacts get": "artifacts",
    "perf boot": "perf",
    "perf footprint": "perf",
    "perf steady": "perf",
}
NESTED_HELP_COMMANDS = {
    "rootfs build",
    "kernel install",
    "kernel verify",
    "host setup-networking",
    "artifacts get",
    "perf boot",
    "perf footprint",
    "perf steady",
    "serve mcp",
    "serve model",
    "secret check",
}
PUBLIC_GO_PACKAGES = {
    "github.com/geoffbelknap/microagent/pkg/diagnostics",
	"github.com/geoffbelknap/microagent/pkg/imagecache",
	"github.com/geoffbelknap/microagent/pkg/kernel",
	"github.com/geoffbelknap/microagent/pkg/perf",
	"github.com/geoffbelknap/microagent/pkg/rootfs",
    "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker",
    "github.com/geoffbelknap/microagent/pkg/vmkit",
    "github.com/geoffbelknap/microagent/pkg/workspace",
}

DEFAULT_GO_SYMBOL_ALLOWLIST = {
    "diagnostics": {
        "AugmentHostSupport",
        "BinaryHasNetAdmin",
        "CheckFirecracker",
        "CheckWindowsHyperV",
        "DeriveNetworkReadiness",
        "DefaultFirecrackerPathFromExecutable",
        "DefaultFirecrackerSupervisorPathFromExecutable",
        "FirecrackerProbe",
        "FirecrackerVersion",
        "FirstOutputLine",
        "NetworkRemediation",
        "ProbeUnprivilegedUserNamespace",
        "ResolveFirecrackerPath",
        "ResolveFirecrackerSupervisorPath",
        "ResolveGuestInitPath",
        "WindowsHyperVProbe",
    },
    "imagecache": {
        "CanonicalRootfsStorePath",
        "Index",
        "IndexPath",
        "MatchesRef",
        "PathInRootfsStore",
        "Provenance",
        "RecordProvenance",
        "RootfsPath",
        "Sort",
        "Upsert",
        "WriteIndex",
    },
    "kernel": {"Defaults", "ManifestEntry", "Support", "SupportForPath"},
    "rootfs": {
        "DefaultInitPath",
        "File",
        "Mount",
        "PortForward",
        "ProgressEvent",
        "ProgressFunc",
        "ValidateBundleRequest",
        "ValidateFiles",
        "ValidateRequest",
    },
    "firecracker": {
        "GuestHalted",
        "Options",
        "ResolveBinary",
        "RunForkMountExec",
        "RunPortForwarder",
        "RunVsockListener",
    },
    "vmkit": {
        "BackendAppleVF",
        "ComponentRole",
        "ContractItem",
        "ContractMediation",
        "ContractParity",
        "ContractState",
        "HostSupport",
        "KernelSupport",
        "NormalizeConfig",
        "ReadinessSignal",
        "RuntimeContract",
        "RuntimeReadiness",
        "RuntimeVerification",
        "SafeIdentifier",
        "SecretRef",
        "ValidateConfig",
        "ValidateIdentity",
        "ValidateMediationConfig",
        "ValidateNetworkConfig",
        "ValidateRequest",
        "VerificationDivergence",
        "VerifiedArtifact",
    },
    "workspace": {
        "AppleVFSupervisorPath",
        "AppleVFSupervisorPathFromExecutable",
        "ApplyProfile",
        "Artifacts",
        "ArtifactsFromOptions",
        "BackendSupportsConsoleInput",
        "BlockDeviceForBackend",
        "BuildCommandAndPort",
        "BuildRootfs",
        "BuildVerification",
        "CandidateWorkspaceRootfsPaths",
        "Cleanup",
        "Command",
        "ConfigDisks",
        "CopyFile",
        "DefaultHostname",
        "DefaultImage",
        "DefaultWorkspaceImageArm64",
        "Disk",
        "Dispatch",
        "DialShellTarget",
        "EnsureCanCreate",
        "EnsureCanStart",
        "EnsureCloneable",
        "EventFile",
        "File",
        "FileSHA256",
        "FirecrackerSupervisorPath",
        "GuestArch",
        "GuestInitPath",
        "GuestInitPathFromExecutable",
        "GuestResult",
        "HasGuestCommand",
        "HasSetupCommand",
        "HostBackend",
        "KernelPath",
        "LatestStartState",
        "LegacyKernelPath",
        "Mke2fsPath",
        "Mounts",
        "MountsForBackend",
        "NetworkConfigFromSpec",
        "DefaultHealthIntervalSeconds",
        "DefaultHealthRetries",
        "DefaultHealthStartPeriodSeconds",
        "DefaultHealthTimeoutSeconds",
        "Health",
        "NetworkConfigPtr",
        "NetworkSpec",
        "NetworkSpecFromConfig",
        "NetworkStatus",
        "NewRequestID",
        "NormalizeHealthCheck",
        "NormalizeNetworkConfig",
        "NormalizeRestartPolicy",
        "ValidateHealthCheck",
        "Output",
        "PackagedKernelPath",
        "PackagedKernelPathFromExecutable",
        "PrepareDisks",
        "Profile",
        "ProfileNames",
        "Profiles",
        "ProbeShellTarget",
        "ReadEvent",
        "ReadGuestResult",
        "ReadRuntimeResult",
        "ReadRuntimeState",
        "FinalCommandAndMode",
        "Request",
        "Resources",
        "ResourcesFromOptions",
        "ResultPath",
        "RootfsFiles",
        "RootfsPortForwards",
        "RuntimeArtifacts",
        "RuntimeState",
        "SCSIBlockDevice",
        "SerialInputPath",
        "SerialLogPath",
        "SetupStep",
        "SetupSteps",
        "MergeEnv",
        "LivePortForwardHostOnlyChange",
        "NormalizeMediationConfig",
        "OptionsFromManifest",
        "SetupCommandFromFile",
        "SecretsControlPort",
        "SecretsPort",
        "SetupCommandsFromFiles",
        "SetupCommandsFromSpec",
        "ShellCommand",
        "ShellPort",
        "ShellPortForName",
        "ShellReadinessSignal",
        "ShellSingleQuote",
        "ShellTargetDescription",
        "ShouldRestart",
        "SpecDisks",
        "StateDir",
        "Supervisor",
        "ValidateDisk",
        "ValidateFiles",
        "ValidateHostBackend",
        "ValidateHostname",
        "ValidateName",
        "ValidateOutput",
        "ValidateOutputs",
        "ValidateResources",
        "ValidateRestartPolicy",
        "VerificationForStatus",
        "VirtioBlockDevice",
        "WaitForSupervised",
        "WorkspaceDiskFilename",
        "WorkspaceDiskFormat",
        "WorkspaceDiskPath",
        "WorkspaceRootfsFilename",
        "WorkspaceRootfsFormat",
        "WorkspaceRootfsPath",
        "WritableKernelPath",
        "WriteManifest",
        "WriteProcessState",
    },
}

FLAG_RE = re.compile(r"(?<![\w-])--[A-Za-z][A-Za-z0-9-]*|(?<![\w-])-[A-Za-z](?![\w-])")
HELP_FLAG_RE = re.compile(r"^\s+(-{1,2}[A-Za-z][A-Za-z0-9-]*)\b")
COMMAND_LINE_RE = re.compile(r"^\s{2}([a-z][a-z0-9-]*(?: [a-z][a-z0-9-]*)?)\s{2,}")
GO_SYMBOL_RE = re.compile(r"^\s*(?:const|var|type|func)\s+([A-Z][A-Za-z0-9_]*)\b")


def run(cmd: list[str]) -> str:
    result = subprocess.run(
        cmd,
        cwd=ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return result.stdout


def normalize_flag(flag: str) -> str:
    if flag.startswith("--") or len(flag) == 2:
        return flag
    return "--" + flag[1:]


def microagent_cmd() -> list[str]:
    binary = os.environ.get("MICROAGENT_DOCS_PARITY_BIN")
    if binary:
        return [binary]
    return ["go", "run", "./cmd/microagent"]


def microagent_help(*args: str) -> str:
    return run([*microagent_cmd(), *args])


def parse_help_commands(help_text: str) -> set[str]:
    commands: set[str] = set()
    section = ""
    for line in help_text.splitlines():
        if line == "Commands:":
            section = "commands"
            continue
        if line == "Advanced:":
            section = "advanced"
            continue
        if line == "Options:":
            section = ""
            continue
        if section not in {"commands", "advanced"}:
            continue
        match = COMMAND_LINE_RE.match(line)
        if match:
            commands.add(match.group(1))
    return commands


def parse_help_flags(help_text: str) -> set[str]:
    flags: set[str] = set()
    for line in help_text.splitlines():
        match = HELP_FLAG_RE.match(line)
        if match:
            flags.add(normalize_flag(match.group(1)))
    return flags


def doc_page_for_command(command: str) -> Path:
    stem = COMMAND_DOC_ALIASES.get(command, command.split()[0])
    return DOCS_CLI / f"{stem}.md"


def documented_flags(page: Path) -> set[str]:
    text = page.read_text(encoding="utf-8")
    return {normalize_flag(flag) for flag in FLAG_RE.findall(text)}


def check_cli() -> list[str]:
    errors: list[str] = []
    help_commands = parse_help_commands(microagent_help("help"))
    for command in sorted(help_commands):
        if command in UNDOCUMENTED_HELP_COMMANDS:
            continue
        page = doc_page_for_command(command)
        if not page.exists():
            errors.append(f"CLI command {command!r} is missing {page.relative_to(ROOT)}")
            continue
        if command in NESTED_HELP_COMMANDS:
            help_text = microagent_help(*command.split(), "--help")
        else:
            help_text = microagent_help(command, "--help")
        help_flags = parse_help_flags(help_text)
        doc_flags = documented_flags(page) | GLOBAL_FLAGS
        missing = sorted(help_flags - doc_flags)
        for flag in missing:
            errors.append(
                f"{page.relative_to(ROOT)}: help for {command!r} includes {flag}, but the page does not"
            )
    return errors


def go_packages() -> list[dict[str, str]]:
    text = run(["go", "list", "-json", "./pkg/..."])
    packages: list[dict[str, str]] = []
    current: dict[str, str] = {}
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith('"ImportPath":'):
            current["import_path"] = stripped.split(":", 1)[1].strip().strip('",')
        elif stripped.startswith('"Name":'):
            current["name"] = stripped.split(":", 1)[1].strip().strip('",')
        elif stripped == "}":
            if current.get("import_path") in PUBLIC_GO_PACKAGES:
                packages.append(current)
            current = {}
    return sorted(packages, key=lambda item: item["import_path"])


def exported_symbols(import_path: str) -> set[str]:
    text = run(["go", "doc", "-short", import_path])
    symbols: set[str] = set()
    for line in text.splitlines():
        match = GO_SYMBOL_RE.match(line)
        if match:
            symbols.add(match.group(1))
    return symbols


def check_go() -> list[str]:
    errors: list[str] = []
    if not GO_DOC.exists():
        return [f"{GO_DOC.relative_to(ROOT)} is missing"]
    doc = GO_DOC.read_text(encoding="utf-8")
    packages = go_packages()
    package_paths = {pkg["import_path"] for pkg in packages}
    missing_packages = sorted(PUBLIC_GO_PACKAGES - package_paths)
    for import_path in missing_packages:
        errors.append(f"{import_path} is in the docs parity public package list but go list did not return it")
    for pkg in packages:
        import_path = pkg["import_path"]
        name = pkg["name"]
        if f"`{import_path.removeprefix('github.com/geoffbelknap/microagent/')}`" not in doc:
            errors.append(f"{GO_DOC.relative_to(ROOT)}: missing package `{import_path}`")
        allowed = DEFAULT_GO_SYMBOL_ALLOWLIST.get(name, set())
        for symbol in sorted(exported_symbols(import_path) - allowed):
            if re.search(rf"`{re.escape(symbol)}`|\b{re.escape(symbol)}\b", doc) is None:
                errors.append(
                    f"{GO_DOC.relative_to(ROOT)}: missing exported {name}.{symbol}; "
                    f"document it or add it to DEFAULT_GO_SYMBOL_ALLOWLIST"
                )
    errors.extend(check_cli_library_mapping(doc))
    return errors


def check_cli_library_mapping(doc: str) -> list[str]:
    errors: list[str] = []
    help_commands = parse_help_commands(microagent_help("help"))
    for command in sorted(help_commands):
        if command in UNDOCUMENTED_HELP_COMMANDS:
            continue
        if f"`microagent {command}`" not in doc:
            errors.append(
                f"{GO_DOC.relative_to(ROOT)}: CLI command {command!r} is missing from the CLI ↔ library mapping table"
            )
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("target", nargs="?", choices=("all", "cli", "go"), default="all")
    args = parser.parse_args()

    errors: list[str] = []
    if args.target in {"all", "cli"}:
        errors.extend(check_cli())
    if args.target in {"all", "go"}:
        errors.extend(check_go())

    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    print(f"docs parity ok ({args.target})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
