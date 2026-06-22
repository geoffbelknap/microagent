import Foundation
import CMicroagentSandbox

#if canImport(Darwin)
import Darwin
#endif

// VMM-process confinement for the apple-vf backend (Spec B).
//
// Each workload runs in a microVM driven by this supervisor process via
// Virtualization / Hypervisor.framework. Confinement is the layer-2 boundary
// *below* the VM: if a VMM / hypervisor escape lands in this host process, it
// finds itself deprivileged —
//   * resource ceilings (setrlimit) + reduced QoS bound what it can consume,
//   * a Seatbelt sandbox denies arbitrary host filesystem writes and non-loopback
//     network egress.
//
// The Seatbelt sandbox is applied fail-closed (ASK tenet 4: an enforcement
// failure must never expand capability — if the profile can't be applied, the VM
// does not start). Resource limits and QoS are best-effort hardening: a failure
// to set them is logged but not fatal, because they degrade gracefully and the
// sandbox remains the authoritative confinement layer.

let confinementMode = "seatbelt"

func logConfinement(_ message: String) {
    FileHandle.standardError.write(Data("confinement: \(message)\n".utf8))
}

// MARK: - Resource ceilings (ASK tenet 8: operations are bounded)

#if canImport(Darwin)
// rlimInfinity is defined as a 64-bit shift macro the Swift importer rejects
// ("structure not supported"), so restate it here. Value: (1 << 63) - 1.
let rlimInfinity = rlim_t((UInt64(1) << 63) - 1)

/// Sets the *soft* limit for `resource` to `desired`, never touching the hard
/// limit (so it never requires privilege) and clamping to the hard ceiling.
///
/// When `raiseOnly` is true the soft limit is never lowered below the current
/// finite soft value — used for RLIMIT_NPROC, where the limit is enforced
/// against the whole UID's live process count, so lowering it could break
/// unrelated forks. raiseOnly still bounds a previously-unlimited soft limit.
func setSoftLimit(_ resource: Int32, to desired: rlim_t, raiseOnly: Bool = false, label: String) {
    var lim = rlimit()
    if getrlimit(resource, &lim) != 0 {
        logConfinement("getrlimit(\(label)) failed: errno \(errno)")
        return
    }
    var newSoft = desired
    if raiseOnly {
        if lim.rlim_cur != rlimInfinity {
            newSoft = max(desired, lim.rlim_cur)
        }
    }
    if lim.rlim_max != rlimInfinity && newSoft > lim.rlim_max {
        newSoft = lim.rlim_max
    }
    lim.rlim_cur = newSoft
    if setrlimit(resource, &lim) != 0 {
        logConfinement("setrlimit(\(label), \(newSoft)) failed: errno \(errno)")
    }
}
#endif

/// Applies host resource ceilings to the current (VM child) process, derived
/// from the VM configuration. Best-effort.
func applyResourceLimits(config: Config) {
    #if canImport(Darwin)
    let cpuCount = rlim_t(max(1, config.cpuCount ?? 2))
    let guestBytes = rlim_t(max(1, config.memoryMiB ?? 512)) * 1024 * 1024

    // No core dumps: a core would spill guest RAM (and any in-flight secrets)
    // to disk. Always 0.
    setSoftLimit(RLIMIT_CORE, to: 0, label: "RLIMIT_CORE")

    // Cap open descriptors. The supervisor needs fds for the rootfs + each
    // attached disk, the vsock/loopback listeners, and up to a few hundred
    // mediated connections; 4096 is generous headroom while still bounding a
    // descriptor-exhaustion escape.
    setSoftLimit(RLIMIT_NOFILE, to: 4096, label: "RLIMIT_NOFILE")

    // Bound process creation. raiseOnly because RLIMIT_NPROC is enforced against
    // the whole real-UID process table.
    setSoftLimit(RLIMIT_NPROC, to: 512 + 128 * cpuCount, raiseOnly: true, label: "RLIMIT_NPROC")

    // Bound the virtual address space. Headroom must comfortably cover the dyld
    // shared cache (several GB of VA), the Virtualization/Hypervisor framework
    // mappings, and the guest RAM backing — hence a large fixed headroom on top
    // of guest memory. This is a coarse runaway-allocation ceiling, not a tight
    // bound; tune empirically against live VM boots (Spec B).
    let asHeadroom: rlim_t = 16 * 1024 * 1024 * 1024
    setSoftLimit(RLIMIT_AS, to: guestBytes + asHeadroom, label: "RLIMIT_AS")
    #endif
}

