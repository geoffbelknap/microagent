import Foundation

// Shared constants and codecs for the supervisor.
//
// These MUST live outside main.swift. Top-level declarations in main.swift are
// locals of the implicit main function, initialized only when the program runs
// top-level code — which the runningUnderXCTest guard skips. Any test that
// reached them (e.g. RuntimeControlTests via writeState) read uninitialized
// memory and crashed with SIGSEGV. In any other file they are ordinary global
// constants with lazy, thread-safe (swift_once) initialization, safe from both
// the executable and the test bundle.

let backendName = "apple-vf"
let eventFileName = "event.json"
let eventsFileName = "events.json"
let configFileName = "config.json"
let runtimeFileName = "runtime.json"
let serialLogFileName = "serial.log"
let serialInputFileName = "serial.in"
let supervisorLogFileName = "supervisor.log"
let datapathDiagnosticsFileName = "datapath.log"
let datapathStartupFileName = "datapath-startup.json"
let quarantineAckFileName = "quarantine.ack.json"
let applyRequestFileName = "apply.request.json"
let applyAckFileName = "apply.ack.json"
let runtimeControlRequestFileName = "runtime-control.request.json"
let runtimeControlAckFileName = "runtime-control.ack.json"
let snapshotRootfsFileName = "rootfs.ext4"
let snapshotMachineStateFileName = "machine-state.vz"
let quarantineControlSignal = SIGUSR1
let applyControlSignal = SIGUSR2
let runtimeControlSignal = SIGHUP
let maxSocketConnections = 128
let maxResultSocketBytes = 16 * 1024 * 1024
let secretsListenerTarget = "secrets://serve"
let caCertListenerTarget = "cacert://serve"
let secretsProtocolVersion = "secrets.v1"
let maxCACertBytes = 1 * 1024 * 1024
let maxDatapathDiagnosticBytes = 64 * 1024
let datapathStartupTimeout: TimeInterval = 5.0
let maxSecretsMessageBytes = 8 * 1024 * 1024

let decoder: JSONDecoder = {
    let decoder = JSONDecoder()
    decoder.dateDecodingStrategy = .iso8601
    return decoder
}()

let encoder: JSONEncoder = {
    let encoder = JSONEncoder()
    encoder.dateEncodingStrategy = .iso8601
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    return encoder
}()
