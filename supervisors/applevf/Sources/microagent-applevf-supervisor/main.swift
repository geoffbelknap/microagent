import Foundation
#if canImport(ObjectiveC)
import ObjectiveC
#endif

#if canImport(Darwin)
import Darwin
#endif

#if canImport(Virtualization)
@preconcurrency import Virtualization
#endif

enum ComponentRole: String, Codable {
    case workload
    case enforcer
}

enum VMState: String, Codable {
    case unknown
    case prepared
    case starting
    case running
    case paused
    case stopped
    case halted
    case quarantined
    case failed
}

struct Identity: Codable {
    var requestID: String
    var runtimeID: String
    var role: ComponentRole
    var backend: String
    var homeHash: String?
}

struct Config: Codable {
    var kernelPath: String
    var rootfsPath: String
    var stateDir: String
    var appleVFMachineIdentifier: String?
    var appleVFNetworkMACAddress: String?
    var memoryMiB: Int?
    var cpuCount: Int?
    var disks: [Disk]?
    var vsockListeners: [VsockListener]?
    var mediation: MediationConfig?
    var network: NetworkConfig?
    var shellPort: UInt16?
    var execPort: UInt16?
    var leaseSeconds: Int?
    var guestShellPort: UInt16?
    var guestExecPort: UInt16?
    var secretsPort: UInt32?
    var secrets: [SecretRef]?
    var secretEnvFiles: [String]?
    var onDemandSecrets: [SecretRef]?
    var secretsAudit: Bool?
    var secretsControlPort: UInt32?
    var caCertPort: UInt32?
    var egressMode: String?
    var egressAllow: [String]?
    var egressPassthrough: [String]?
    var egressSwapConfigPath: String?
    var egressMaxBytesPerSec: Int64?
    var egressMaxTotalBytes: Int64?
    var egressMaxConcurrentConns: Int32?
    var egressAuditMaxBytes: Int64?
    var egressAuditMaxBackups: Int?
    var modelGuestPort: UInt16?
    var modelVsockPort: UInt32?
    var serialInput: Bool?
}

struct MediationConfig: Codable {
    var enabled: Bool
    var required: Bool
    var port: UInt32?
    var target: String?
    var failClosed: Bool
}

struct NetworkConfig: Codable {
    var mode: String
    var portForwards: [PortForward]?
    var dns: [String]?
    var routes: [String]?
    var ip: String?
    var subnet: String?
    var gateway: String?
}

struct PortForward: Codable {
    var protocolName: String
    var host: String?
    var hostPort: UInt16
    var guestPort: UInt16

    enum CodingKeys: String, CodingKey {
        case protocolName = "protocol"
        case host
        case hostPort
        case guestPort
    }
}

struct Disk: Codable {
    var name: String
    var path: String
    var mountpoint: String
    var mode: String
}

struct VsockListener: Codable {
    var port: UInt32
    var target: String
}

struct SecretRef: Codable {
    var name: String
    var ref: String
}

struct Request: Codable {
    var command: String
    var identity: Identity?
    var config: Config?
    var tag: String?
}

struct Event: Codable {
    var identity: Identity
    var state: VMState
    var detail: String?
    var observedAt: Date
}

struct HostSupport: Codable {
    var backend: String
    var architecture: String
    var frameworkAvailable: Bool
    var virtualizationSupported: Bool
    var supervisorPath: String?
    var supervisorAvailable: Bool?
    var pauseResumeAvailable: Bool?
    var snapshotCreateAvailable: Bool?
    var snapshotAvailable: Bool?
    var consoleAvailable: Bool?
    var consoleMode: String?
    // VMM-process confinement posture (Spec B). confinementMode is this build's
    // posture ("seatbelt"); confinementActive is true only when a self-check has
    // verified the Seatbelt profile actually applies on this host.
    var confinementMode: String?
    var confinementActive: Bool?
}

struct ReadinessSignal: Codable {
    var ready: Bool? = nil
    var observedAt: Date? = nil
    var detail: String? = nil
    var error: String? = nil
}

struct RuntimeReadiness: Codable {
    var guestReady: ReadinessSignal
    var shellReady: ReadinessSignal
    var execReady: ReadinessSignal
    var resultReady: ReadinessSignal
    var mediationReady: ReadinessSignal

    enum CodingKeys: String, CodingKey {
        case guestReady
        case shellReady
        case execReady
        case resultReady
        case mediationReady
    }

    init(
        guestReady: ReadinessSignal = ReadinessSignal(),
        shellReady: ReadinessSignal = ReadinessSignal(),
        execReady: ReadinessSignal = ReadinessSignal(),
        resultReady: ReadinessSignal = ReadinessSignal(),
        mediationReady: ReadinessSignal = ReadinessSignal()
    ) {
        self.guestReady = guestReady
        self.shellReady = shellReady
        self.execReady = execReady
        self.resultReady = resultReady
        self.mediationReady = mediationReady
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        guestReady = try container.decodeIfPresent(ReadinessSignal.self, forKey: .guestReady) ?? ReadinessSignal()
        shellReady = try container.decodeIfPresent(ReadinessSignal.self, forKey: .shellReady) ?? ReadinessSignal()
        execReady = try container.decodeIfPresent(ReadinessSignal.self, forKey: .execReady) ?? ReadinessSignal()
        resultReady = try container.decodeIfPresent(ReadinessSignal.self, forKey: .resultReady) ?? ReadinessSignal()
        mediationReady = try container.decodeIfPresent(ReadinessSignal.self, forKey: .mediationReady) ?? ReadinessSignal()
    }
}

struct RuntimeResult: Codable {
    var identity: Identity
    var backend: String?
    var resultPath: String?
    var startedAt: String?
    var completedAt: String?
    var exitCode: Int
    var stdout: String?
    var stderr: String?
    var error: String?
}

struct GuestResult: Codable {
    var startedAt: String?
    var exitedAt: String?
    var exitCode: Int
    var stdout: String?
    var stderr: String?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case startedAt = "started_at"
        case exitedAt = "exited_at"
        case exitCode = "exit_code"
        case stdout
        case stderr
        case error
    }
}

struct Response: Codable {
    var ok: Bool
    var backend: String? = nil
    var event: Event? = nil
    var host: HostSupport? = nil
    var readiness: RuntimeReadiness? = nil
    var result: RuntimeResult? = nil
    var mediation: MediationConfig? = nil
    var network: NetworkConfig? = nil
    var saveStateCheck: SaveStateCheckDiagnostics? = nil
    var error: String? = nil

    enum CodingKeys: String, CodingKey {
        case ok
        case backend
        case event
        case host
        case readiness
        case result
        case mediation
        case network
        case saveStateCheck = "save_state_check"
        case error
    }
}

struct SaveStateCheckStep: Codable {
    var name: String
    var ok: Bool
    var detail: String?
    var error: String?
    var errorDetail: ErrorDiagnostic?
    var observedAt: Date

    enum CodingKeys: String, CodingKey {
        case name
        case ok
        case detail
        case error
        case errorDetail = "error_detail"
        case observedAt = "observed_at"
    }
}

struct ErrorUserInfoEntry: Codable {
    var key: String
    var value: String
}

final class ErrorDiagnostic: Codable {
    var type: String
    var description: String
    var localizedDescription: String
    var localizedFailureReason: String?
    var localizedRecoverySuggestion: String?
    var domain: String?
    var code: Int?
    var userInfo: [ErrorUserInfoEntry]?
    var underlying: ErrorDiagnostic?

    init(type: String, description: String, localizedDescription: String, localizedFailureReason: String?, localizedRecoverySuggestion: String?, domain: String?, code: Int?, userInfo: [ErrorUserInfoEntry]?, underlying: ErrorDiagnostic?) {
        self.type = type
        self.description = description
        self.localizedDescription = localizedDescription
        self.localizedFailureReason = localizedFailureReason
        self.localizedRecoverySuggestion = localizedRecoverySuggestion
        self.domain = domain
        self.code = code
        self.userInfo = userInfo
        self.underlying = underlying
    }

    enum CodingKeys: String, CodingKey {
        case type
        case description
        case localizedDescription = "localized_description"
        case localizedFailureReason = "localized_failure_reason"
        case localizedRecoverySuggestion = "localized_recovery_suggestion"
        case domain
        case code
        case userInfo = "user_info"
        case underlying
    }
}

struct SaveStateCheckDiagnostics: Codable {
    var mode: String
    var configShape: String
    var saveStatePath: String
    var destinationExistedBefore: Bool
    var destinationParentExistsAfter: Bool?
    var destinationExistsAfter: Bool?
    var initialVMState: String?
    var stateAfterStart: String?
    var stateBeforePause: String?
    var stateAfterPause: String?
    var stateBeforeSave: String?
    var stateAfterSave: String?
    var restoreCheck: Bool?
    var restoreInitialVMState: String?
    var stateAfterRestore: String?
    var stateAfterRestoreResume: String?
    var steps: [SaveStateCheckStep]
    var ok: Bool
    var error: String?
    var errorDetail: ErrorDiagnostic?
    var startedAt: Date
    var completedAt: Date?

    enum CodingKeys: String, CodingKey {
        case mode
        case configShape = "config_shape"
        case saveStatePath = "save_state_path"
        case destinationExistedBefore = "destination_existed_before"
        case destinationParentExistsAfter = "destination_parent_exists_after"
        case destinationExistsAfter = "destination_exists_after"
        case initialVMState = "initial_vm_state"
        case stateAfterStart = "state_after_start"
        case stateBeforePause = "state_before_pause"
        case stateAfterPause = "state_after_pause"
        case stateBeforeSave = "state_before_save"
        case stateAfterSave = "state_after_save"
        case restoreCheck = "restore_check"
        case restoreInitialVMState = "restore_initial_vm_state"
        case stateAfterRestore = "state_after_restore"
        case stateAfterRestoreResume = "state_after_restore_resume"
        case steps
        case ok
        case error
        case errorDetail = "error_detail"
        case startedAt = "started_at"
        case completedAt = "completed_at"
    }
}

// Shared constants and the state-file codecs live in Globals.swift: top-level
// declarations in main.swift are locals of the implicit main function and are
// never initialized when the test bundle calls into this module.

struct RuntimeState: Codable {
    var event: Event
    var config: Config
    var pid: Int32?
    var serialLogPath: String
    var serialInputPath: String?
    var startedAt: Date?
    var updatedAt: Date
    var readiness: RuntimeReadiness?
    var error: String?
}

struct ApplyAck: Codable {
    var runtimeID: String
    var observedAt: String
    var error: String?
}

struct RuntimeControlRequest: Codable {
    var action: String
    var saveStatePath: String?
    var rootfsSnapshotPath: String?
}

struct RuntimeControlAck: Codable {
    var runtimeID: String
    var action: String
    var observedAt: String
    var error: String?
}

struct SecretControlRequest: Codable {
    var protocolVersion: String
    var op: String

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol_version"
        case op
    }
}

struct SecretControlResponse: Codable {
    var protocolVersion: String
    var ok: Bool
    var error: String?

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol_version"
        case ok
        case error
    }
}

func main() -> Int32 {
    do {
        if let code = runUtilityCommandIfPresent() {
            return code
        }
        let request = try readRequest()
        if request.command == "console" {
            try runConsole(request)
            return 0
        }
        if request.command == "deadman" {
            return runDeadman(request)
        }
        let response = try handle(request)
        write(response)
        return response.ok ? 0 : 1
    } catch {
        write(Response(ok: false, backend: backendName, error: String(describing: error)))
        return 1
    }
}

func runUtilityCommandIfPresent() -> Int32? {
    let args = Array(CommandLine.arguments.dropFirst())
    // --confinement-selfcheck applies the Seatbelt profile to this throwaway
    // process and exits 0 iff sandbox_init succeeds on this host. hostSupport()
    // spawns it so ConfinementActive only ever reports true when confinement is
    // verified to apply here (honesty invariant).
    if args == ["--confinement-selfcheck"] {
        return runConfinementSelfCheck()
    }
    if args.first == "--save-restore-config-check" {
        return runSaveRestoreConfigCheck(args: Array(args.dropFirst()))
    }
    if args.first == "--save-state-check" {
        return runSaveStateCheck(args: Array(args.dropFirst()))
    }
    return nil
}

func runSaveRestoreConfigCheck(args: [String]) -> Int32 {
    do {
        let request: Request
        if args.count == 2 && args[0] == "--request" {
            request = try decoder.decode(Request.self, from: Data(contentsOf: URL(fileURLWithPath: args[1])))
        } else if args.count == 2 && args[0] == "--request-json" {
            request = try decoder.decode(Request.self, from: Data(args[1].utf8))
        } else {
            throw ProtocolError.invalid("usage: --save-restore-config-check (--request <path>|--request-json <json>)")
        }
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        let runtimeConfig = try runtimeConfigForStart(identity: identity, config: config)
        #if canImport(Virtualization)
        guard hostSupport().virtualizationSupported else {
            throw ProtocolError.invalid("Apple Virtualization is not available on this host")
        }
        if #available(macOS 14.0, *) {
            try prepareHostFDEgressBeforeConfinement(config: runtimeConfig, identity: identity)
            defer { closeHostFDEgress() }
            let vmConfig = try virtualMachineConfiguration(identity: identity, config: runtimeConfig, serialMode: .detached)
            try vmConfig.validate()
            try vmConfig.validateSaveRestoreSupport()
            let event = Event(identity: identity, state: .prepared, detail: "save/restore supported", observedAt: Date())
            write(response(event: event, config: runtimeConfig, error: nil))
            return 0
        }
        throw ProtocolError.invalid("Apple VF save/restore requires macOS 14 or newer")
        #else
        throw ProtocolError.invalid("Virtualization.framework is not available in this build")
        #endif
    } catch {
        write(Response(ok: false, backend: backendName, error: String(describing: error)))
        return 1
    }
}

func runSaveStateCheck(args: [String]) -> Int32 {
    do {
        let parsed = try parseSaveStateCheckArgs(args)
        let identity = try validatedIdentity(parsed.request.identity)
        let config = try validatedConfig(parsed.request.config)
        let runtimeConfig = try runtimeConfigForStart(identity: identity, config: config)
        #if canImport(Virtualization)
        guard hostSupport().virtualizationSupported else {
            throw ProtocolError.invalid("Apple Virtualization is not available on this host")
        }
        if #available(macOS 14.0, *) {
            let mode = parsed.confined ? "confined" : "unconfined"
            let diagnostics = runSaveStateCheckVM(identity: identity, config: runtimeConfig, confined: parsed.confined, saveStatePath: parsed.saveStatePath, configShape: parsed.configShape, restoreCheck: parsed.restoreCheck)
            if diagnostics.ok {
                let event = Event(identity: identity, state: .paused, detail: "saveMachineStateTo succeeded (\(mode), \(parsed.configShape))", observedAt: Date())
                var out = response(event: event, config: runtimeConfig, error: nil)
                out.saveStateCheck = diagnostics
                write(out)
                return 0
            }
            write(Response(ok: false, backend: backendName, saveStateCheck: diagnostics, error: diagnostics.error ?? "saveMachineStateTo failed"))
            return 1
        }
        throw ProtocolError.invalid("Apple VF save state requires macOS 14 or newer")
        #else
        throw ProtocolError.invalid("Virtualization.framework is not available in this build")
        #endif
    } catch {
        write(Response(ok: false, backend: backendName, error: String(describing: error)))
        return 1
    }
}

struct SaveStateCheckArgs {
    var request: Request
    var confined: Bool
    var saveStatePath: String?
    var configShape: String
    var restoreCheck: Bool
}