/// Lowers the process QoS so a deprivileged escape can't starve the host. The
/// detached workload runs at utility; the interactive console keeps a higher
/// class for responsiveness.
func applyQoS(_ qos: qos_class_t) {
    #if canImport(Darwin)
    let rc = pthread_set_qos_class_self_np(qos, 0)
    if rc != 0 {
        logConfinement("pthread_set_qos_class_self_np failed: \(rc)")
    }
    #endif
}

// MARK: - Seatbelt profile

/// Quotes a path as an SBPL string literal, escaping backslashes and quotes.
func sbplQuote(_ value: String) -> String {
    var out = "\""
    for ch in value {
        switch ch {
        case "\\": out += "\\\\"
        case "\"": out += "\\\""
        default: out.append(ch)
        }
    }
    out += "\""
    return out
}

/// Returns the canonical variants of an absolute path that a sandbox check might
/// see: the standardized path, its symlink-resolved form (when it exists), and a
/// /private-prefixed variant for the classic macOS /var,/tmp,/etc symlinks. The
/// sandbox canonicalizes paths, so allowing every variant avoids false denials
/// (e.g. a TMPDIR under /var/folders that resolves to /private/var/folders).
func sandboxPathVariants(_ path: String) -> [String] {
    guard !path.isEmpty else { return [] }
    var seen = Set<String>()
    var out: [String] = []
    func add(_ candidate: String) {
        guard !candidate.isEmpty, !seen.contains(candidate) else { return }
        seen.insert(candidate)
        out.append(candidate)
    }
    let standardized = URL(fileURLWithPath: path).standardizedFileURL.path
    add(standardized)
    if FileManager.default.fileExists(atPath: standardized) {
        add(URL(fileURLWithPath: standardized).resolvingSymlinksInPath().path)
    }
    for prefix in ["/var/", "/tmp/", "/etc/"] {
        if standardized.hasPrefix(prefix) {
            add("/private" + standardized)
        }
    }
    if standardized == "/tmp" || standardized == "/var" || standardized == "/etc" {
        add("/private" + standardized)
    }
    return out
}

/// Reads a confstr(3) darwin user directory (temp/cache), if available.
func darwinUserDir(_ key: Int32) -> String? {
    #if canImport(Darwin)
    let size = confstr(key, nil, 0)
    guard size > 0 else { return nil }
    var buffer = [CChar](repeating: 0, count: size)
    guard confstr(key, &buffer, size) == size else { return nil }
    let bytes = buffer.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }
    let path = String(decoding: bytes, as: UTF8.self)
    return path.isEmpty ? nil : path
    #else
    return nil
    #endif
}

/// The per-user darwin temp + cache directories. Frameworks (Foundation,
/// Virtualization) scribble scratch state here; allowing writes to these keeps
/// the deny-by-default posture for $HOME, /etc, credentials, etc.
func darwinUserScratchDirs() -> [String] {
    var dirs: [String] = []
    #if canImport(Darwin)
    if let t = darwinUserDir(_CS_DARWIN_USER_TEMP_DIR) { dirs.append(t) }
    if let c = darwinUserDir(_CS_DARWIN_USER_CACHE_DIR) { dirs.append(c) }
    #endif
    return dirs
}

/// Builds the Seatbelt (SBPL) profile string. Pure and deterministic given its
/// inputs, so it is unit-testable.
///
/// - writableSubpaths: directory trees the child may write under (the workspace
///   runtime dir, framework scratch dirs).
/// - writableFiles: individual files the child may write (rootfs + rw disks).
func seatbeltProfile(writableSubpaths: [String], writableFiles: [String]) -> String {
    var lines: [String] = []
    lines.append("(version 1)")
    lines.append(";; microagent apple-vf VMM-process confinement (Spec B). Deny-by-default.")
    lines.append(";; Layer-2 boundary below the VM: a hypervisor escape lands here, unable to")
    lines.append(";; write outside the workspace surface, dump core, or reach non-loopback network.")
    lines.append(";; ROLLOUT (v0.8.2): file *writes* are deny-by-default with an explicit allow-list")
    lines.append(";; (the high-value confinement). Reads / mach-lookup / IOKit are intentionally")
    lines.append(";; broad until the Hypervisor.framework service + user-client set is enumerated")
    lines.append(";; empirically on a VM-booting host (watch: log stream --predicate 'sender == \"Sandbox\"').")
    lines.append("(deny default)")
    lines.append("")
    lines.append(";; process / signals (self only)")
    lines.append("(allow process-info* (target self))")
    lines.append("(allow process-fork)")
    lines.append("(allow process-exec*)")
    lines.append("(allow signal (target self))")
    lines.append("")
    lines.append(";; system introspection used by Foundation / Virtualization")
    lines.append("(allow sysctl-read)")
    lines.append("(allow mach-lookup)")
    lines.append("(allow iokit-open)")
    lines.append("(allow iokit-get-properties)")
    lines.append("")
    lines.append(";; reads: broad for now (dyld shared cache, frameworks, kernel image, config)")
    lines.append("(allow file-read*)")
    lines.append("(allow file-ioctl)")
    lines.append("")
    lines.append(";; network: loopback + local listeners only (TCP publish forwarder)")
    lines.append("(allow network-bind (local ip))")
    lines.append("(allow network-inbound (local ip))")
    lines.append("(allow network-outbound (remote ip \"localhost:*\"))")
    lines.append("")
    lines.append(";; writes: DENY by default, allow only the workspace + framework scratch surface")
    for dir in writableSubpaths {
        for variant in sandboxPathVariants(dir) {
            lines.append("(allow file-write* (subpath \(sbplQuote(variant))))")
        }
    }
    for file in writableFiles {
        for variant in sandboxPathVariants(file) {
            lines.append("(allow file-write* (literal \(sbplQuote(variant))))")
        }
    }
    return lines.joined(separator: "\n") + "\n"
}

