import Foundation
import MicroAgentVMKit

let command = CommandLine.arguments.dropFirst().first ?? "help"

switch command {
case "version":
    print("microagent-vmkit 0.1.0")
case "help", "--help", "-h":
    print("""
    microagent-vmkit

    Commands:
      version   Print version information
      help      Show this help
    """)
default:
    fputs("unknown command: \(command)\n", stderr)
    exit(2)
}