func parseSaveStateCheckArgs(_ args: [String]) throws -> SaveStateCheckArgs {
    var request: Request?
    var confined = true
    var saveStatePath: String?
    var configShape = "full"
    var restoreCheck = false
    var i = 0
    while i < args.count {
        switch args[i] {
        case "--request":
            guard i + 1 < args.count else { throw ProtocolError.invalid("--request requires a path") }
            request = try decoder.decode(Request.self, from: Data(contentsOf: URL(fileURLWithPath: args[i + 1])))
            i += 2
        case "--request-json":
            guard i + 1 < args.count else { throw ProtocolError.invalid("--request-json requires JSON") }
            request = try decoder.decode(Request.self, from: Data(args[i + 1].utf8))
            i += 2
        case "--mode":
            guard i + 1 < args.count else { throw ProtocolError.invalid("--mode requires confined or unconfined") }
            switch args[i + 1] {
            case "confined":
                confined = true
            case "unconfined":
                confined = false
            default:
                throw ProtocolError.invalid("--mode requires confined or unconfined")
            }
            i += 2
        case "--save-state-path":
            guard i + 1 < args.count else { throw ProtocolError.invalid("--save-state-path requires a path") }
            saveStatePath = args[i + 1]
            i += 2
        case "--config-shape":
            guard i + 1 < args.count else { throw ProtocolError.invalid("--config-shape requires full, minimal, no-network, no-sockets, no-serial, nat-network, or full-same-vm") }
            switch args[i + 1] {
            case "full", "minimal", "no-network", "no-sockets", "no-serial", "nat-network", "full-same-vm":
                configShape = args[i + 1]
            default:
                throw ProtocolError.invalid("--config-shape requires full, minimal, no-network, no-sockets, no-serial, nat-network, or full-same-vm")
            }
            i += 2
        case "--restore-check":
            restoreCheck = true
            i += 1
        default:
            throw ProtocolError.invalid("usage: --save-state-check (--request <path>|--request-json <json>) [--mode confined|unconfined] [--save-state-path <path>] [--config-shape full|minimal|no-network|no-sockets|no-serial|nat-network|full-same-vm] [--restore-check]")
        }
    }
    guard let request else {
        throw ProtocolError.invalid("usage: --save-state-check (--request <path>|--request-json <json>) [--mode confined|unconfined] [--save-state-path <path>] [--config-shape full|minimal|no-network|no-sockets|no-serial|nat-network|full-same-vm] [--restore-check]")
    }
    return SaveStateCheckArgs(request: request, confined: confined, saveStatePath: saveStatePath, configShape: configShape, restoreCheck: restoreCheck)
}

#if canImport(Virtualization)
@available(macOS 14.0, *)
func runSaveStateCheckVM(identity: Identity, config: Config, confined: Bool, saveStatePath: String?, configShape: String, restoreCheck: Bool) -> SaveStateCheckDiagnostics {
    let path = saveStatePath ?? runtimeDirectory(identity: identity, stateDir: config.stateDir).appendingPathComponent("save-state-check.vmstate").path
    let mode = confined ? "confined" : "unconfined"
    var diagnostics = SaveStateCheckDiagnostics(
        mode: mode,
        configShape: configShape,
        saveStatePath: path,
        destinationExistedBefore: FileManager.default.fileExists(atPath: path),
        destinationParentExistsAfter: nil,
        destinationExistsAfter: nil,
        initialVMState: nil,
        stateAfterStart: nil,
        stateBeforePause: nil,
        stateAfterPause: nil,
        stateBeforeSave: nil,
        stateAfterSave: nil,
        restoreCheck: restoreCheck,
        restoreInitialVMState: nil,
        stateAfterRestore: nil,
        stateAfterRestoreResume: nil,
        steps: [],
        ok: false,
        error: nil,
        startedAt: Date(),
        completedAt: nil
    )
    do {
        if saveStateCheckNeedsHostFDEgress(configShape) {
            try recordSaveStateStep(&diagnostics, name: "prepare-host-fd-egress") {
                try prepareHostFDEgressBeforeConfinement(config: config, identity: identity)
            }
        }
        defer {
            if saveStateCheckNeedsHostFDEgress(configShape) {
                closeHostFDEgress()
            }
        }
        if confined {
            try recordSaveStateStep(&diagnostics, name: "apply-confinement") {
                try applyConfinement(identity: identity, config: config, qos: QOS_CLASS_UTILITY)
            }
        }
        let vmConfig = try saveStateCheckConfiguration(identity: identity, config: config, configShape: configShape)
        try recordSaveStateStep(&diagnostics, name: "validate") {
            try vmConfig.validate()
        }
        try recordSaveStateStep(&diagnostics, name: "validate-save-restore-support") {
            try vmConfig.validateSaveRestoreSupport()
        }
        let vm = VZVirtualMachine(configuration: vmConfig)
        diagnostics.initialVMState = describeVMState(vm)
        let delegate = VMRunDelegate(identity: identity, config: config)
        vm.delegate = delegate
        let socketListeners: [SocketListenerHandle]
        let publishForwarder: TCPPublishForwarder?
        if !saveStateCheckUsesSocketDevices(configShape) {
            socketListeners = []
        } else {
            socketListeners = try installSocketListeners(vm: vm, identity: identity, config: config)
        }
        if !saveStateCheckUsesNetworkDevices(configShape) || !saveStateCheckUsesSocketDevices(configShape) {
            publishForwarder = nil
        } else {
            publishForwarder = try installTCPPublishForwarder(vm: vm, config: config)
        }
        try withExtendedLifetime((delegate, socketListeners, publishForwarder)) {
            try startPauseAndSave(vm: vm, saveStatePath: path, diagnostics: &diagnostics)
        }
        if restoreCheck {
            try recordSaveStateStep(&diagnostics, name: "stop-source-vm") {
                try waitForVZOptionalError(timeout: 10.0) { complete in
                    vm.stop { complete($0) }
                }
            }
            if saveStateCheckRestoreUsesSameVM(configShape) {
                diagnostics.restoreInitialVMState = describeVMState(vm)
                try recordSaveStateStep(&diagnostics, name: "restore-machine-state") {
                    try waitForVZOptionalError(timeout: 60.0) { complete in
                        vm.restoreMachineStateFrom(url: URL(fileURLWithPath: path)) { complete($0) }
                    }
                }
                diagnostics.stateAfterRestore = describeVMState(vm)
                try recordSaveStateStep(&diagnostics, name: "restore-resume") {
                    try waitForVZResult(timeout: 10.0) { complete in
                        vm.resume { complete($0) }
                    }
                }
                diagnostics.stateAfterRestoreResume = describeVMState(vm)
            } else {
                let restoreConfig = try saveStateCheckConfiguration(identity: identity, config: config, configShape: configShape)
                try recordSaveStateStep(&diagnostics, name: "restore-validate") {
                    try restoreConfig.validate()
                }
                try recordSaveStateStep(&diagnostics, name: "restore-validate-save-restore-support") {
                    try restoreConfig.validateSaveRestoreSupport()
                }
                let restoreVM = VZVirtualMachine(configuration: restoreConfig)
                diagnostics.restoreInitialVMState = describeVMState(restoreVM)
                let restoreDelegate = VMRunDelegate(identity: identity, config: config)
                restoreVM.delegate = restoreDelegate
                try withExtendedLifetime(restoreDelegate) {
                    try recordSaveStateStep(&diagnostics, name: "restore-machine-state") {
                        try waitForVZOptionalError(timeout: 60.0) { complete in
                            restoreVM.restoreMachineStateFrom(url: URL(fileURLWithPath: path)) { complete($0) }
                        }
                    }
                    diagnostics.stateAfterRestore = describeVMState(restoreVM)
                    try recordSaveStateStep(&diagnostics, name: "restore-resume") {
                        try waitForVZResult(timeout: 10.0) { complete in
                            restoreVM.resume { complete($0) }
                        }
                    }
                    diagnostics.stateAfterRestoreResume = describeVMState(restoreVM)
                }
            }
        }
        diagnostics.ok = true
    } catch {
        diagnostics.error = String(describing: error)
        diagnostics.errorDetail = errorDiagnostic(error)
    }
    diagnostics.destinationParentExistsAfter = FileManager.default.fileExists(atPath: URL(fileURLWithPath: path).deletingLastPathComponent().path)
    diagnostics.destinationExistsAfter = FileManager.default.fileExists(atPath: path)
    diagnostics.completedAt = Date()
    return diagnostics
}

@available(macOS 14.0, *)
func minimalSaveStateConfiguration(config: Config) throws -> VZVirtualMachineConfiguration {
    let vmConfig = VZVirtualMachineConfiguration()
    let platform = VZGenericPlatformConfiguration()
    platform.machineIdentifier = try genericMachineIdentifier(from: config)
    vmConfig.platform = platform
    let bootLoader = VZLinuxBootLoader(kernelURL: URL(fileURLWithPath: config.kernelPath))
    bootLoader.commandLine = "console=hvc0 root=/dev/vda rw init=/sbin/microagent-init"
    vmConfig.bootLoader = bootLoader
    vmConfig.cpuCount = config.cpuCount ?? 2
    vmConfig.memorySize = UInt64(config.memoryMiB ?? 512) * 1024 * 1024
    let attachment = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: config.rootfsPath), readOnly: false)
    vmConfig.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: attachment)]
    vmConfig.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
    vmConfig.networkDevices = []
    vmConfig.socketDevices = []
    vmConfig.serialPorts = []
    return vmConfig
}

@available(macOS 14.0, *)
func saveStateCheckConfiguration(identity: Identity, config: Config, configShape: String) throws -> VZVirtualMachineConfiguration {
    if configShape == "minimal" {
        return try minimalSaveStateConfiguration(config: config)
    }
    var buildConfig = config
    if configShape == "nat-network" {
        buildConfig.egressMode = "off"
    }
    let vmConfig = try virtualMachineConfiguration(identity: identity, config: buildConfig, serialMode: .detached)
    switch configShape {
    case "full", "full-same-vm":
        break
    case "no-network":
        vmConfig.networkDevices = []
    case "nat-network":
        let device = VZVirtioNetworkDeviceConfiguration()
        device.attachment = VZNATNetworkDeviceAttachment()
        vmConfig.networkDevices = [device]
    case "no-sockets":
        vmConfig.socketDevices = []
    case "no-serial":
        vmConfig.serialPorts = []
    default:
        throw ProtocolError.invalid("unsupported save-state config shape \(configShape)")
    }
    return vmConfig
}

func saveStateCheckNeedsHostFDEgress(_ configShape: String) -> Bool {
    configShape == "full" || configShape == "full-same-vm" || configShape == "no-network" || configShape == "no-sockets" || configShape == "no-serial"
}

func saveStateCheckUsesNetworkDevices(_ configShape: String) -> Bool {
    configShape != "minimal" && configShape != "no-network"
}

func saveStateCheckUsesSocketDevices(_ configShape: String) -> Bool {
    configShape != "minimal" && configShape != "no-sockets"
}

func saveStateCheckRestoreUsesSameVM(_ configShape: String) -> Bool {
    configShape == "full-same-vm"
}

@available(macOS 14.0, *)
func startPauseAndSave(vm: VZVirtualMachine, saveStatePath: String, diagnostics: inout SaveStateCheckDiagnostics) throws {
    try recordSaveStateStep(&diagnostics, name: "start") {
        try waitForVZResult(timeout: 30.0) { complete in
            vm.start { complete($0) }
        }
    }
    diagnostics.stateAfterStart = describeVMState(vm)
    Thread.sleep(forTimeInterval: 2.0)
    diagnostics.stateBeforePause = describeVMState(vm)
    try recordSaveStateStep(&diagnostics, name: "pause") {
        try pauseVMForSave(vm: vm)
    }
    diagnostics.stateAfterPause = describeVMState(vm)
    let url = URL(fileURLWithPath: saveStatePath)
    if FileManager.default.fileExists(atPath: url.path) {
        try recordSaveStateStep(&diagnostics, name: "remove-existing-destination") {
            try FileManager.default.removeItem(at: url)
        }
    }
    try recordSaveStateStep(&diagnostics, name: "prepare-destination-parent") {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    }
    diagnostics.stateBeforeSave = describeVMState(vm)
    try recordSaveStateStep(&diagnostics, name: "save-machine-state") {
        try waitForVZOptionalError(timeout: 30.0) { complete in
            vm.saveMachineStateTo(url: url) { complete($0) }
        }
    }
    diagnostics.stateAfterSave = describeVMState(vm)
}

@available(macOS 14.0, *)
func pauseVMForSave(vm: VZVirtualMachine) throws {
    do {
        try waitForVZResult(timeout: 10.0) { complete in
            vm.pause { complete($0) }
        }
    } catch {
        let deadline = Date().addingTimeInterval(3.0)
        while Date() < deadline {
            if vm.state == .paused {
                return
            }
            Thread.sleep(forTimeInterval: 0.05)
        }
        throw error
    }
}

@available(macOS 13.0, *)
func describeVMState(_ vm: VZVirtualMachine) -> String {
    String(describing: vm.state)
}

func recordSaveStateStep(_ diagnostics: inout SaveStateCheckDiagnostics, name: String, operation: () throws -> Void) throws {
    do {
        try operation()
        diagnostics.steps.append(SaveStateCheckStep(name: name, ok: true, detail: nil, error: nil, errorDetail: nil, observedAt: Date()))
    } catch {
        diagnostics.steps.append(SaveStateCheckStep(name: name, ok: false, detail: nil, error: String(describing: error), errorDetail: errorDiagnostic(error), observedAt: Date()))
        throw error
    }
}

func errorDiagnostic(_ error: Error, seen: Set<ObjectIdentifier> = []) -> ErrorDiagnostic {
    let nsError = error as NSError
    let objectID = ObjectIdentifier(nsError)
    let nextSeen = seen.union([objectID])
    let entries = nsError.userInfo
        .filter { key, _ in key != NSUnderlyingErrorKey }
        .map { key, value in ErrorUserInfoEntry(key: key, value: String(describing: value)) }
        .sorted { $0.key < $1.key }
    let underlying: ErrorDiagnostic?
    if let nested = nsError.userInfo[NSUnderlyingErrorKey] as? Error, !seen.contains(objectID) {
        underlying = errorDiagnostic(nested, seen: nextSeen)
    } else {
        underlying = nil
    }
    return ErrorDiagnostic(
        type: String(reflecting: Swift.type(of: error)),
        description: String(describing: error),
        localizedDescription: nsError.localizedDescription,
        localizedFailureReason: nsError.localizedFailureReason,
        localizedRecoverySuggestion: nsError.localizedRecoverySuggestion,
        domain: nsError.domain,
        code: nsError.code,
        userInfo: entries.isEmpty ? nil : entries,
        underlying: underlying
    )
}

func waitForVZResult(timeout: TimeInterval, operation: (@escaping (Result<Void, Error>) -> Void) -> Void) throws {
    var result: Result<Void, Error>?
    operation { completed in
        result = completed
    }
    let deadline = Date().addingTimeInterval(timeout)
    while result == nil && Date() < deadline {
        RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
    }
    guard let result else {
        throw ProtocolError.invalid("Virtualization operation timed out after \(timeout)s")
    }
    try result.get()
}

func waitForVZOptionalError(timeout: TimeInterval, operation: (@escaping (Error?) -> Void) -> Void) throws {
    var completed = false
    var failure: Error?
    operation { error in
        completed = true
        failure = error
    }
    let deadline = Date().addingTimeInterval(timeout)
    while !completed && Date() < deadline {
        RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
    }
    guard completed else {
        throw ProtocolError.invalid("Virtualization operation timed out after \(timeout)s")
    }
    if let failure {
        throw failure
    }
}
#endif

