import Foundation

// mediationReadinessSignal mirrors pkg/vmkit.MediationReadinessSignal: the
// runtime contract defines mediationReady as "declared mediation channel
// target is live reachable for a running workspace", so a running VM state
// alone never makes the signal ready — the declared target must accept a TCP
// connection at observation time.
func mediationReadinessSignal(
    mediation: MediationConfig,
    state: VMState,
    observedAt: Date,
    probeTimeoutMillis: Int32 = 150
) -> ReadinessSignal {
    let detail = "mediation required=\(mediation.required) failClosed=\(mediation.failClosed) port=\(mediation.port ?? 0) target=\(mediation.target ?? "")"
    if state != .running {
        return ReadinessSignal(
            ready: false,
            observedAt: observedAt,
            detail: detail + "; workspace is not running",
            error: mediation.required ? "required mediation is not ready" : nil
        )
    }
    let observed = Date()
    guard let target = try? parseTCPHostPort(mediation.target ?? "") else {
        return ReadinessSignal(
            ready: false,
            observedAt: observed,
            detail: detail + "; mediation target is not tcp host:port",
            error: mediation.required ? "required mediation target is invalid" : nil
        )
    }
    let start = DispatchTime.now()
    let probeError = probeTCPReachability(target: target, timeoutMillis: probeTimeoutMillis)
    let elapsedMillis = (DispatchTime.now().uptimeNanoseconds - start.uptimeNanoseconds) / 1_000_000
    if let probeError {
        return ReadinessSignal(
            ready: false,
            observedAt: observed,
            detail: "\(detail); mediation target unreachable at \(target.host):\(target.port) after \(elapsedMillis)ms: \(probeError)",
            error: mediation.required ? "required mediation target is unreachable" : nil
        )
    }
    return ReadinessSignal(
        ready: true,
        observedAt: observed,
        detail: "\(detail); mediation target reachable at \(target.host):\(target.port) in \(elapsedMillis)ms"
    )
}

// probeTCPReachability returns nil when a TCP connection to target completes
// within timeoutMillis, and a short failure description otherwise. The socket
// is non-blocking so a blackholed target costs at most the timeout, never the
// kernel's multi-second connect default.
func probeTCPReachability(target: TCPHostPort, timeoutMillis: Int32) -> String? {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 {
        return "socket failed with errno \(errno)"
    }
    defer { close(fd) }
    var addr = sockaddr_in()
    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = target.port.bigEndian
    let host = target.host == "localhost" ? "127.0.0.1" : target.host
    guard inet_pton(AF_INET, host, &addr.sin_addr) == 1 else {
        return "host \(target.host) is not an IPv4 address or localhost"
    }
    let flags = fcntl(fd, F_GETFL, 0)
    _ = fcntl(fd, F_SETFL, flags | O_NONBLOCK)
    let result = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    if result == 0 {
        return nil
    }
    if errno != EINPROGRESS && errno != EINTR {
        return "connect failed: \(errnoDescription(errno))"
    }
    var pfd = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
    var remaining = timeoutMillis
    while true {
        let started = DispatchTime.now()
        let pollResult = poll(&pfd, 1, remaining)
        if pollResult < 0 && errno == EINTR {
            let spent = Int32((DispatchTime.now().uptimeNanoseconds - started.uptimeNanoseconds) / 1_000_000)
            remaining = max(0, remaining - spent)
            if remaining == 0 {
                return "connect timed out after \(timeoutMillis)ms"
            }
            continue
        }
        if pollResult == 0 {
            return "connect timed out after \(timeoutMillis)ms"
        }
        if pollResult < 0 {
            return "poll failed with errno \(errno)"
        }
        break
    }
    var connectError: Int32 = 0
    var len = socklen_t(MemoryLayout<Int32>.size)
    if getsockopt(fd, SOL_SOCKET, SO_ERROR, &connectError, &len) != 0 {
        return "getsockopt failed: \(errnoDescription(errno))"
    }
    if connectError != 0 {
        return "connect failed: \(errnoDescription(connectError))"
    }
    return nil
}

private func errnoDescription(_ code: Int32) -> String {
    if let text = strerror(code) {
        return "\(String(cString: text)) (errno \(code))"
    }
    return "errno \(code)"
}