/// Builds the workspace-specific Seatbelt profile for a VM child.
func buildSeatbeltProfile(identity: Identity, config: Config) -> String {
    var writableSubpaths = [runtimeDirectory(identity: identity, stateDir: config.stateDir).path]
    writableSubpaths.append(contentsOf: darwinUserScratchDirs())

    // rootfs is always attached read-write; rw disks need write access, ro disks
    // are covered by the broad read allow.
    var writableFiles = [config.rootfsPath]
    for disk in config.disks ?? [] where disk.mode == "rw" {
        writableFiles.append(disk.path)
    }
    return seatbeltProfile(writableSubpaths: writableSubpaths, writableFiles: writableFiles)
}

/// Applies a Seatbelt profile to the current process. Throws on failure so the
/// caller can fail closed.
func applySeatbelt(profile: String) throws {
    var errPtr: UnsafeMutablePointer<CChar>? = nil
    let rc = profile.withCString { microagent_sandbox_apply($0, &errPtr) }
    if rc != 0 {
        let detail = errPtr.map { String(cString: $0) } ?? "unknown error"
        if let errPtr { microagent_sandbox_free_error(errPtr) }
        throw ProtocolError.invalid("failed to apply Seatbelt confinement (fail-closed): \(detail)")
    }
    if let errPtr { microagent_sandbox_free_error(errPtr) }
}

/// Applies full confinement (resource limits + QoS + Seatbelt) to the current VM
/// child. Resource limits and QoS are best-effort; the Seatbelt sandbox is
/// fail-closed — a throw here aborts VM start.
func applyConfinement(identity: Identity, config: Config, qos: qos_class_t) throws {
    applyResourceLimits(config: config)
    applyQoS(qos)
    try applySeatbelt(profile: buildSeatbeltProfile(identity: identity, config: config))
    logConfinement("seatbelt confinement applied for \(identity.runtimeID)")
}

// MARK: - Self-check (honesty invariant for diagnostics)

/// The profile used by the `--confinement-selfcheck` probe: the same SBPL
/// template as a real workspace, parameterized on a throwaway temp dir so the
/// profile compiles/applies on this host without referencing any workspace.
func confinementSelfCheckProfile() -> String {
    seatbeltProfile(writableSubpaths: [NSTemporaryDirectory()], writableFiles: [])
}

/// Applies the self-check profile to this (throwaway) process. Exit 0 iff the
/// Seatbelt profile is valid and sandbox_init succeeds on this host.
func runConfinementSelfCheck() -> Int32 {
    do {
        try applySeatbelt(profile: confinementSelfCheckProfile())
        return 0
    } catch {
        FileHandle.standardError.write(Data("\(error)\n".utf8))
        return 1
    }
}

/// Verifies, by spawning the `--confinement-selfcheck` probe, that Seatbelt
/// confinement actually applies on this host. Used by hostSupport() so
/// ConfinementActive is only ever reported true when it truly is.
func confinementActiveOnThisHost() -> Bool {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: currentExecutablePath())
    process.arguments = ["--confinement-selfcheck"]
    process.standardInput = FileHandle.nullDevice
    process.standardOutput = FileHandle.nullDevice
    process.standardError = FileHandle.nullDevice
    do {
        try process.run()
    } catch {
        logConfinement("self-check spawn failed: \(error)")
        return false
    }
    // Guard against a hung probe so `doctor` / `host inspect` never block.
    let watchdog = DispatchWorkItem {
        if process.isRunning { process.terminate() }
    }
    DispatchQueue.global().asyncAfter(deadline: .now() + 5, execute: watchdog)
    process.waitUntilExit()
    watchdog.cancel()
    return process.terminationStatus == 0
}