func readRequest() throws -> Request {
    let args = Array(CommandLine.arguments.dropFirst())
    if args.count == 2 && args[0] == "--request" {
        return try decoder.decode(Request.self, from: Data(contentsOf: URL(fileURLWithPath: args[1])))
    }
    if args.count == 2 && args[0] == "--request-json" {
        return try decoder.decode(Request.self, from: Data(args[1].utf8))
    }
    let data = FileHandle.standardInput.readDataToEndOfFile()
    if data.isEmpty {
        throw ProtocolError.invalid("request JSON is required on stdin or with --request")
    }
    return try decoder.decode(Request.self, from: data)
}

func handle(_ request: Request) throws -> Response {
    switch request.command {
    case "run":
        try runVM(request)
        return Response(ok: true, backend: backendName)
    case "host":
        return Response(ok: true, backend: backendName, host: hostSupport())
    case "check":
        let identity = try validatedIdentity(request.identity)
        let _ = try validatedConfig(request.config)
        return Response(ok: true, backend: backendName, event: Event(identity: identity, state: .prepared, detail: "validated", observedAt: Date()))
    case "prepare":
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        let event = Event(identity: identity, state: .prepared, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: nil, error: nil)
        return response(event: event, config: config, error: nil)
    case "start":
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        try ensureCanStart(identity: identity, stateDir: config.stateDir)
        let support = hostSupport()
        guard support.frameworkAvailable && support.virtualizationSupported else {
            return Response(ok: false, backend: backendName, error: "Apple Virtualization is not available on this host")
        }
        let runtimeConfig = try runtimeConfigForStart(identity: identity, config: config)
        #if canImport(Virtualization)
        if #available(macOS 13.0, *) {
            let vmConfig = try virtualMachineConfiguration(identity: identity, config: runtimeConfig, serialMode: .detached)
            try vmConfig.validate()
        }
        #endif
        let event = Event(identity: identity, state: .starting, detail: "serial=\(serialLogPath(identity: identity, stateDir: runtimeConfig.stateDir).path)", observedAt: Date())
        try writeState(event: event, config: runtimeConfig)
        var runRequest = request.withCommand("run")
        runRequest.config = runtimeConfig
        let process = Process()
        process.executableURL = URL(fileURLWithPath: currentExecutablePath())
        process.arguments = ["--request-json", try requestJSON(runRequest)]
        process.standardInput = FileHandle.nullDevice
        FileManager.default.createFile(atPath: supervisorLogPath(identity: identity, stateDir: runtimeConfig.stateDir).path, contents: nil)
        let supervisorLog = try FileHandle(forWritingTo: supervisorLogPath(identity: identity, stateDir: runtimeConfig.stateDir))
        process.standardOutput = supervisorLog
        process.standardError = supervisorLog
        try process.run()
        try writeRuntimeState(event: event, config: runtimeConfig, pid: process.processIdentifier, error: nil)
        let responseConfig = try readRuntimeState(identity: identity, stateDir: runtimeConfig.stateDir)?.config ?? runtimeConfig
        return response(event: event, config: responseConfig, error: nil)
    case "inspect":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        var event = try readEvent(identity: identity, stateDir: config.stateDir) ?? Event(identity: identity, state: .unknown, detail: nil, observedAt: Date())
        if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), !processAlive(runtime.pid), event.state == .starting || event.state == .running || event.state == .paused {
            event = Event(identity: event.identity, state: .stopped, detail: event.detail, observedAt: Date())
            try writeState(event: event, config: runtime.config)
            try writeRuntimeState(event: event, config: runtime.config, pid: nil, error: runtime.error)
        }
        let runtimeConfig = try readRuntimeState(identity: identity, stateDir: config.stateDir)?.config ?? config
        return response(event: event, config: runtimeConfig, error: nil)
    case "gc":
        return try gcWorkspace(request)
    case "stop":
        return try stateOnly(request, state: .stopped, detail: nil)
    case "halt":
        return try stateOnly(request, state: .halted, detail: nil)
    case "quarantine":
        return try quarantine(request)
    case "pause":
        return try pauseLive(request)
    case "resume":
        return try resumeLive(request)
    case "snapshot":
        return try snapshotLive(request)
    case "apply":
        return try applyLive(request)
    case "kill":
        return try stateOnly(request, state: .stopped, detail: "forced")
    case "delete":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        try ensureCanDelete(identity: identity, stateDir: config.stateDir)
        let dir = runtimeDirectory(identity: identity, stateDir: config.stateDir)
        if FileManager.default.fileExists(atPath: dir.path) {
            try FileManager.default.removeItem(at: dir)
        }
        return Response(ok: true, backend: backendName, event: Event(identity: identity, state: .stopped, detail: "deleted", observedAt: Date()))
    default:
        throw ProtocolError.invalid("unknown command: \(request.command)")
    }
}

func pauseLive(_ request: Request) throws -> Response {
    return try runtimeControl(request, action: "pause", requiredState: .running, nextState: .paused, detail: "apple-vf virtual machine paused")
}

func resumeLive(_ request: Request) throws -> Response {
    return try runtimeControl(request, action: "resume", requiredState: .paused, nextState: .running, detail: "apple-vf virtual machine resumed")
}

func snapshotLive(_ request: Request) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    let tag = try validatedSnapshotTag(request.tag)
    guard let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir) else {
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is not running")
    }
    guard runtime.event.state == .running || runtime.event.state == .paused else {
        throw ProtocolError.invalid("snapshot requires state running or paused, got \(runtime.event.state.rawValue)")
    }
    guard processAlive(runtime.pid), let pid = runtime.pid else {
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is not running")
    }
    let dir = snapshotDirectory(identity: identity, stateDir: runtime.config.stateDir, tag: tag)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    let saveStatePath = snapshotMachineStatePath(identity: identity, stateDir: runtime.config.stateDir, tag: tag)
    let rootfsSnapshotPath = snapshotRootfsPath(identity: identity, stateDir: runtime.config.stateDir, tag: tag)
    let requestPath = runtimeControlRequestPath(identity: identity, stateDir: runtime.config.stateDir)
    let ackPath = runtimeControlAckPath(identity: identity, stateDir: runtime.config.stateDir)
    try? FileManager.default.removeItem(at: ackPath)
    try encoder.encode(RuntimeControlRequest(action: "snapshot", saveStatePath: saveStatePath.path, rootfsSnapshotPath: rootfsSnapshotPath.path)).write(to: requestPath, options: .atomic)
    if kill(pid, runtimeControlSignal) != 0 && errno != ESRCH {
        throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
    }
    let ack = try waitForRuntimeControlAck(path: ackPath, action: "snapshot", timeout: 60.0)
    if let error = ack.error, !error.isEmpty {
        throw ProtocolError.invalid(error)
    }
    let event = Event(identity: identity, state: runtime.event.state, detail: "snapshot \(tag) captured", observedAt: Date())
    try writeState(event: event, config: runtime.config)
    try writeRuntimeState(event: event, config: runtime.config, pid: pid, error: nil)
    return response(event: event, config: runtime.config, error: nil)
}

func validatedSnapshotTag(_ tag: String?) throws -> String {
    let value = (tag ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    if !isSafeIdentifier(value) {
        throw ProtocolError.invalid("snapshot tag must be a safe basename")
    }
    return value
}

func validatedOptionalSnapshotTag(_ tag: String?) throws -> String? {
    let value = (tag ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    if value.isEmpty {
        return nil
    }
    if !isSafeIdentifier(value) {
        throw ProtocolError.invalid("snapshot tag must be a safe basename")
    }
    return value
}

func runtimeControl(_ request: Request, action: String, requiredState: VMState, nextState: VMState, detail: String) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    guard let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir) else {
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is not running")
    }
    guard runtime.event.state == requiredState else {
        throw ProtocolError.invalid("\(action) requires state \(requiredState.rawValue), got \(runtime.event.state.rawValue)")
    }
    guard processAlive(runtime.pid), let pid = runtime.pid else {
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is not running")
    }
    let requestPath = runtimeControlRequestPath(identity: identity, stateDir: runtime.config.stateDir)
    let ackPath = runtimeControlAckPath(identity: identity, stateDir: runtime.config.stateDir)
    try? FileManager.default.removeItem(at: ackPath)
    try encoder.encode(RuntimeControlRequest(action: action)).write(to: requestPath, options: .atomic)
    if kill(pid, runtimeControlSignal) != 0 && errno != ESRCH {
        throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
    }
    let ack = try waitForRuntimeControlAck(path: ackPath, action: action, timeout: 5.0)
    if let error = ack.error, !error.isEmpty {
        throw ProtocolError.invalid(error)
    }
    let event = Event(identity: identity, state: nextState, detail: detail, observedAt: Date())
    try writeState(event: event, config: runtime.config)
    try writeRuntimeState(event: event, config: runtime.config, pid: pid, error: nil)
    return response(event: event, config: runtime.config, error: nil)
}

func applyLive(_ request: Request) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try validatedConfig(request.config)
    guard let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), processAlive(runtime.pid), let pid = runtime.pid else {
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is not running")
    }
    guard runtime.event.state == .running else {
        throw ProtocolError.invalid("apply requires state running, got \(runtime.event.state.rawValue)")
    }
    guard livePortForwardHostOnlyChange(oldConfig: runtime.config, newConfig: config) else {
        throw ProtocolError.invalid("live network apply only supports host bind changes for existing port forwards; stop and start \(identity.runtimeID) to apply this change")
    }
    let requestPath = applyRequestPath(identity: identity, stateDir: runtime.config.stateDir)
    let ackPath = applyAckPath(identity: identity, stateDir: runtime.config.stateDir)
    try? FileManager.default.removeItem(at: ackPath)
    try encoder.encode(config).write(to: requestPath, options: .atomic)
    if kill(pid, applyControlSignal) != 0 && errno != ESRCH {
        throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
    }
    let ack = try waitForApplyAck(path: ackPath, timeout: 2.0)
    if let error = ack.error, !error.isEmpty {
        throw ProtocolError.invalid(error)
    }
    let event = Event(identity: identity, state: .running, detail: "network applied", observedAt: Date())
    try writeState(event: event, config: config)
    try writeRuntimeState(event: event, config: config, pid: pid, error: nil)
    return response(event: event, config: config, error: nil)
}

func quarantine(_ request: Request) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    let detail = "host-side network, mediation, and serial input severed"
    let event = Event(identity: identity, state: .quarantined, detail: detail, observedAt: Date())
    if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), processAlive(runtime.pid), let pid = runtime.pid {
        let ack = quarantineAckPath(identity: identity, stateDir: runtime.config.stateDir)
        try? FileManager.default.removeItem(at: ack)
        if kill(pid, quarantineControlSignal) != 0 && errno != ESRCH {
            throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
        }
        try waitForQuarantineAck(path: ack, timeout: 2.0)
        try writeState(event: event, config: runtime.config)
        try writeRuntimeState(event: event, config: runtime.config, pid: pid, error: nil)
        return response(event: event, config: runtime.config, error: nil)
    }
    try writeState(event: event, config: config)
    try writeRuntimeState(event: event, config: config, pid: nil, error: nil)
    return response(event: event, config: config, error: nil)
}

func waitForApplyAck(path: URL, timeout: TimeInterval) throws -> ApplyAck {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if FileManager.default.fileExists(atPath: path.path) {
            return try decoder.decode(ApplyAck.self, from: Data(contentsOf: path))
        }
        usleep(20_000)
    }
    throw ProtocolError.invalid("apple-vf apply control did not acknowledge before timeout")
}

func waitForQuarantineAck(path: URL, timeout: TimeInterval) throws {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if FileManager.default.fileExists(atPath: path.path) {
            return
        }
        usleep(20_000)
    }
    throw ProtocolError.invalid("apple-vf quarantine control did not acknowledge before timeout")
}

func waitForRuntimeControlAck(path: URL, action: String, timeout: TimeInterval) throws -> RuntimeControlAck {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if FileManager.default.fileExists(atPath: path.path) {
            let ack = try decoder.decode(RuntimeControlAck.self, from: Data(contentsOf: path))
            if ack.action == action {
                return ack
            }
        }
        usleep(20_000)
    }
    throw ProtocolError.invalid("apple-vf \(action) control did not acknowledge before timeout")
}

func gcWorkspace(_ request: Request) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    guard let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir) else {
        let event = try readEvent(identity: identity, stateDir: config.stateDir) ?? Event(identity: identity, state: .unknown, detail: nil, observedAt: Date())
        return response(event: event, config: config, error: nil)
    }
    guard runtime.event.state == .running || runtime.event.state == .starting || runtime.event.state == .paused else {
        return response(event: runtime.event, config: runtime.config, error: nil)
    }
    let alive = processAlive(runtime.pid)
    let expired = leaseExpired(runtime)
    if alive && !expired {
        return response(event: runtime.event, config: runtime.config, error: nil)
    }
    let reason = alive && expired ? "reaped by gc: lifetime lease expired" : "reaped by gc: process gone"
    if alive, let pid = runtime.pid {
        if kill(pid, SIGKILL) != 0 && errno != ESRCH {
            fputs("gc signal \(pid) failed with errno \(errno)\n", stderr)
        }
        _ = waitForProcessExit(pid: pid, timeout: 2.0)
    }
    let event = Event(identity: identity, state: .stopped, detail: runtime.event.detail, observedAt: Date())
    try writeState(event: event, config: runtime.config)
    try writeRuntimeState(event: event, config: runtime.config, pid: nil, error: reason)
    return response(event: event, config: runtime.config, error: nil)
}

func runDeadman(_ request: Request) -> Int32 {
    guard let identity = request.identity, let stateDir = request.config?.stateDir else {
        return 1
    }
    while true {
        guard let runtime = try? readRuntimeState(identity: identity, stateDir: stateDir) else {
            return 0
        }
        guard let lease = runtime.config.leaseSeconds, lease > 0 else {
            return 0
        }
        guard runtime.event.state == .running || runtime.event.state == .starting || runtime.event.state == .paused else {
            return 0
        }
        do {
            _ = try gcWorkspace(request.withCommand("gc"))
        } catch {
            fputs("deadman reconcile \(identity.runtimeID): \(error)\n", stderr)
        }
        Thread.sleep(forTimeInterval: deadmanPollInterval(leaseSeconds: lease))
    }
}

func deadmanPollInterval(leaseSeconds: Int) -> TimeInterval {
    return max(1.0, min(60.0, Double(leaseSeconds) / 4.0))
}

func startDeadmanProcessIfNeeded(request: Request, identity: Identity, config: Config) {
    guard (config.leaseSeconds ?? 0) > 0,
          let payload = try? requestJSON(request.withCommand("deadman")) else {
        return
    }
    let process = Process()
    process.executableURL = URL(fileURLWithPath: currentExecutablePath())
    process.arguments = ["--request-json", payload]
    process.standardInput = FileHandle.nullDevice
    if let log = deadmanLogFile(identity: identity, stateDir: config.stateDir) {
        process.standardOutput = log
        process.standardError = log
    } else {
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
    }
    do {
        try process.run()
    } catch {
        fputs("start deadman watcher \(identity.runtimeID): \(error)\n", stderr)
    }
}

func deadmanLogFile(identity: Identity, stateDir: String) -> FileHandle? {
    let path = runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("deadman.log")
    FileManager.default.createFile(atPath: path.path, contents: nil)
    guard let handle = try? FileHandle(forWritingTo: path) else {
        return nil
    }
    _ = try? handle.seekToEnd()
    return handle
}

func leaseExpired(_ runtime: RuntimeState) -> Bool {
    guard let lease = runtime.config.leaseSeconds, lease > 0 else {
        return false
    }
    var base = runtime.startedAt ?? Date.distantPast
    if let activity = lastActivity(identity: runtime.event.identity, stateDir: runtime.config.stateDir), activity > base {
        base = activity
    }
    if base == Date.distantPast {
        return false
    }
    return Date() > base.addingTimeInterval(TimeInterval(lease))
}

