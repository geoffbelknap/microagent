import Foundation
import MicroAgentVMKit

struct ValidationInput: Decodable {
    var identity: RuntimeIdentity
    var config: VMConfig
}

let arguments = Array(CommandLine.arguments.dropFirst())
let command = arguments.first ?? "help"

switch command {
case "apple-vf-host-check":
    do {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        try FileHandle.standardOutput.write(contentsOf: encoder.encode(AppleVirtualizationHost.support()))
        print()
    } catch {
        fputs("\(error)\n", stderr)
        exit(1)
    }
case "version":
    print("microagent-vmkit 0.1.0")
case "validate-config":
    guard arguments.count == 2 else {
        fputs("usage: microagent-vmkit validate-config <request.json>\n", stderr)
        exit(2)
    }
    do {
        let url = URL(fileURLWithPath: arguments[1])
        let data = try Data(contentsOf: url)
        let request = try JSONDecoder().decode(ValidationInput.self, from: data)
        try validateRuntimeConfig(request.config)
        let event = RuntimeEvent(identity: request.identity, state: .prepared, detail: "validated")
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        encoder.dateEncodingStrategy = .iso8601
        try FileHandle.standardOutput.write(contentsOf: encoder.encode(event))
        print()
    } catch {
        fputs("\(error)\n", stderr)
        exit(1)
    }
case "help", "--help", "-h":
    print("""
    microagent-vmkit

    Commands:
      apple-vf-host-check           Print Apple Virtualization host support
      validate-config <request.json>  Validate a VM lifecycle request
      version                       Print version information
      help                          Show this help
    """)
default:
    fputs("unknown command: \(command)\n", stderr)
    exit(2)
}