func lastActivity(identity: Identity, stateDir: String) -> Date? {
    let path = runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("activity")
    guard let attrs = try? FileManager.default.attributesOfItem(atPath: path.path),
          let modified = attrs[.modificationDate] as? Date else {
        return nil
    }
    return modified
}

func stateOnly(_ request: Request, state: VMState, detail: String?) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    var eventDetail = detail
    if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), processAlive(runtime.pid), let pid = runtime.pid {
        let signal = detail == "forced" ? SIGKILL : SIGTERM
        if kill(pid, signal) != 0 && errno != ESRCH {
            throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
        }
        if !waitForProcessExit(pid: pid, timeout: signal == SIGKILL ? 30.0 : 15.0) {
            if signal == SIGKILL {
                eventDetail = "forced; process exit not observed before timeout"
            } else {
                if kill(pid, SIGKILL) != 0 && errno != ESRCH {
                    throw ProtocolError.invalid("signal \(pid) fallback failed with errno \(errno)")
                }
                if !waitForProcessExit(pid: pid, timeout: 30.0) {
                    eventDetail = "forced after signal \(signal) timeout; process exit not observed before timeout"
                } else {
                    eventDetail = "forced after signal \(signal) timeout"
                }
            }
        }
        let event = Event(identity: identity, state: state, detail: eventDetail, observedAt: Date())
        try writeState(event: event, config: runtime.config)
        try writeRuntimeState(event: event, config: runtime.config, pid: nil, error: nil)
        return response(event: event, config: runtime.config, error: nil)
    } else {
        let event = Event(identity: identity, state: state, detail: eventDetail, observedAt: Date())
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: nil, error: nil)
        return response(event: event, config: config, error: nil)
    }
}

func validatedIdentity(_ identity: Identity?) throws -> Identity {
    guard let identity else {
        throw ProtocolError.invalid("identity is required")
    }
    if identity.requestID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("identity.requestID is required")
    }
    if identity.runtimeID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("identity.runtimeID is required")
    }
    if !isSafeIdentifier(identity.runtimeID) {
        throw ProtocolError.invalid("identity.runtimeID must be a safe basename")
    }
    if identity.backend != backendName {
        throw ProtocolError.invalid("identity.backend must be \(backendName)")
    }
    return identity
}

func validatedConfig(_ config: Config?) throws -> Config {
    var config = try stateConfig(config)
    if config.kernelPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("config.kernelPath is required")
    }
    if config.rootfsPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("config.rootfsPath is required")
    }
    if config.memoryMiB == nil {
        config.memoryMiB = 512
    }
    if config.cpuCount == nil {
        config.cpuCount = 2
    }
    if config.memoryMiB ?? 0 <= 0 {
        throw ProtocolError.invalid("config.memoryMiB must be positive")
    }
    if config.cpuCount ?? 0 <= 0 {
        throw ProtocolError.invalid("config.cpuCount must be positive")
    }
    var diskNames = Set<String>()
    var diskMountpoints = Set<String>()
    for disk in config.disks ?? [] {
        if disk.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw ProtocolError.invalid("disk name is required")
        }
        if disk.name == "rootfs" {
            throw ProtocolError.invalid("disk name rootfs is reserved")
        }
        if !diskNames.insert(disk.name).inserted {
            throw ProtocolError.invalid("duplicate disk name \(disk.name)")
        }
        if disk.path.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw ProtocolError.invalid("disk \(disk.name) path is required")
        }
        if disk.mountpoint.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !disk.mountpoint.hasPrefix("/") {
            throw ProtocolError.invalid("disk \(disk.name) mountpoint must be absolute")
        }
        if !diskMountpoints.insert(disk.mountpoint).inserted {
            throw ProtocolError.invalid("duplicate disk mountpoint \(disk.mountpoint)")
        }
        if disk.mode != "ro" && disk.mode != "rw" {
            throw ProtocolError.invalid("disk \(disk.name) mode must be ro or rw")
        }
        try readableFile(disk.path, name: "disk \(disk.name) path")
    }
    var ports = Set<UInt32>()
    for listener in config.vsockListeners ?? [] {
        if listener.port == 0 {
            throw ProtocolError.invalid("vsock listener port must be positive")
        }
        if !ports.insert(listener.port).inserted {
            throw ProtocolError.invalid("duplicate vsock listener port \(listener.port)")
        }
        if listener.target.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw ProtocolError.invalid("vsock listener \(listener.port) target is required")
        }
    }
    try validateNetworkConfig(config.network)
    try validateMediationConfig(config.mediation)
    try readableFile(config.kernelPath, name: "config.kernelPath")
    try readableFile(config.rootfsPath, name: "config.rootfsPath")
    return config
}

func validateMediationConfig(_ mediation: MediationConfig?) throws {
    guard let mediation, mediation.enabled else {
        return
    }
    if mediation.required && !mediation.failClosed {
        throw ProtocolError.invalid("required mediation must set failClosed=true")
    }
    if mediation.port == nil || mediation.port == 0 {
        throw ProtocolError.invalid("mediation.port is required when mediation is enabled")
    }
    if (mediation.target ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("mediation.target is required when mediation is enabled")
    }
    _ = try parseTCPHostPort(mediation.target ?? "")
}

func validateNetworkConfig(_ network: NetworkConfig?) throws {
    guard let network else {
        return
    }
    let mode = normalizedNetworkMode(network)
    switch mode {
    case "user", "isolated":
        break
    default:
        throw ProtocolError.invalid("network.mode must be user or isolated")
    }
    let ip = network.ip?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    let gateway = network.gateway?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    let subnet = network.subnet?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    if !ip.isEmpty || !gateway.isEmpty || !subnet.isEmpty {
        if ip.isEmpty || gateway.isEmpty {
            throw ProtocolError.invalid("Apple VF static networking requires network.ip and network.gateway")
        }
    }
}

func normalizedNetworkMode(_ network: NetworkConfig?) -> String {
    let mode = network?.mode.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return mode.isEmpty ? "user" : mode
}

func stateConfig(_ config: Config?) throws -> Config {
    guard let config else {
        throw ProtocolError.invalid("config is required")
    }
    if config.stateDir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("config.stateDir is required")
    }
    return config
}

func ensureCanStart(identity: Identity, stateDir: String) throws {
    guard let runtime = try readRuntimeState(identity: identity, stateDir: stateDir) else {
        return
    }
    switch runtime.event.state {
    case .unknown, .prepared, .halted, .stopped, .failed:
        return
    case .quarantined:
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is quarantined; halt, stop, or kill it before start")
    case .paused:
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is paused; resume it before start")
    case .starting, .running:
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is already \(runtime.event.state.rawValue)")
    }
}

func ensureCanDelete(identity: Identity, stateDir: String) throws {
    guard let runtime = try readRuntimeState(identity: identity, stateDir: stateDir) else {
        return
    }
    if processAlive(runtime.pid) {
        throw ProtocolError.invalid("apple-vf workspace \(identity.runtimeID) is running; stop or kill it before delete")
    }
}

func hostSupport() -> HostSupport {
    #if canImport(Virtualization)
    let available = true
    let supported: Bool
    let saveRestoreSupported: Bool
    if #available(macOS 13.0, *) {
        supported = VZVirtualMachine.isSupported
    } else {
        supported = false
    }
    if #available(macOS 14.0, *) {
        saveRestoreSupported = supported
    } else {
        saveRestoreSupported = false
    }
    #else
    let available = false
    let supported = false
    let saveRestoreSupported = false
    #endif
    return HostSupport(
        backend: backendName,
        architecture: hostArchitecture(),
        frameworkAvailable: available,
        virtualizationSupported: supported,
        supervisorPath: currentExecutablePath(),
        supervisorAvailable: true,
        pauseResumeAvailable: supported,
        snapshotCreateAvailable: saveRestoreSupported,
        snapshotAvailable: saveRestoreSupported,
        consoleAvailable: true,
        consoleMode: "interactive",
        confinementMode: confinementMode,
        confinementActive: confinementActiveOnThisHost()
    )
}

func runtimeDirectory(identity: Identity, stateDir: String) -> URL {
    URL(fileURLWithPath: stateDir).appendingPathComponent(identity.runtimeID, isDirectory: true)
}

func isSafeIdentifier(_ value: String) -> Bool {
    let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
    if trimmed.isEmpty || trimmed == "." || trimmed == ".." {
        return false
    }
    return !trimmed.contains("/") && !trimmed.contains("\\") && !trimmed.contains("\0")
}

func eventPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(eventFileName)
}

func configPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(configFileName)
}

func runtimePath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(runtimeFileName)
}

func serialLogPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(serialLogFileName)
}

func serialInputPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(serialInputFileName)
}

func supervisorLogPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(supervisorLogFileName)
}

func quarantineAckPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(quarantineAckFileName)
}

func applyRequestPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(applyRequestFileName)
}

func applyAckPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(applyAckFileName)
}

func runtimeControlRequestPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(runtimeControlRequestFileName)
}

func runtimeControlAckPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(runtimeControlAckFileName)
}

func snapshotDirectory(identity: Identity, stateDir: String, tag: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir)
        .appendingPathComponent("snapshots", isDirectory: true)
        .appendingPathComponent(tag, isDirectory: true)
}

func snapshotRootfsPath(identity: Identity, stateDir: String, tag: String) -> URL {
    snapshotDirectory(identity: identity, stateDir: stateDir, tag: tag).appendingPathComponent(snapshotRootfsFileName)
}

func snapshotMachineStatePath(identity: Identity, stateDir: String, tag: String) -> URL {
    snapshotDirectory(identity: identity, stateDir: stateDir, tag: tag).appendingPathComponent(snapshotMachineStateFileName)
}

func resultPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("result.json")
}

func normalizedFilePath(_ path: String) -> String {
    URL(fileURLWithPath: path).standardizedFileURL.path
}

func response(event: Event, config: Config, error: String?) -> Response {
    var response = Response(
        ok: event.state != .failed,
        backend: backendName,
        event: event,
        host: nil,
        readiness: readiness(event: event, config: config),
        result: try? readRuntimeResult(identity: event.identity, stateDir: config.stateDir),
        mediation: config.mediation,
        network: config.network,
        error: error
    )
    if let error, !error.isEmpty {
        response.ok = false
    }
    return response
}

func readiness(event: Event, config: Config) -> RuntimeReadiness {
    var readiness = RuntimeReadiness(
        guestReady: ReadinessSignal(),
        shellReady: ReadinessSignal(),
        execReady: ReadinessSignal(),
        resultReady: ReadinessSignal(),
        mediationReady: ReadinessSignal()
    )
    if event.state == .running || event.state == .paused || event.state == .halted || event.state == .stopped || event.state == .quarantined {
        readiness.guestReady = ReadinessSignal(ready: true, observedAt: event.observedAt, detail: "workspace reached runtime state \(event.state.rawValue)", error: nil)
    }
    if event.state == .running, config.serialInput == true {
        let path = serialInputPath(identity: event.identity, stateDir: config.stateDir)
        if FileManager.default.fileExists(atPath: path.path) {
            readiness.shellReady = ReadinessSignal(ready: true, observedAt: fileModTime(path), detail: "console input is available", error: nil)
        }
    }
    if event.state == .running, let execPort = config.execPort, execPort > 0 {
        readiness.execReady = ReadinessSignal(ready: true, observedAt: event.observedAt, detail: "structured exec forward is configured on 127.0.0.1:\(execPort)", error: nil)
    }
    let path = resultPath(identity: event.identity, stateDir: config.stateDir)
    if FileManager.default.fileExists(atPath: path.path) {
        readiness.resultReady = ReadinessSignal(ready: true, observedAt: fileModTime(path), detail: "guest result is available", error: nil)
    }
    if let mediation = config.mediation, mediation.enabled {
        let ready = event.state == .running
        readiness.mediationReady = ReadinessSignal(
            ready: ready,
            observedAt: event.observedAt,
            detail: "mediation required=\(mediation.required) failClosed=\(mediation.failClosed) port=\(mediation.port ?? 0) target=\(mediation.target ?? "")",
            error: !ready && mediation.required ? "required mediation is not ready" : nil
        )
    }
    return readiness
}

func readRuntimeResult(identity: Identity, stateDir: String) throws -> RuntimeResult {
    let path = resultPath(identity: identity, stateDir: stateDir)
    let guest = try decoder.decode(GuestResult.self, from: Data(contentsOf: path))
    return RuntimeResult(
        identity: identity,
        backend: backendName,
        resultPath: path.path,
        startedAt: guest.startedAt,
        completedAt: guest.exitedAt,
        exitCode: guest.exitCode,
        stdout: guest.stdout,
        stderr: guest.stderr,
        error: guest.error
    )
}

func fileModTime(_ url: URL) -> Date? {
    guard let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
          let modified = attributes[.modificationDate] as? Date else {
        return nil
    }
    return modified
}

func writeState(event: Event, config: Config) throws {
    let directory = runtimeDirectory(identity: event.identity, stateDir: config.stateDir)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try encoder.encode(event).write(to: eventPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
    try appendEvent(event: event, stateDir: config.stateDir)
    try encoder.encode(config).write(to: configPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
}

func appendEvent(event: Event, stateDir: String) throws {
    let maxEvents = 1024
    let path = runtimeDirectory(identity: event.identity, stateDir: stateDir).appendingPathComponent(eventsFileName)
    var events: [Event] = []
    if FileManager.default.fileExists(atPath: path.path) {
        let data = try Data(contentsOf: path)
        if !data.isEmpty {
            events = try decoder.decode([Event].self, from: data)
        }
    }
    events.append(event)
    if events.count > maxEvents {
        events = Array(events.suffix(maxEvents))
    }
    try encoder.encode(events).write(to: path, options: .atomic)
}

func writeRuntimeState(event: Event, config: Config, pid: Int32?, error: String?) throws {
    let previous = try? readRuntimeState(identity: event.identity, stateDir: config.stateDir)
    var runtimeConfig = config
    if (runtimeConfig.leaseSeconds ?? 0) <= 0, let previousLease = previous?.config.leaseSeconds, previousLease > 0 {
        runtimeConfig.leaseSeconds = previousLease
    }
    let startedAt = event.state == .starting || event.state == .running ? Date() : previous?.startedAt
    let runtime = RuntimeState(
        event: event,
        config: runtimeConfig,
        pid: pid,
        serialLogPath: serialLogPath(identity: event.identity, stateDir: runtimeConfig.stateDir).path,
        serialInputPath: serialInputPath(identity: event.identity, stateDir: runtimeConfig.stateDir).path,
        startedAt: startedAt,
        updatedAt: Date(),
        readiness: readiness(event: event, config: runtimeConfig),
        error: error
    )
    try encoder.encode(runtime).write(to: runtimePath(identity: event.identity, stateDir: runtimeConfig.stateDir), options: .atomic)
}

func readEvent(identity: Identity, stateDir: String) throws -> Event? {
    let path = eventPath(identity: identity, stateDir: stateDir)
    guard FileManager.default.fileExists(atPath: path.path) else {
        return nil
    }
    return try decoder.decode(Event.self, from: Data(contentsOf: path))
}

func readRuntimeState(identity: Identity, stateDir: String) throws -> RuntimeState? {
    let path = runtimePath(identity: identity, stateDir: stateDir)
    guard FileManager.default.fileExists(atPath: path.path) else {
        return nil
    }
    return try decoder.decode(RuntimeState.self, from: Data(contentsOf: path))
}

func requestJSON(_ request: Request) throws -> String {
    let data = try encoder.encode(request)
    return String(data: data, encoding: .utf8) ?? "{}"
}

func currentExecutablePath() -> String {
    #if canImport(Darwin)
    var size = UInt32(0)
    _ = _NSGetExecutablePath(nil, &size)
    var buffer = [CChar](repeating: 0, count: Int(size))
    if _NSGetExecutablePath(&buffer, &size) == 0 {
        let bytes = buffer.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }
        return String(decoding: bytes, as: UTF8.self)
    }
    #endif
    return CommandLine.arguments[0]
}

extension Request {
    func withCommand(_ command: String) -> Request {
        Request(command: command, identity: identity, config: config)
    }
}

func readableFile(_ path: String, name: String) throws {
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory), !isDirectory.boolValue, FileManager.default.isReadableFile(atPath: path) else {
        throw ProtocolError.invalid("\(name) is not readable at \(path)")
    }
}

func processAlive(_ pid: Int32?) -> Bool {
    guard let pid, pid > 0 else {
        return false
    }
    if kill(pid, 0) == 0 {
        return true
    }
    return errno == EPERM
}

func waitForProcessExit(pid: Int32, timeout: TimeInterval) -> Bool {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if !processAlive(pid) {
            return true
        }
        usleep(20_000)
    }
    return !processAlive(pid)
}

struct TCPHostPort {
    let host: String
    let port: UInt16
}

func parseTCPHostPort(_ raw: String) throws -> TCPHostPort {
    let parts = raw.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: ":", omittingEmptySubsequences: false)
    if parts.count != 2 || parts[0].isEmpty || parts[1].isEmpty {
        throw ProtocolError.invalid("target must be host:port, got \(raw)")
    }
    guard let port = UInt16(parts[1]) else {
        throw ProtocolError.invalid("target port is invalid in \(raw)")
    }
    return TCPHostPort(host: String(parts[0]), port: port)
}

#if canImport(Virtualization)
enum SerialAttachmentMode {
    case detached
    case standardIO
}

@available(macOS 13.0, *)
extension VZVirtioSocketConnection: @retroactive @unchecked Sendable {}

@available(macOS 13.0, *)
final class VMRunDelegate: NSObject, VZVirtualMachineDelegate {
    let identity: Identity
    let config: Config

    init(identity: Identity, config: Config) {
        self.identity = identity
        self.config = config
    }

    func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        updateRuntimeAfterGuestResult(identity: identity, config: config)
        closeHostFDEgress()
        CFRunLoopStop(CFRunLoopGetMain())
    }

    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        updateRuntime(identity: identity, config: config, state: .failed, error: error.localizedDescription)
        closeHostFDEgress()
        CFRunLoopStop(CFRunLoopGetMain())
    }
}

func updateRuntimeAfterGuestResult(identity: Identity, config: Config) {
    let result = try? readRuntimeResult(identity: identity, stateDir: config.stateDir)
    if let result, result.exitCode != 0 {
        updateRuntime(identity: identity, config: config, state: .failed, error: result.error ?? "guest exited with status \(result.exitCode)")
    } else {
        updateRuntime(identity: identity, config: config, state: .stopped, error: nil)
    }
}

@available(macOS 13.0, *)
final class SocketListenerHandle {
    let port: UInt32
    let listener: VZVirtioSocketListener
    let delegate: VZVirtioSocketListenerDelegate

    init(port: UInt32, listener: VZVirtioSocketListener, delegate: VZVirtioSocketListenerDelegate) {
        self.port = port
        self.listener = listener
        self.delegate = delegate
    }
}

@available(macOS 13.0, *)
protocol QuarantineClosable {
    func quarantineClose()
}

@available(macOS 13.0, *)
final class TCPPublishForwarder: @unchecked Sendable {
    private let socketDevice: VZVirtioSocketDevice
    private var listenerFDs: [Int32]
    private var currentForwards: [PortForward]
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(socketDevice: VZVirtioSocketDevice, forwards: [PortForward]) throws {
        self.socketDevice = socketDevice
        self.listenerFDs = []
        self.currentForwards = []
        try install(forwards: forwards)
    }

    deinit {
        quarantineClose()
    }

    func quarantineClose() {
        lock.lock()
        let fds = listenerFDs
        listenerFDs = []
        currentForwards = []
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for fd in fds {
            shutdown(fd, SHUT_RDWR)
            close(fd)
        }
        for connection in retainedConnections {
            connection.close()
        }
    }

    func reload(forwards: [PortForward]) throws {
        let previous = closeCurrent()
        do {
            try install(forwards: forwards)
        } catch {
            try? install(forwards: previous)
            throw error
        }
    }

    private func closeCurrent() -> [PortForward] {
        lock.lock()
        let previousForwards = currentForwards
        let fds = listenerFDs
        listenerFDs = []
        currentForwards = []
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for fd in fds {
            shutdown(fd, SHUT_RDWR)
            close(fd)
        }
        for connection in retainedConnections {
            connection.close()
        }
        return previousForwards
    }

    private func install(forwards: [PortForward]) throws {
        var opened: [Int32] = []
        do {
            for forward in forwards {
                opened.append(try listenTCP(forward))
            }
        } catch {
            for fd in opened {
                close(fd)
            }
            throw error
        }
        lock.lock()
        listenerFDs = opened
        currentForwards = forwards
        lock.unlock()
        for (idx, forward) in forwards.enumerated() {
            let fd = opened[idx]
            Thread.detachNewThread {
                self.acceptLoop(listenerFD: fd, guestVsockPort: guestVsockPort(forward))
            }
        }
    }

    private func acceptLoop(listenerFD: Int32, guestVsockPort: UInt32) {
        while true {
            let tcpFD = accept(listenerFD, nil, nil)
            if tcpFD < 0 {
                return
            }
            connectTCP(tcpFD, toGuestVsockPort: guestVsockPort)
        }
    }

    private func connectTCP(_ tcpFD: Int32, toGuestVsockPort guestVsockPort: UInt32) {
        let semaphore = DispatchSemaphore(value: 0)
        let resultBox = SocketConnectionResult()
        DispatchQueue.main.async {
            self.socketDevice.connect(toPort: guestVsockPort) { result in
                if case .success(let connection) = result {
                    resultBox.connection = connection
                }
                semaphore.signal()
            }
        }
        if semaphore.wait(timeout: .now() + 10) == .timedOut {
            close(tcpFD)
            return
        }
        guard let connection = resultBox.connection else {
            close(tcpFD)
            return
        }
        if !retain(connection) {
            close(tcpFD)
            connection.close()
            return
        }
        let vsockFD = connection.fileDescriptor
        Thread.detachNewThread {
            copyFD(from: tcpFD, to: vsockFD)
            shutdown(vsockFD, SHUT_WR)
            close(tcpFD)
            connection.close()
        }
        Thread.detachNewThread {
            copyFD(from: vsockFD, to: tcpFD)
            shutdown(tcpFD, SHUT_WR)
            connection.close()
            self.release(connection)
        }
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

func guestVsockPort(_ forward: PortForward) -> UInt32 {
    UInt32(forward.guestPort)
}

@available(macOS 13.0, *)
final class SocketConnectionResult: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: VZVirtioSocketConnection?

    var connection: VZVirtioSocketConnection? {
        get {
            lock.lock()
            defer { lock.unlock() }
            return stored
        }
        set {
            lock.lock()
            stored = newValue
            lock.unlock()
        }
    }
}

@available(macOS 13.0, *)
final class ResultSocketDelegate: NSObject, VZVirtioSocketListenerDelegate, @unchecked Sendable {
    private let path: String
    private let onResultWritten: (() -> Void)?
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(path: String, onResultWritten: (() -> Void)? = nil) {
        self.path = path
        self.onResultWritten = onResultWritten
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        if !retain(connection) {
            connection.close()
            return false
        }
        let fd = connection.fileDescriptor
        let path = self.path
        DispatchQueue.global(qos: .utility).async {
            defer {
                connection.close()
                self.release(connection)
            }
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while true {
                let n = read(fd, &buffer, buffer.count)
                if n > 0 {
                    if data.count + n > maxResultSocketBytes {
                        return
                    }
                    data.append(buffer, count: n)
                    continue
                }
                break
            }
            if data.isEmpty {
                return
            }
            do {
                try FileManager.default.createDirectory(at: URL(fileURLWithPath: path).deletingLastPathComponent(), withIntermediateDirectories: true)
                try data.write(to: URL(fileURLWithPath: path), options: .atomic)
                self.onResultWritten?()
            } catch {
                return
            }
        }
        return true
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
extension ResultSocketDelegate: QuarantineClosable {
    func quarantineClose() {
        lock.lock()
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for connection in retainedConnections {
            connection.close()
        }
    }
}

@available(macOS 13.0, *)
final class TCPSocketDelegate: NSObject, VZVirtioSocketListenerDelegate, @unchecked Sendable {
    private let target: TCPHostPort
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(target: TCPHostPort) {
        self.target = target
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        let remoteFD = dialTCP(target)
        if remoteFD < 0 {
            return false
        }
        if !retain(connection) {
            close(remoteFD)
            connection.close()
            return false
        }
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + .milliseconds(10)) {
            self.proxy(connection: connection, remoteFD: remoteFD)
        }
        return true
    }

    private func proxy(connection: VZVirtioSocketConnection, remoteFD: Int32) {
        let localFD = connection.fileDescriptor
        let group = DispatchGroup()
        group.enter()
        DispatchQueue.global(qos: .utility).async {
            copyFD(from: localFD, to: remoteFD)
            shutdown(remoteFD, SHUT_WR)
            group.leave()
        }
        group.enter()
        DispatchQueue.global(qos: .utility).async {
            copyFD(from: remoteFD, to: localFD)
            shutdown(localFD, SHUT_WR)
            group.leave()
        }
        group.notify(queue: .global(qos: .utility)) {
            close(remoteFD)
            connection.close()
            self.release(connection)
        }
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
extension TCPSocketDelegate: QuarantineClosable {
    func quarantineClose() {
        lock.lock()
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for connection in retainedConnections {
            connection.close()
        }
    }
}

struct SecretsRequest: Codable {
    var protocolVersion: String
    var name: String?

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol_version"
        case name
    }
}

struct SecretsEntry: Codable {
    var name: String
    var value: Data
}

struct SecretsBundle: Codable {
    var protocolVersion: String
    var secrets: [SecretsEntry]

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol_version"
        case secrets
    }
}

struct SecretsGetResponse: Codable {
    var protocolVersion: String
    var name: String
    var value: Data?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol_version"
        case name
        case value
        case error
    }
}

@available(macOS 13.0, *)
final class SecretsSocketDelegate: NSObject, VZVirtioSocketListenerDelegate, @unchecked Sendable {
    private let runtimeID: String
    private let stateDir: String
    private let bundle: SecretsBundle
    private let onDemand: [String: String]
    private let audit: Bool
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(identity: Identity, config: Config) throws {
        self.runtimeID = identity.runtimeID
        self.stateDir = config.stateDir
        self.bundle = try resolveSecretsBundle(config: config)
        var refs: [String: String] = [:]
        for ref in config.onDemandSecrets ?? [] {
            refs[ref.name] = ref.ref
        }
        self.onDemand = refs
        self.audit = config.secretsAudit == true
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        if !retain(connection) {
            connection.close()
            return false
        }
        let fd = connection.fileDescriptor
        DispatchQueue.global(qos: .utility).async {
            defer {
                connection.close()
                self.release(connection)
            }
            self.handle(fd: fd)
        }
        return true
    }

    private func handle(fd: Int32) {
        guard let req: SecretsRequest = try? readFramedJSON(fd: fd), req.protocolVersion == secretsProtocolVersion else {
            return
        }
        let name = req.name ?? ""
        if name.isEmpty {
            for entry in bundle.secrets {
                record(name: entry.name, access: "materialize", result: "ok")
            }
            _ = try? writeFramedJSON(fd: fd, bundle)
            return
        }
        guard let ref = onDemand[name] else {
            record(name: name, access: "on-demand", result: "denied")
            _ = try? writeFramedJSON(fd: fd, SecretsGetResponse(protocolVersion: secretsProtocolVersion, name: name, value: nil, error: "secret is not declared on-demand"))
            return
        }
        do {
            let value = try resolveSecret(ref: ref)
            record(name: name, access: "on-demand", result: "ok")
            try writeFramedJSON(fd: fd, SecretsGetResponse(protocolVersion: secretsProtocolVersion, name: name, value: value, error: nil))
        } catch {
            record(name: name, access: "on-demand", result: "error")
            _ = try? writeFramedJSON(fd: fd, SecretsGetResponse(protocolVersion: secretsProtocolVersion, name: name, value: nil, error: "resolve failed"))
        }
    }

    private func record(name: String, access: String, result: String) {
        guard audit else {
            return
        }
        let path = URL(fileURLWithPath: stateDir).appendingPathComponent(runtimeID).appendingPathComponent("secrets-access.jsonl")
        let rec = [
            "at": ISO8601DateFormatter().string(from: Date()),
            "runtime_id": runtimeID,
            "name": name,
            "access": access,
            "result": result,
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: rec, options: [.sortedKeys]) else {
            return
        }
        try? FileManager.default.createDirectory(at: path.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        if !FileManager.default.fileExists(atPath: path.path) {
            FileManager.default.createFile(atPath: path.path, contents: nil, attributes: [.posixPermissions: 0o600])
        }
        if let handle = try? FileHandle(forWritingTo: path) {
            _ = try? handle.seekToEnd()
            try? handle.write(contentsOf: data + Data([0x0a]))
            try? handle.close()
        }
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
extension SecretsSocketDelegate: QuarantineClosable {
    func quarantineClose() {
        lock.lock()
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for connection in retainedConnections {
            connection.close()
        }
    }
}

@available(macOS 13.0, *)
final class QuarantineController {
    private let identity: Identity
    private let config: Config
    private let vm: VZVirtualMachine
    private let socketListeners: [SocketListenerHandle]
    private let publishForwarder: TCPPublishForwarder?
    private var source: DispatchSourceSignal?
    private var quarantined = false

    init(identity: Identity, config: Config, vm: VZVirtualMachine, socketListeners: [SocketListenerHandle], publishForwarder: TCPPublishForwarder?) {
        self.identity = identity
        self.config = config
        self.vm = vm
        self.socketListeners = socketListeners
        self.publishForwarder = publishForwarder
    }

    func start() {
        signal(quarantineControlSignal, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: quarantineControlSignal, queue: .main)
        source.setEventHandler { [weak self] in
            self?.quarantine()
        }
        source.resume()
        self.source = source
    }

    private func quarantine() {
        if !quarantined {
            quarantined = true
            for device in vm.networkDevices {
                device.attachment = nil
            }
            if let socket = vm.socketDevices.first as? VZVirtioSocketDevice {
                for handle in socketListeners {
                    socket.removeSocketListener(forPort: handle.port)
                    (handle.delegate as? QuarantineClosable)?.quarantineClose()
                }
            }
            publishForwarder?.quarantineClose()
            try? FileManager.default.removeItem(at: serialInputPath(identity: identity, stateDir: config.stateDir))
        }
        writeQuarantineAck()
    }

    private func writeQuarantineAck() {
        let body: [String: String] = [
            "runtimeID": identity.runtimeID,
            "observedAt": ISO8601DateFormatter().string(from: Date())
        ]
        if let data = try? JSONSerialization.data(withJSONObject: body, options: [.prettyPrinted, .sortedKeys]) {
            try? data.write(to: quarantineAckPath(identity: identity, stateDir: config.stateDir), options: .atomic)
        }
    }
}

@available(macOS 13.0, *)
final class ApplyController {
    private let identity: Identity
    private let config: Config
    private let publishForwarder: TCPPublishForwarder?
    private var source: DispatchSourceSignal?

    init(identity: Identity, config: Config, publishForwarder: TCPPublishForwarder?) {
        self.identity = identity
        self.config = config
        self.publishForwarder = publishForwarder
    }

    func start() {
        signal(applyControlSignal, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: applyControlSignal, queue: .main)
        source.setEventHandler { [weak self] in
            self?.apply()
        }
        source.resume()
        self.source = source
    }

    private func apply() {
        let requestPath = applyRequestPath(identity: identity, stateDir: config.stateDir)
        let ackPath = applyAckPath(identity: identity, stateDir: config.stateDir)
        var errorMessage: String?
        do {
            let nextConfig = try decoder.decode(Config.self, from: Data(contentsOf: requestPath))
            guard livePortForwardHostOnlyChange(oldConfig: config, newConfig: nextConfig) else {
                throw ProtocolError.invalid("live network apply only supports host bind changes for existing port forwards")
            }
            let forwards = tcpPublishForwards(config: nextConfig)
            if forwards.isEmpty {
                publishForwarder?.quarantineClose()
            } else if let publishForwarder {
                try publishForwarder.reload(forwards: forwards)
            } else {
                throw ProtocolError.invalid("Apple VF publish forwarder is not available")
            }
        } catch {
            errorMessage = String(describing: error)
        }
        writeApplyAck(path: ackPath, error: errorMessage)
    }

    private func writeApplyAck(path: URL, error: String?) {
        let ack = ApplyAck(runtimeID: identity.runtimeID, observedAt: ISO8601DateFormatter().string(from: Date()), error: error)
        if let data = try? encoder.encode(ack) {
            try? data.write(to: path, options: .atomic)
        }
    }
}

@available(macOS 14.0, *)
final class RuntimeControlController {
    private let identity: Identity
    private let config: Config
    private let vm: VZVirtualMachine
    private let queue = DispatchQueue(label: "microagent.applevf.runtime-control", qos: .utility)
    private var source: DispatchSourceSignal?
    private var timer: DispatchSourceTimer?
    private var lastRequestData: Data?

    init(identity: Identity, config: Config, vm: VZVirtualMachine) {
        self.identity = identity
        self.config = config
        self.vm = vm
    }

    func start() {
        lastRequestData = try? Data(contentsOf: runtimeControlRequestPath(identity: identity, stateDir: config.stateDir))
        signal(runtimeControlSignal, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: runtimeControlSignal, queue: queue)
        source.setEventHandler { [weak self] in
            self?.applyControlRequest()
        }
        source.resume()
        self.source = source

        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + .milliseconds(100), repeating: .milliseconds(100))
        timer.setEventHandler { [weak self] in
            self?.applyControlRequest()
        }
        timer.resume()
        self.timer = timer
    }

    private func applyControlRequest() {
        let requestPath = runtimeControlRequestPath(identity: identity, stateDir: config.stateDir)
        let ackPath = runtimeControlAckPath(identity: identity, stateDir: config.stateDir)
        let request: RuntimeControlRequest
        do {
            let data = try Data(contentsOf: requestPath)
            if data == lastRequestData {
                return
            }
            lastRequestData = data
            request = try decoder.decode(RuntimeControlRequest.self, from: data)
        } catch {
            writeAck(path: ackPath, action: "unknown", error: String(describing: error))
            return
        }

        switch request.action {
        case "pause":
            performOnMainRunLoop { [weak self] in
                self?.pauseVM(path: ackPath)
            }
        case "resume":
            performOnMainRunLoop { [weak self] in
                self?.resumeVM(path: ackPath)
            }
        case "snapshot":
            let saveStatePath = request.saveStatePath
            let rootfsSnapshotPath = request.rootfsSnapshotPath
            performOnMainRunLoop { [weak self] in
                self?.snapshotVM(path: ackPath, saveStatePath: saveStatePath, rootfsSnapshotPath: rootfsSnapshotPath)
            }
        default:
            writeAck(path: ackPath, action: request.action, error: "unknown runtime control action \(request.action)")
        }
    }

    private func performOnMainRunLoop(_ block: @escaping () -> Void) {
        CFRunLoopPerformBlock(CFRunLoopGetMain(), CFRunLoopMode.defaultMode.rawValue, block)
        CFRunLoopWakeUp(CFRunLoopGetMain())
    }

    private func pauseVM(path: URL) {
        vm.pause { [weak self] result in
            switch result {
            case .success:
                self?.writeAck(path: path, action: "pause", error: nil)
            case .failure(let error):
                self?.writeAck(path: path, action: "pause", error: error.localizedDescription)
            }
        }
    }

    private func resumeVM(path: URL) {
        vm.resume { [weak self] result in
            switch result {
            case .success:
                do {
                    try self?.rehydrateMaterializedSecretsIfNeeded()
                    self?.writeAck(path: path, action: "resume", error: nil)
                } catch {
                    self?.writeAck(path: path, action: "resume", error: String(describing: error))
                }
            case .failure(let error):
                self?.writeAck(path: path, action: "resume", error: error.localizedDescription)
            }
        }
    }

    private func snapshotVM(path: URL, saveStatePath: String?, rootfsSnapshotPath: String?) {
        do {
            guard let saveStatePath, !saveStatePath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw ProtocolError.invalid("snapshot control missing saveStatePath")
            }
            guard let rootfsSnapshotPath, !rootfsSnapshotPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw ProtocolError.invalid("snapshot control missing rootfsSnapshotPath")
            }
            try snapshotVM(saveStatePath: URL(fileURLWithPath: saveStatePath), rootfsSnapshotPath: URL(fileURLWithPath: rootfsSnapshotPath))
            writeAck(path: path, action: "snapshot", error: nil)
        } catch {
            writeAck(path: path, action: "snapshot", error: String(describing: error))
        }
    }

    private func snapshotVM(saveStatePath: URL, rootfsSnapshotPath: URL) throws {
        let wasRunning = vm.state == .running
        if materializedSecretsDeclared(config) && !wasRunning {
            throw ProtocolError.invalid("cannot purge secrets for snapshot: workspace \(identity.runtimeID) is \(String(describing: vm.state)), must be running")
        }
        var purged = false
        var pausedForSnapshot = false
        do {
            if materializedSecretsDeclared(config) {
                try sendGuestSecretControl(op: "purge")
                purged = true
            }
            if wasRunning {
                try waitForVZResult(timeout: 10.0) { complete in
                    vm.pause { complete($0) }
                }
                pausedForSnapshot = true
            } else if vm.state != .paused {
                throw ProtocolError.invalid("snapshot requires VM state running or paused, got \(String(describing: vm.state))")
            }
            if FileManager.default.fileExists(atPath: saveStatePath.path) {
                try FileManager.default.removeItem(at: saveStatePath)
            }
            if FileManager.default.fileExists(atPath: rootfsSnapshotPath.path) {
                try FileManager.default.removeItem(at: rootfsSnapshotPath)
            }
            try FileManager.default.createDirectory(at: saveStatePath.deletingLastPathComponent(), withIntermediateDirectories: true)
            try waitForVZOptionalError(timeout: 30.0) { complete in
                vm.saveMachineStateTo(url: saveStatePath) { complete($0) }
            }
            try FileManager.default.copyItem(at: URL(fileURLWithPath: config.rootfsPath), to: rootfsSnapshotPath)
            if wasRunning {
                try waitForVZResult(timeout: 10.0) { complete in
                    vm.resume { complete($0) }
                }
                pausedForSnapshot = false
                if purged {
                    try sendGuestSecretControl(op: "rehydrate")
                }
            }
        } catch {
            if pausedForSnapshot {
                try? waitForVZResult(timeout: 10.0) { complete in
                    vm.resume { complete($0) }
                }
                if purged {
                    try? sendGuestSecretControl(op: "rehydrate")
                }
            }
            throw error
        }
    }

    private func rehydrateMaterializedSecretsIfNeeded() throws {
        if materializedSecretsDeclared(config) {
            try sendGuestSecretControl(op: "rehydrate")
        }
    }

    private func sendGuestSecretControl(op: String) throws {
        guard let port = config.secretsControlPort, port > 0 else {
            throw ProtocolError.invalid("materialized secrets require secretsControlPort")
        }
        guard let socket = vm.socketDevices.first as? VZVirtioSocketDevice else {
            throw ProtocolError.invalid("materialized secrets require a virtio socket device")
        }
        let connection = try connectGuestSocket(socket: socket, port: port, timeout: 10.0)
        defer { connection.close() }
        let fd = connection.fileDescriptor
        try writeFramedJSON(fd: fd, SecretControlRequest(protocolVersion: secretsProtocolVersion, op: op))
        let response: SecretControlResponse = try readFramedJSON(fd: fd)
        guard response.protocolVersion == secretsProtocolVersion else {
            throw ProtocolError.invalid("unsupported secrets protocol \(response.protocolVersion)")
        }
        if !response.ok {
            throw ProtocolError.invalid("secret control \(op) failed: \(response.error ?? "")")
        }
    }

    private func writeAck(path: URL, action: String, error: String?) {
        let ack = RuntimeControlAck(runtimeID: identity.runtimeID, action: action, observedAt: ISO8601DateFormatter().string(from: Date()), error: error)
        if let data = try? encoder.encode(ack) {
            try? data.write(to: path, options: .atomic)
        }
    }
}

@available(macOS 13.0, *)
final class CACertSocketDelegate: NSObject, VZVirtioSocketListenerDelegate, @unchecked Sendable {
    private let path: String
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(identity: Identity, config: Config) {
        self.path = URL(fileURLWithPath: config.stateDir)
            .appendingPathComponent(identity.runtimeID)
            .appendingPathComponent("egress-ca.pem")
            .path
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        if !retain(connection) {
            connection.close()
            return false
        }
        let fd = connection.fileDescriptor
        DispatchQueue.global(qos: .utility).async {
            defer {
                connection.close()
                self.release(connection)
            }
            self.handle(fd: fd)
        }
        return true
    }

    private func handle(fd: Int32) {
        guard let data = try? Data(contentsOf: URL(fileURLWithPath: path)),
              !data.isEmpty,
              data.count <= maxCACertBytes else {
            return
        }
        var frame = Data([
            UInt8((UInt32(data.count) >> 24) & 0xff),
            UInt8((UInt32(data.count) >> 16) & 0xff),
            UInt8((UInt32(data.count) >> 8) & 0xff),
            UInt8(UInt32(data.count) & 0xff),
        ])
        frame.append(data)
        _ = try? writeAll(fd: fd, data: frame)
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
extension CACertSocketDelegate: QuarantineClosable {
    func quarantineClose() {
        lock.lock()
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for connection in retainedConnections {
            connection.close()
        }
    }
}
#endif

func resolveSecretsBundle(config: Config) throws -> SecretsBundle {
    var entries: [SecretsEntry] = []
    var seen: Set<String> = []
    func add(name: String, value: Data) throws {
        if !validSecretName(name) {
            throw ProtocolError.invalid("invalid secret name \(name)")
        }
        if seen.contains(name) {
            throw ProtocolError.invalid("duplicate secret name \(name)")
        }
        seen.insert(name)
        entries.append(SecretsEntry(name: name, value: value))
    }
    for ref in config.secrets ?? [] {
        try add(name: ref.name, value: resolveSecret(ref: ref.ref))
    }
    for path in config.secretEnvFiles ?? [] {
        fputs("warning: secrets env file \"\(path)\" is plaintext: not encrypted at rest, not for production\n", stderr)
        let values = try loadDotenv(path: path)
        for key in values.keys.sorted() {
            try add(name: key, value: Data(values[key]!.utf8))
        }
    }
    return SecretsBundle(protocolVersion: secretsProtocolVersion, secrets: entries)
}

func resolveSecret(ref: String) throws -> Data {
    guard let idx = ref.firstIndex(of: ":") else {
        throw ProtocolError.invalid("secret reference \(ref) is missing a scheme")
    }
    let scheme = String(ref[..<idx])
    let rest = String(ref[ref.index(after: idx)...])
    let value: Data
    switch scheme {
    case "env":
        if rest.isEmpty {
            throw ProtocolError.invalid("env reference is missing a variable name")
        }
        fputs("warning: secret scheme \"env\" is plaintext: not encrypted at rest, not for production\n", stderr)
        value = Data((ProcessInfo.processInfo.environment[rest] ?? "").utf8)
    case "file":
        if rest.isEmpty {
            throw ProtocolError.invalid("file reference is missing a path")
        }
        fputs("warning: secret scheme \"file\" is plaintext: not encrypted at rest, not for production\n", stderr)
        value = try Data(contentsOf: URL(fileURLWithPath: rest))
    case "dotenv":
        let parts = rest.split(separator: "#", maxSplits: 1, omittingEmptySubsequences: false)
        if parts.count != 2 || parts[0].isEmpty || parts[1].isEmpty {
            throw ProtocolError.invalid("dotenv reference \(rest) must be PATH#KEY")
        }
        fputs("warning: secret scheme \"dotenv\" is plaintext: not encrypted at rest, not for production\n", stderr)
        let values = try loadDotenv(path: String(parts[0]))
        guard let found = values[String(parts[1])] else {
            throw ProtocolError.invalid("key \(parts[1]) not found in dotenv file \(parts[0])")
        }
        value = Data(found.utf8)
    default:
        throw ProtocolError.invalid("unknown secret scheme \(scheme)")
    }
    if value.isEmpty {
        throw ProtocolError.invalid("secret \(ref) resolved to an empty value")
    }
    return value
}

func loadDotenv(path: String) throws -> [String: String] {
    let text = try String(contentsOfFile: path, encoding: .utf8)
    var out: [String: String] = [:]
    for (idx, rawLine) in text.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
        var line = rawLine.trimmingCharacters(in: .whitespacesAndNewlines)
        if line.isEmpty || line.hasPrefix("#") {
            continue
        }
        if line.hasPrefix("export ") {
            line.removeFirst("export ".count)
        }
        guard let eq = line.firstIndex(of: "=") else {
            throw ProtocolError.invalid("dotenv line \(idx + 1) is not KEY=VALUE")
        }
        let key = line[..<eq].trimmingCharacters(in: .whitespacesAndNewlines)
        if key.isEmpty {
            throw ProtocolError.invalid("dotenv line \(idx + 1) has an empty key")
        }
        var value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespacesAndNewlines)
        if value.count >= 2, let first = value.first, let last = value.last, (first == "\"" && last == "\"") || (first == "'" && last == "'") {
            value.removeFirst()
            value.removeLast()
        }
        out[key] = value
    }
    return out
}

func validSecretName(_ name: String) -> Bool {
    if name.isEmpty || name.count > 128 {
        return false
    }
    for scalar in name.unicodeScalars {
        let v = scalar.value
        let ok = (v >= 65 && v <= 90) || (v >= 97 && v <= 122) || (v >= 48 && v <= 57) || v == 95 || v == 45 || v == 46
        if !ok {
            return false
        }
    }
    return true
}

func materializedSecretsDeclared(_ config: Config) -> Bool {
    return !(config.secrets ?? []).isEmpty || !(config.secretEnvFiles ?? []).isEmpty
}

#if canImport(Virtualization)
@available(macOS 13.0, *)
func connectGuestSocket(socket: VZVirtioSocketDevice, port: UInt32, timeout: TimeInterval) throws -> VZVirtioSocketConnection {
    var result: Result<VZVirtioSocketConnection, Error>?
    socket.connect(toPort: port) { completed in
        result = completed
    }
    let deadline = Date().addingTimeInterval(timeout)
    while result == nil && Date() < deadline {
        RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
    }
    guard let result else {
        throw ProtocolError.invalid("guest socket port \(port) did not connect before timeout")
    }
    return try result.get()
}
#endif

func readFramedJSON<T: Decodable>(fd: Int32) throws -> T {
    let prefix = try readExact(fd: fd, count: 4)
    let length = prefix.withUnsafeBytes { raw -> UInt32 in
        let bytes = raw.bindMemory(to: UInt8.self)
        return (UInt32(bytes[0]) << 24) | (UInt32(bytes[1]) << 16) | (UInt32(bytes[2]) << 8) | UInt32(bytes[3])
    }
    if length > maxSecretsMessageBytes {
        throw ProtocolError.invalid("secretxfer message length \(length) exceeds maximum \(maxSecretsMessageBytes)")
    }
    let data = try readExact(fd: fd, count: Int(length))
    return try JSONDecoder().decode(T.self, from: data)
}

func writeFramedJSON<T: Encodable>(fd: Int32, _ msg: T) throws {
    let data = try JSONEncoder().encode(msg)
    if data.count > UInt32.max {
        throw ProtocolError.invalid("secretxfer message length exceeds uint32 prefix")
    }
    var frame = Data([
        UInt8((UInt32(data.count) >> 24) & 0xff),
        UInt8((UInt32(data.count) >> 16) & 0xff),
        UInt8((UInt32(data.count) >> 8) & 0xff),
        UInt8(UInt32(data.count) & 0xff),
    ])
    frame.append(data)
    try writeAll(fd: fd, data: frame)
}

func readExact(fd: Int32, count: Int) throws -> Data {
    var out = Data()
    var buffer = [UInt8](repeating: 0, count: min(4096, max(count, 1)))
    while out.count < count {
        let wanted = min(buffer.count, count - out.count)
        let n = read(fd, &buffer, wanted)
        if n < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
            continue
        }
        if n <= 0 {
            throw ProtocolError.invalid("short read")
        }
        out.append(buffer, count: n)
    }
    return out
}

func writeAll(fd: Int32, data: Data) throws {
    try data.withUnsafeBytes { raw in
        guard let base = raw.baseAddress else {
            return
        }
        var written = 0
        while written < data.count {
            let n = write(fd, base.advanced(by: written), data.count - written)
            if n < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
                continue
            }
            if n <= 0 {
                throw ProtocolError.invalid("short write")
            }
            written += n
        }
    }
}

func updateRuntime(identity: Identity, config: Config, state: VMState, error: String?) {
    let event = Event(identity: identity, state: state, detail: "serial=\(serialLogPath(identity: identity, stateDir: config.stateDir).path)", observedAt: Date())
    do {
        let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir)
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: runtime?.pid, error: error)
    } catch {
        // Background VM state updates cannot safely change the stdout protocol.
    }
}

func runVM(_ request: Request) throws {
    let identity = try validatedIdentity(request.identity)
    let config = try validatedConfig(request.config)
    let runtimeConfig = try runtimeConfigForStart(identity: identity, config: config)
    #if canImport(Virtualization)
    guard hostSupport().virtualizationSupported else {
        throw ProtocolError.invalid("Apple Virtualization is not available on this host")
    }
    if #available(macOS 13.0, *) {
        // Spawn the host-fd egress datapath BEFORE confinement so it runs
        // unsandboxed (the Seatbelt profile is inherited by children and is
        // loopback-only; the datapath needs full network access to NAT).
        try prepareHostFDEgressBeforeConfinement(config: runtimeConfig, identity: identity)
        defer { closeHostFDEgress() }
        // Confine this detached VM child before any VM resources are created
        // (Spec B). Fail-closed: if the Seatbelt sandbox cannot be applied, the
        // VM does not start.
        try applyConfinement(identity: identity, config: runtimeConfig, qos: QOS_CLASS_UTILITY)
        let vmConfig = try virtualMachineConfiguration(identity: identity, config: runtimeConfig, serialMode: .detached)
        try vmConfig.validate()
        let restoreTag = try validatedOptionalSnapshotTag(request.tag)
        if restoreTag != nil {
            if #available(macOS 14.0, *) {
                try vmConfig.validateSaveRestoreSupport()
            } else {
                throw ProtocolError.invalid("Apple VF snapshot restore requires macOS 14 or newer")
            }
        }

        let vm = VZVirtualMachine(configuration: vmConfig)
        let delegate = VMRunDelegate(identity: identity, config: runtimeConfig)
        vm.delegate = delegate
        if let restoreTag {
            if #available(macOS 14.0, *) {
                let saveState = snapshotMachineStatePath(identity: identity, stateDir: runtimeConfig.stateDir, tag: restoreTag)
                do {
                    try waitForVZOptionalError(timeout: 60.0) { complete in
                        vm.restoreMachineStateFrom(url: saveState) { complete($0) }
                    }
                } catch {
                    updateRuntime(identity: identity, config: runtimeConfig, state: .failed, error: error.localizedDescription)
                    throw error
                }
            } else {
                let error = ProtocolError.invalid("Apple VF snapshot restore requires macOS 14 or newer")
                updateRuntime(identity: identity, config: runtimeConfig, state: .failed, error: String(describing: error))
                throw error
            }
        }
        let socketListeners = try installSocketListeners(vm: vm, identity: identity, config: runtimeConfig)
        let publishForwarder = try installTCPPublishForwarder(vm: vm, config: runtimeConfig)
        let quarantineController = QuarantineController(identity: identity, config: runtimeConfig, vm: vm, socketListeners: socketListeners, publishForwarder: publishForwarder)
        let applyController = ApplyController(identity: identity, config: runtimeConfig, publishForwarder: publishForwarder)
        let runtimeControlController = RuntimeControlController(identity: identity, config: runtimeConfig, vm: vm)
        quarantineController.start()
        applyController.start()
        runtimeControlController.start()
        let semaphore = DispatchSemaphore(value: 0)
        var startError: Error?
        if let restoreTag {
            _ = restoreTag
            vm.resume { result in
                switch result {
                case .success:
                    updateRuntime(identity: identity, config: runtimeConfig, state: .running, error: nil)
                    let storedConfig = (try? readRuntimeState(identity: identity, stateDir: runtimeConfig.stateDir))?.config ?? runtimeConfig
                    var deadmanRequest = request
                    deadmanRequest.config = storedConfig
                    startDeadmanProcessIfNeeded(request: deadmanRequest, identity: identity, config: storedConfig)
                case .failure(let error):
                    startError = error
                    updateRuntime(identity: identity, config: runtimeConfig, state: .failed, error: error.localizedDescription)
                }
                semaphore.signal()
            }
        } else {
            vm.start { result in
                switch result {
                case .success:
                    updateRuntime(identity: identity, config: runtimeConfig, state: .running, error: nil)
                    let storedConfig = (try? readRuntimeState(identity: identity, stateDir: runtimeConfig.stateDir))?.config ?? runtimeConfig
                    var deadmanRequest = request
                    deadmanRequest.config = storedConfig
                    startDeadmanProcessIfNeeded(request: deadmanRequest, identity: identity, config: storedConfig)
                case .failure(let error):
                    startError = error
                    updateRuntime(identity: identity, config: runtimeConfig, state: .failed, error: error.localizedDescription)
                }
                semaphore.signal()
            }
        }
        while semaphore.wait(timeout: .now()) == .timedOut {
            RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
        }
        if let startError {
            throw startError
        }
        withExtendedLifetime((delegate, socketListeners, publishForwarder, quarantineController, applyController, runtimeControlController)) {
            CFRunLoopRun()
        }
    } else {
        throw ProtocolError.invalid("Apple Virtualization requires macOS 13 or newer")
    }
    #else
    throw ProtocolError.invalid("Virtualization.framework is not available in this build")
    #endif
}

func runConsole(_ request: Request) throws {
    let identity = try validatedIdentity(request.identity)
    let config = try validatedConfig(request.config)
    let runtimeConfig = try runtimeConfigForStart(identity: identity, config: config)
    #if canImport(Virtualization)
    guard hostSupport().virtualizationSupported else {
        throw ProtocolError.invalid("Apple Virtualization is not available on this host")
    }
    if #available(macOS 13.0, *) {
        // Spawn the host-fd egress datapath before confinement (see runVM).
        try prepareHostFDEgressBeforeConfinement(config: runtimeConfig, identity: identity)
        defer { closeHostFDEgress() }
        // Confine the console VM child too (Spec B). User-initiated QoS keeps the
        // interactive session responsive. Fail-closed on sandbox failure.
        try applyConfinement(identity: identity, config: runtimeConfig, qos: QOS_CLASS_USER_INITIATED)
        let vmConfig = try virtualMachineConfiguration(identity: identity, config: runtimeConfig, serialMode: .standardIO)
        try vmConfig.validate()

        let vm = VZVirtualMachine(configuration: vmConfig)
        let delegate = VMRunDelegate(identity: identity, config: runtimeConfig)
        vm.delegate = delegate
        let socketListeners = try installSocketListeners(vm: vm, identity: identity, config: runtimeConfig)
        let publishForwarder = try installTCPPublishForwarder(vm: vm, config: runtimeConfig)
        let quarantineController = QuarantineController(identity: identity, config: runtimeConfig, vm: vm, socketListeners: socketListeners, publishForwarder: publishForwarder)
        let applyController = ApplyController(identity: identity, config: runtimeConfig, publishForwarder: publishForwarder)
        let runtimeControlController = RuntimeControlController(identity: identity, config: runtimeConfig, vm: vm)
        quarantineController.start()
        applyController.start()
        runtimeControlController.start()
        let semaphore = DispatchSemaphore(value: 0)
        var startError: Error?
        vm.start { result in
            switch result {
            case .success:
                updateRuntime(identity: identity, config: runtimeConfig, state: .running, error: nil)
            case .failure(let error):
                startError = error
                updateRuntime(identity: identity, config: runtimeConfig, state: .failed, error: error.localizedDescription)
            }
            semaphore.signal()
        }
        while semaphore.wait(timeout: .now()) == .timedOut {
            RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
        }
        if let startError {
            throw startError
        }
        withExtendedLifetime((delegate, socketListeners, publishForwarder, quarantineController, applyController, runtimeControlController)) {
            CFRunLoopRun()
        }
    } else {
        throw ProtocolError.invalid("Apple Virtualization requires macOS 13 or newer")
    }
    #else
    throw ProtocolError.invalid("Virtualization.framework is not available in this build")
    #endif
}

#if canImport(Virtualization)
@available(macOS 13.0, *)
func installSocketListeners(vm: VZVirtualMachine, identity: Identity, config: Config) throws -> [SocketListenerHandle] {
    guard let socket = vm.socketDevices.first as? VZVirtioSocketDevice else {
        return []
    }
    var handles: [SocketListenerHandle] = []
    var listeners = config.vsockListeners ?? []
    if let mediation = config.mediation, mediation.enabled, let port = mediation.port, let target = mediation.target {
        if let existing = listeners.first(where: { $0.port == port }) {
            if existing.target != target {
                throw ProtocolError.invalid("mediation port \(port) conflicts with vsock listener target \(existing.target)")
            }
        } else {
            listeners.append(VsockListener(port: port, target: target))
        }
    }
    for listenerConfig in listeners {
        let listener = VZVirtioSocketListener()
        let delegate: VZVirtioSocketListenerDelegate
        if listenerConfig.target == secretsListenerTarget {
            delegate = try SecretsSocketDelegate(identity: identity, config: config)
        } else if listenerConfig.target == caCertListenerTarget {
            delegate = CACertSocketDelegate(identity: identity, config: config)
        } else if let target = try? parseTCPHostPort(listenerConfig.target) {
            delegate = TCPSocketDelegate(target: target)
        } else {
            if normalizedFilePath(listenerConfig.target) != normalizedFilePath(resultPath(identity: identity, stateDir: config.stateDir).path) {
                throw ProtocolError.invalid("vsock listener \(listenerConfig.port) target must be host:port or the runtime result path")
            }
            delegate = ResultSocketDelegate(path: listenerConfig.target, onResultWritten: {
                if hostFDEgressEnabled(config: config) {
                    updateRuntimeAfterGuestResult(identity: identity, config: config)
                    closeHostFDEgress()
                    CFRunLoopStop(CFRunLoopGetMain())
                }
            })
        }
        listener.delegate = delegate
        socket.setSocketListener(listener, forPort: listenerConfig.port)
        handles.append(SocketListenerHandle(port: listenerConfig.port, listener: listener, delegate: delegate))
    }
    return handles
}

@available(macOS 13.0, *)
func installTCPPublishForwarder(vm: VZVirtualMachine, config: Config) throws -> TCPPublishForwarder? {
    let forwards = tcpPublishForwards(config: config)
    if forwards.isEmpty {
        return nil
    }
    guard let socket = vm.socketDevices.first as? VZVirtioSocketDevice else {
        throw ProtocolError.invalid("Apple VF publish requires a virtio socket device")
    }
    return try TCPPublishForwarder(socketDevice: socket, forwards: forwards)
}

func tcpPublishForwards(config: Config) -> [PortForward] {
    var forwards = config.network?.portForwards ?? []
    if let shellPort = config.shellPort, shellPort > 0 {
        forwards.append(PortForward(protocolName: "tcp", host: "127.0.0.1", hostPort: shellPort, guestPort: guestShellPort(config)))
    }
    if let execPort = config.execPort, execPort > 0 {
        forwards.append(PortForward(protocolName: "tcp", host: "127.0.0.1", hostPort: execPort, guestPort: guestExecPort(config)))
    }
    return forwards
}

func livePortForwardHostOnlyChange(oldConfig: Config, newConfig: Config) -> Bool {
    if oldConfig.shellPort != newConfig.shellPort {
        return false
    }
    if oldConfig.guestShellPort != newConfig.guestShellPort {
        return false
    }
    if oldConfig.execPort != newConfig.execPort || oldConfig.guestExecPort != newConfig.guestExecPort {
        return false
    }
    let oldNetwork = oldConfig.network
    let newNetwork = newConfig.network
    if oldNetwork?.mode != newNetwork?.mode ||
        (oldNetwork?.dns ?? []) != (newNetwork?.dns ?? []) ||
        (oldNetwork?.routes ?? []) != (newNetwork?.routes ?? []) ||
        oldNetwork?.ip != newNetwork?.ip {
        return false
    }
    let oldForwards = oldNetwork?.portForwards ?? []
    let newForwards = newNetwork?.portForwards ?? []
    if oldForwards.count != newForwards.count {
        return false
    }
    for idx in oldForwards.indices {
        let oldForward = oldForwards[idx]
        let newForward = newForwards[idx]
        if oldForward.protocolName != newForward.protocolName ||
            oldForward.hostPort != newForward.hostPort ||
            oldForward.guestPort != newForward.guestPort {
            return false
        }
    }
    return true
}

func listenTCP(_ forward: PortForward) throws -> Int32 {
    if forward.protocolName != "" && forward.protocolName != "tcp" {
        throw ProtocolError.invalid("publish protocol must be tcp")
    }
    let host = (forward.host ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    let bindHost = host.isEmpty ? "127.0.0.1" : (host == "localhost" ? "127.0.0.1" : host)
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 {
        throw ProtocolError.invalid("open published tcp socket failed with errno \(errno)")
    }
    var yes: Int32 = 1
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
    var addr = sockaddr_in()
    #if os(macOS)
    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    #endif
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = forward.hostPort.bigEndian
    guard inet_pton(AF_INET, bindHost, &addr.sin_addr) == 1 else {
        close(fd)
        throw ProtocolError.invalid("publish host \(bindHost) must be an IPv4 address or localhost")
    }
    let bindResult = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    if bindResult != 0 {
        let saved = errno
        close(fd)
        throw ProtocolError.invalid("listen \(bindHost):\(forward.hostPort) failed with errno \(saved)")
    }
    if listen(fd, 128) != 0 {
        let saved = errno
        close(fd)
        throw ProtocolError.invalid("listen \(bindHost):\(forward.hostPort) failed with errno \(saved)")
    }
    return fd
}

func dialTCP(_ target: TCPHostPort) -> Int32 {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 {
        return -1
    }
    var addr = sockaddr_in()
    #if os(macOS)
    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    #endif
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = target.port.bigEndian
    let host = target.host == "localhost" ? "127.0.0.1" : target.host
    guard inet_pton(AF_INET, host, &addr.sin_addr) == 1 else {
        close(fd)
        return -1
    }
    let result = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    if result == 0 {
        return fd
    }
    let saved = errno
    if saved == EINTR || saved == EINPROGRESS || saved == EALREADY {
        if waitForTCPConnect(fd: fd, timeoutMillis: 5_000) {
            return fd
        }
    }
    close(fd)
    return -1
}

func waitForTCPConnect(fd: Int32, timeoutMillis: Int32) -> Bool {
    var pfd = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
    while true {
        let result = poll(&pfd, 1, timeoutMillis)
        if result < 0 && errno == EINTR {
            continue
        }
        if result <= 0 {
            close(fd)
            return false
        }
        var error: Int32 = 0
        var length = socklen_t(MemoryLayout<Int32>.size)
        if getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &length) != 0 || error != 0 {
            close(fd)
            return false
        }
        return true
    }
}

@discardableResult
func copyFD(from source: Int32, to destination: Int32) -> Int64 {
    var total: Int64 = 0
    var buffer = [UInt8](repeating: 0, count: 32 * 1024)
    while true {
        let readCount = buffer.withUnsafeMutableBytes {
            read(source, $0.baseAddress, $0.count)
        }
        if readCount < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
            usleep(1_000)
            continue
        }
        if readCount <= 0 {
            return total
        }
        var written = 0
        while written < readCount {
            let result = buffer.withUnsafeBytes {
                write(destination, $0.baseAddress!.advanced(by: written), readCount - written)
            }
            if result < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
                usleep(1_000)
                continue
            }
            if result <= 0 {
                return total
            }
            written += result
            total += Int64(result)
        }
    }
}

@available(macOS 13.0, *)
func virtualMachineConfiguration(identity: Identity, config: Config, serialMode: SerialAttachmentMode?) throws -> VZVirtualMachineConfiguration {
    let vmConfig = VZVirtualMachineConfiguration()
    let platform = VZGenericPlatformConfiguration()
    platform.machineIdentifier = try genericMachineIdentifier(from: config)
    vmConfig.platform = platform
    let bootLoader = VZLinuxBootLoader(kernelURL: URL(fileURLWithPath: config.kernelPath))
    bootLoader.commandLine = linuxKernelCommandLine(for: config)
    vmConfig.bootLoader = bootLoader
    vmConfig.cpuCount = config.cpuCount ?? 2
    vmConfig.memorySize = UInt64(config.memoryMiB ?? 512) * 1024 * 1024
    let attachment = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: config.rootfsPath), readOnly: false)
    if let disks = config.disks, !disks.isEmpty {
        var storageDevices: [VZVirtioBlockDeviceConfiguration] = [VZVirtioBlockDeviceConfiguration(attachment: attachment)]
        for disk in disks {
            let diskAttachment = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: disk.path), readOnly: disk.mode == "ro")
            storageDevices.append(VZVirtioBlockDeviceConfiguration(attachment: diskAttachment))
        }
        vmConfig.storageDevices = storageDevices
    } else {
        vmConfig.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: attachment)]
    }
    vmConfig.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
    vmConfig.networkDevices = try networkDevices(for: config, identity: identity)
    if let serialMode {
        let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
        switch serialMode {
        case .detached:
            try FileManager.default.createDirectory(at: runtimeDirectory(identity: identity, stateDir: config.stateDir), withIntermediateDirectories: true)
            let inputHandle: FileHandle?
            if config.serialInput == true {
                let inputURL = serialInputPath(identity: identity, stateDir: config.stateDir)
                try prepareSerialInput(path: inputURL.path)
                let inputPipe = Pipe()
                bridgeSerialInput(path: inputURL.path, to: inputPipe.fileHandleForWriting)
                inputHandle = inputPipe.fileHandleForReading
            } else {
                inputHandle = nil
            }
            FileManager.default.createFile(atPath: serialLogPath(identity: identity, stateDir: config.stateDir).path, contents: nil)
            let serialHandle = try FileHandle(forWritingTo: serialLogPath(identity: identity, stateDir: config.stateDir))
            try serialHandle.seekToEnd()
            serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: inputHandle, fileHandleForWriting: serialHandle)
        case .standardIO:
            configureRawTerminal(FileHandle.standardInput)
            serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: FileHandle.standardInput, fileHandleForWriting: FileHandle.standardOutput)
        }
        vmConfig.serialPorts = [serial]
    }
    if !(config.vsockListeners ?? []).isEmpty || config.mediation?.enabled == true || !(config.network?.portForwards ?? []).isEmpty || config.shellPort != nil || config.execPort != nil || config.secretsPort != nil || config.modelVsockPort != nil {
        vmConfig.socketDevices = [VZVirtioSocketDeviceConfiguration()]
    }
    return vmConfig
}

func linuxKernelCommandLine(for config: Config) -> String {
    var args = ["console=hvc0", "root=/dev/vda", "rw", "init=/sbin/microagent-init"]
    if let shellPort = config.shellPort, shellPort > 0 {
        args.append("microagent_shell_port=\(guestShellPort(config))")
    }
    if let execPort = config.execPort, execPort > 0 {
        args.append("microagent_exec_port=\(guestExecPort(config))")
    }
    if let secretsPort = config.secretsPort, secretsPort > 0 {
        args.append("microagent_secrets_port=\(secretsPort)")
    }
    if !(config.onDemandSecrets ?? []).isEmpty {
        args.append("microagent_secrets_api=1")
    }
    if let secretsControlPort = config.secretsControlPort, secretsControlPort > 0 {
        args.append("microagent_secrets_ctl_port=\(secretsControlPort)")
    }
    if let modelGuestPort = config.modelGuestPort, modelGuestPort > 0,
       let modelVsockPort = config.modelVsockPort, modelVsockPort > 0 {
        args.append("microagent_model_fwd=\(modelGuestPort):\(modelVsockPort)")
    }
    switch normalizedNetworkMode(config.network) {
    case "user" where hostFDEgressEnabled(config: config):
        // The host-fd gateway owns a fixed subnet; the guest is statically
        // configured to it regardless of any spec network fields.
        args.append("microagent_net_if=eth0")
        args.append("microagent_net_ip=\(hostFDGuestIP)")
        args.append("microagent_net_gw=\(hostFDGatewayIP)")
        args.append("microagent_net_dns=\(hostFDGuestDNS)")
    case "user":
        let ip = config.network?.ip?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let gateway = config.network?.gateway?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let dns = config.network?.dns?.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty } ?? []
        if !ip.isEmpty && !gateway.isEmpty {
            args.append("microagent_net_if=eth0")
            args.append("microagent_net_ip=\(ip)")
            args.append("microagent_net_gw=\(gateway)")
            if !dns.isEmpty {
                args.append("microagent_net_dns=\(dns.joined(separator: ","))")
            }
        } else {
            args.append("ip=dhcp")
            args.append("microagent_dns=\(dhcpDNSNameservers(explicit: dns).joined(separator: ","))")
            args.append("microagent_dns_fallback_gateway=1")
        }
    default:
        break
    }
    return args.joined(separator: " ")
}

struct HostDNSResolver {
    var nameservers: [String]
    var interface: String
    var flags: String
}

func dhcpDNSNameservers(explicit: [String]) -> [String] {
    if !explicit.isEmpty {
        return explicit
    }
    let host = hostIPv4DNSNameservers()
    if !host.isEmpty {
        return host
    }
    return ["1.1.1.1", "8.8.8.8"]
}

func hostIPv4DNSNameservers() -> [String] {
    guard let output = scutilDNSOutput() else {
        return []
    }
    var resolvers: [HostDNSResolver] = []
    var nameservers: [String] = []
    var iface = ""
    var flags = ""

    func finishResolver() {
        if !nameservers.isEmpty {
            resolvers.append(HostDNSResolver(nameservers: nameservers, interface: iface, flags: flags))
        }
        nameservers = []
        iface = ""
        flags = ""
    }

    for line in output.split(separator: "\n", omittingEmptySubsequences: false) {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        if trimmed.hasPrefix("resolver #") {
            finishResolver()
            continue
        }
        if trimmed.hasPrefix("nameserver[") {
            let value = trimmed.split(separator: ":", maxSplits: 1).last?.trimmingCharacters(in: .whitespaces) ?? ""
            if isIPv4Address(value) {
                nameservers.append(value)
            }
            continue
        }
        if trimmed.hasPrefix("if_index") {
            if let open = trimmed.firstIndex(of: "("), let close = trimmed.firstIndex(of: ")"), open < close {
                iface = String(trimmed[trimmed.index(after: open)..<close])
            }
            continue
        }
        if trimmed.hasPrefix("flags") {
            flags = trimmed.split(separator: ":", maxSplits: 1).last?.trimmingCharacters(in: .whitespaces) ?? ""
        }
    }
    finishResolver()

    let preferred = resolvers.first {
        !$0.interface.hasPrefix("utun") && !$0.flags.contains("Supplemental")
    }
    if let preferred {
        return uniqueIPv4Nameservers(preferred.nameservers)
    }
    return uniqueIPv4Nameservers(resolvers.flatMap { $0.nameservers })
}

func scutilDNSOutput() -> String? {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/sbin/scutil")
    process.arguments = ["--dns"]
    let pipe = Pipe()
    process.standardOutput = pipe
    process.standardError = FileHandle.nullDevice
    do {
        try process.run()
        process.waitUntilExit()
    } catch {
        return nil
    }
    guard process.terminationStatus == 0 else {
        return nil
    }
    return String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)
}

func isIPv4Address(_ value: String) -> Bool {
    var addr = in_addr()
    return value.withCString { inet_pton(AF_INET, $0, &addr) == 1 }
}

func uniqueIPv4Nameservers(_ values: [String]) -> [String] {
    var seen = Set<String>()
    var unique: [String] = []
    for value in values where !seen.contains(value) {
        seen.insert(value)
        unique.append(value)
    }
    return unique
}

func guestExecPort(_ config: Config) -> UInt16 {
    if let guestExecPort = config.guestExecPort, guestExecPort > 0 {
        return guestExecPort
    }
    return config.execPort ?? 0
}

func guestShellPort(_ config: Config) -> UInt16 {
    if let guestShellPort = config.guestShellPort, guestShellPort > 0 {
        return guestShellPort
    }
    return config.shellPort ?? 0
}

func runtimeConfigForStart(identity: Identity, config: Config) throws -> Config {
    var runtimeConfig = config
    #if canImport(Virtualization)
    if #available(macOS 13.0, *), (runtimeConfig.appleVFMachineIdentifier ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        runtimeConfig.appleVFMachineIdentifier = VZGenericMachineIdentifier().dataRepresentation.base64EncodedString()
    }
    if #available(macOS 13.0, *), (runtimeConfig.appleVFNetworkMACAddress ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        runtimeConfig.appleVFNetworkMACAddress = VZMACAddress.randomLocallyAdministered().string
    }
    #endif
    return runtimeConfig
}

@available(macOS 13.0, *)
func genericMachineIdentifier(from config: Config) throws -> VZGenericMachineIdentifier {
    let encoded = (config.appleVFMachineIdentifier ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    if encoded.isEmpty {
        return VZGenericMachineIdentifier()
    }
    guard let data = Data(base64Encoded: encoded), let identifier = VZGenericMachineIdentifier(dataRepresentation: data) else {
        throw ProtocolError.invalid("appleVFMachineIdentifier is not a valid VZGenericMachineIdentifier data representation")
    }
    return identifier
}

@available(macOS 13.0, *)
func networkDevices(for config: Config, identity: Identity) throws -> [VZVirtioNetworkDeviceConfiguration] {
    switch normalizedNetworkMode(config.network) {
    case "user":
        // Host-fd egress capture provider (opt-in for S1): the guest NIC is a
        // host-owned socket driven by the microagent userspace gateway, so all
        // egress is captured and (with mediation) cannot bypass the mediator.
        if hostFDEgressEnabled(config: config) {
            return [try makeHostFDNetworkDevice(macAddress: appleVFNetworkMACAddress(from: config))]
        }
        // Default: Apple Virtualization.framework's VZNATNetworkDeviceAttachment
        // runs in user space inside the framework, providing the unprivileged
        // outbound-only semantics that "user" mode promises on Linux via pasta.
        let device = VZVirtioNetworkDeviceConfiguration()
        device.attachment = VZNATNetworkDeviceAttachment()
        device.macAddress = try appleVFNetworkMACAddress(from: config)
        return [device]
    case "isolated":
        return []
    default:
        throw ProtocolError.invalid("network.mode must be user or isolated")
    }
}

@available(macOS 13.0, *)
func appleVFNetworkMACAddress(from config: Config) throws -> VZMACAddress {
    let value = (config.appleVFNetworkMACAddress ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    if value.isEmpty {
        return VZMACAddress.randomLocallyAdministered()
    }
    guard let address = VZMACAddress(string: value) else {
        throw ProtocolError.invalid("appleVFNetworkMACAddress is not a valid MAC address")
    }
    return address
}

func configureRawTerminal(_ fileHandle: FileHandle) {
    guard isatty(fileHandle.fileDescriptor) == 1 else {
        return
    }
    var attributes = termios()
    if tcgetattr(fileHandle.fileDescriptor, &attributes) == 0 {
        attributes.c_iflag &= ~tcflag_t(ICRNL)
        attributes.c_lflag &= ~tcflag_t(ICANON | ECHO)
        tcsetattr(fileHandle.fileDescriptor, TCSANOW, &attributes)
    }
}

func prepareSerialInput(path: String) throws {
    var info = stat()
    if lstat(path, &info) == 0 {
        if (info.st_mode & S_IFMT) == S_IFIFO {
            return
        }
        try FileManager.default.removeItem(atPath: path)
    } else if errno != ENOENT {
        throw ProtocolError.invalid("inspect serial input failed with errno \(errno)")
    }
    if mkfifo(path, S_IRUSR | S_IWUSR) != 0 && errno != EEXIST {
        throw ProtocolError.invalid("create serial input failed with errno \(errno)")
    }
}

func bridgeSerialInput(path: String, to output: FileHandle) {
    DispatchQueue.global(qos: .utility).async {
        var buffer = [UInt8](repeating: 0, count: 4096)
        let readFD = open(path, O_RDONLY)
        if readFD < 0 {
            output.closeFile()
            return
        }
        let keepaliveFD = open(path, O_WRONLY | O_NONBLOCK)
        defer {
            close(readFD)
            if keepaliveFD >= 0 {
                close(keepaliveFD)
            }
            output.closeFile()
        }
        while true {
            let n = read(readFD, &buffer, buffer.count)
            if n > 0 {
                if !FileManager.default.fileExists(atPath: path) {
                    return
                }
                output.write(Data(buffer.prefix(n)))
                continue
            }
            if n == 0 {
                return
            }
            if errno == EINTR {
                usleep(10_000)
                continue
            }
            return
        }
    }
}
#endif

func write(_ response: Response) {
    do {
        FileHandle.standardOutput.write(try encoder.encode(response))
        FileHandle.standardOutput.write(Data("\n".utf8))
    } catch {
        FileHandle.standardError.write(Data("\(error)\n".utf8))
    }
}

func hostArchitecture() -> String {
    #if canImport(Darwin)
    var uts = utsname()
    uname(&uts)
    return withUnsafePointer(to: &uts.machine) {
        $0.withMemoryRebound(to: CChar.self, capacity: 1) {
            String(cString: $0)
        }
    }
    #else
    return "unknown"
    #endif
}

enum ProtocolError: Error, CustomStringConvertible {
    case invalid(String)

    var description: String {
        switch self {
        case .invalid(let message):
            return message
        }
    }
}

let runningUnderXCTest = CommandLine.arguments.first?.hasSuffix("xctest") == true ||
    ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil
if !runningUnderXCTest {
    exit(main())
}
