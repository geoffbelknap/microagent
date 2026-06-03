//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const configPath = "/etc/microagent/run.json"
const resultConnectTimeout = 15 * time.Second
const tcpVsockListenersEnv = "MICROAGENT_VSOCK_TCP_LISTENERS"
const consoleShellExitedMarker = "microagent-init: console shell exited; closing connect session"
const defaultGuestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type config struct {
	Command            []string      `json:"command"`
	Env                []string      `json:"env,omitempty"`
	Port               uint32        `json:"port"`
	Mode               string        `json:"mode,omitempty"`
	Mounts             []mount       `json:"mounts,omitempty"`
	HostForwards       []hostForward `json:"hostForwards,omitempty"`
	ShellPort          uint16        `json:"shellPort,omitempty"`
	ExecPort           uint16        `json:"execPort,omitempty"`
	SecretsPort        uint16        `json:"secretsPort,omitempty"`
	SecretsAPI         bool          `json:"secretsApi,omitempty"`
	SecretsControlPort uint16        `json:"secretsControlPort,omitempty"`
	ModelGuestPort     uint16        `json:"modelGuestPort,omitempty"`
	ModelVsockPort     uint32        `json:"modelVsockPort,omitempty"`
	ConsoleShell       string        `json:"consoleShell,omitempty"`
	Hostname           string        `json:"hostname,omitempty"`
}

type mount struct {
	Device     string `json:"device"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
}

type result struct {
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "host-forward-helper" {
		os.Exit(runHostForwardHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "shell-helper" {
		os.Exit(runShellHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "exec-service" {
		os.Exit(runExecService(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "model-forward-helper" {
		os.Exit(runModelForwardHelper(os.Args[2:]))
	}
	code := run()
	poweroff()
	os.Exit(code)
}

func run() int {
	log.Println("microagent-init: starting")
	if err := mountGuestFilesystems(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	if err := ensureGuestDevices(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	cfg, err := readConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	if err := applyKernelConfigOverrides(&cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	if cfg.SecretsPort != 0 {
		if err := fetchAndWriteSecrets(cfg.SecretsPort); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
	}
	if cfg.SecretsAPI && cfg.SecretsPort != 0 {
		if err := serveSecretsAPI(secretsAPISock, cfg.SecretsPort); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
		cfg.Env = append(cfg.Env, "MICROAGENT_SECRETS_SOCK="+secretsAPISock)
	}
	if cfg.SecretsControlPort != 0 && cfg.SecretsPort != 0 {
		if err := serveSecretsControl(cfg.SecretsControlPort, cfg.SecretsPort); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 127
		}
	}
	res := result{StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	code := 0
	if err := mountDisks(cfg.Mounts); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := configureBootNetwork(); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := configureKernelDHCPNetwork(); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := configureKernelDHCPDNS(); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := configureHostname(cfg.Hostname); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := startTCPVsockBridges(cfg.Env); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := startConfiguredHostForwards(cfg); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if cfg.ModelGuestPort != 0 && cfg.ModelVsockPort != 0 {
		if err := startModelForwarder(cfg.ModelGuestPort, cfg.ModelVsockPort); err != nil {
			fmt.Fprintf(os.Stderr, "model forwarder: %v\n", err)
		}
	}
	if err := startShellHelper(cfg.ShellPort, cfg.ConsoleShell, guestEnv(cfg.Env)); err != nil {
		code = 127
		res.Error = err.Error()
		fmt.Fprintln(os.Stderr, err)
		res.ExitCode = code
		res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = sendResult(cfg.Port, res)
		return code
	}
	if err := startStructuredExecService(cfg.ExecPort, guestEnv(cfg.Env)); err != nil {
		log.Printf("microagent-init: structured exec service failed to start on vsock port %d: %v", cfg.ExecPort, err)
	}
	if cfg.Mode == "service" && len(cfg.Command) > 0 {
		if err := attachConsole(); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
			res.ExitCode = code
			res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = sendResult(cfg.Port, res)
			return code
		}
		if err := execServiceCommand(cfg.Command, guestEnv(cfg.Env)); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
		}
	} else if cfg.Mode == "managed-service" && len(cfg.Command) > 0 {
		if err := attachConsole(); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
			res.ExitCode = code
			res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = sendResult(cfg.Port, res)
			return code
		}
		if err := runManagedServiceCommand(cfg.Command, guestEnv(cfg.Env)); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
		}
	} else if len(cfg.Command) > 0 {
		env := guestEnv(cfg.Env)
		command, err := resolveGuestCommand(cfg.Command, env)
		if err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
			res.ExitCode = code
			res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = sendResult(cfg.Port, res)
			return code
		}
		log.Printf("microagent-init: handing off to %v", command)
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdout)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
		err = cmd.Run()
		res.Stdout = stdout.String()
		res.Stderr = stderr.String()
		if err != nil {
			code = exitCode(err)
			res.Error = err.Error()
		}
	} else {
		if err := attachConsole(); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
			res.ExitCode = code
			res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = sendResult(cfg.Port, res)
			return code
		}
		if err := runInteractiveShells(guestEnv(cfg.Env), cfg.ConsoleShell); err != nil {
			code = 127
			res.Error = err.Error()
			fmt.Fprintln(os.Stderr, err)
		}
	}
	res.ExitCode = code
	res.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := sendResult(cfg.Port, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return code
}

func execServiceCommand(command []string, env []string) error {
	command, err := resolveGuestCommand(command, env)
	if err != nil {
		return err
	}
	log.Printf("microagent-init: exec service command %v", command)
	markExtraFilesCloseOnExec()
	return syscall.Exec(command[0], command, env)
}

func resolveGuestCommand(command []string, env []string) ([]string, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("guest command is empty")
	}
	if strings.Contains(command[0], "/") {
		return command, nil
	}
	searchPath := envValue(env, "PATH")
	if searchPath == "" {
		searchPath = defaultGuestPath
	}
	for _, dir := range filepath.SplitList(searchPath) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, command[0])
		if err := unix.Access(candidate, unix.X_OK); err == nil {
			resolved := append([]string{}, command...)
			resolved[0] = candidate
			return resolved, nil
		}
	}
	return nil, fmt.Errorf("resolve guest command %q in PATH: %w", command[0], exec.ErrNotFound)
}

func runManagedServiceCommand(command []string, env []string) error {
	backoff := time.Second
	for {
		reapExitedChildren()
		log.Printf("microagent-init: starting managed service command %v", command)
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Env = env
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		startedAt := time.Now()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start managed service command: %w", err)
		}
		if err := cmd.Wait(); err != nil {
			log.Printf("microagent-init: managed service command exited: %v", err)
		} else {
			log.Println("microagent-init: managed service command exited")
		}
		reapExitedChildren()
		if time.Since(startedAt) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("microagent-init: restarting managed service command in %s", backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func reapExitedChildren() {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err == nil && pid > 0 {
			log.Printf("microagent-init: reaped child pid %d", pid)
			continue
		}
		if err == nil {
			return
		}
		if err != unix.ECHILD {
			log.Printf("microagent-init: reap child: %v", err)
		}
		return
	}
}

func startInteractiveShell(env []string, shellPath string) error {
	command, err := consoleShellCommand(shellPath)
	if err != nil {
		return err
	}
	log.Printf("microagent-init: starting console shell %v", command)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start console shell: %w", err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("microagent-init: console shell exited: %v", err)
			return
		}
		log.Println(consoleShellExitedMarker)
	}()
	return nil
}

func configureHostname(hostname string) error {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return nil
	}
	if err := validateHostname(hostname); err != nil {
		return err
	}
	if err := unix.Sethostname([]byte(hostname)); err != nil {
		return fmt.Errorf("set hostname %s: %w", hostname, err)
	}
	if err := os.WriteFile("/etc/hostname", []byte(hostname+"\n"), 0o644); err != nil {
		if errors.Is(err, syscall.EROFS) {
			log.Printf("microagent-init: /etc/hostname is read-only; kernel hostname set to %s", hostname)
			return nil
		}
		return fmt.Errorf("write /etc/hostname: %w", err)
	}
	log.Printf("microagent-init: hostname set to %s", hostname)
	return nil
}

func validateHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	if len(hostname) > 63 {
		return fmt.Errorf("hostname must be 63 characters or fewer")
	}
	if hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return fmt.Errorf("hostname must not start or end with '-'")
	}
	for _, r := range hostname {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return fmt.Errorf("hostname must contain only letters, numbers, and '-'")
	}
	return nil
}

func runInteractiveShells(env []string, shellPath string) error {
	command, err := consoleShellCommand(shellPath)
	if err != nil {
		return err
	}
	for {
		log.Printf("microagent-init: starting console shell %v", command)
		err := runInteractiveShell(env, command)
		if shellLaunchFailed(err) {
			return err
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		log.Println(consoleShellExitedMarker)
		time.Sleep(250 * time.Millisecond)
	}
}

func consoleShellCommand(shellPath string) ([]string, error) {
	shellPath = strings.TrimSpace(shellPath)
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	if !strings.HasPrefix(shellPath, "/") {
		return nil, fmt.Errorf("console shell must be an absolute path: %q", shellPath)
	}
	if info, err := os.Stat(shellPath); err != nil {
		return nil, fmt.Errorf("console shell %s is unavailable: %w", shellPath, err)
	} else if info.IsDir() {
		return nil, fmt.Errorf("console shell %s is a directory", shellPath)
	} else if info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("console shell %s is not executable", shellPath)
	}
	return []string{shellPath, "-i"}, nil
}

func runInteractiveShell(env []string, command []string) error {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func shellLaunchFailed(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}

type guestFilesystem struct {
	Source string
	Target string
	FSType string
	Flags  uintptr
}

var guestFilesystems = []guestFilesystem{
	{Source: "proc", Target: "/proc", FSType: "proc", Flags: unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
	{Source: "sysfs", Target: "/sys", FSType: "sysfs", Flags: unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
	{Source: "devpts", Target: "/dev/pts", FSType: "devpts", Flags: unix.MS_NOSUID | unix.MS_NOEXEC},
}

var mountGuestFilesystem = unix.Mount
var mountDiskFilesystem = unix.Mount
var mknodGuestDevice = unix.Mknod

func mountGuestFilesystems() error {
	for _, fs := range guestFilesystems {
		if err := os.MkdirAll(fs.Target, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", fs.Target, err)
		}
		if err := mountGuestFilesystem(fs.Source, fs.Target, fs.FSType, fs.Flags, ""); err != nil && !errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("mount %s: %w", fs.Target, err)
		}
		log.Printf("microagent-init: mounted %s", fs.Target)
	}
	return nil
}

func ensureGuestDevices() error {
	if err := ensureGuestCharDevice("/dev/null", 1, 3, 0o666); err != nil {
		return err
	}
	return nil
}

func ensureGuestCharDevice(path string, major, minor uint32, perm uint32) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return fmt.Errorf("create device directory for %s: %w", path, err)
	}
	dev := int(unix.Mkdev(major, minor))
	if err := mknodGuestDevice(path, syscall.S_IFCHR|perm, dev); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

func filepathDir(path string) string {
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		return path[:idx]
	}
	return "."
}

func mountDisks(mounts []mount) error {
	for _, mount := range mounts {
		if mount.Device == "" || mount.Mountpoint == "" {
			return fmt.Errorf("mount device and mountpoint are required")
		}
		if err := os.MkdirAll(mount.Mountpoint, 0o755); err != nil {
			return fmt.Errorf("create mountpoint %s: %w", mount.Mountpoint, err)
		}
		flags := uintptr(0)
		data := ""
		if mount.Mode == "ro" {
			flags = unix.MS_RDONLY
			data = "noload"
		} else if mount.Mode != "rw" {
			return fmt.Errorf("mount %s mode must be ro or rw", mount.Mountpoint)
		}
		if err := mountDiskFilesystem(mount.Device, mount.Mountpoint, "ext4", flags, data); err != nil {
			return fmt.Errorf("mount %s at %s: %w", mount.Device, mount.Mountpoint, err)
		}
	}
	return nil
}

func configureKernelDHCPNetwork() error {
	if err := ensureProcMounted(); err != nil {
		return err
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil || !cmdlineRequestsDHCP(string(cmdline)) {
		return nil
	}
	if _, ok := defaultGatewayNameserver("/proc/net/route"); ok {
		return nil
	}
	iface := firstGuestNetworkInterface()
	if iface == "" {
		log.Println("microagent-init: DHCP requested but no guest network interface is present")
		return nil
	}
	if err := setInterfaceUp(iface); err != nil {
		return fmt.Errorf("bring up DHCP interface %s: %w", iface, err)
	}
	udhcpc, err := findGuestExecutable("udhcpc")
	if err != nil {
		return fmt.Errorf("DHCP requested but udhcpc is not available: %w", err)
	}
	script := "/tmp/microagent-udhcpc.script"
	if err := os.WriteFile(script, []byte(udhcpcScript), 0o755); err != nil {
		return fmt.Errorf("write udhcpc script: %w", err)
	}
	out, err := exec.Command(udhcpc, "-i", iface, "-n", "-q", "-t", "5", "-T", "1", "-s", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("run udhcpc on %s: %w: %s", iface, err, strings.TrimSpace(string(out)))
	}
	log.Printf("microagent-init: DHCP configured %s", iface)
	return nil
}

const udhcpcScript = `#!/bin/sh
case "$1" in
  bound|renew)
    if [ -n "$ip" ] && [ -n "$subnet" ]; then
      ifconfig "$interface" "$ip" netmask "$subnet" up
    else
      ifconfig "$interface" up
    fi
    if [ -n "$router" ]; then
      for r in $router; do
        route del default 2>/dev/null || true
        route add default gw "$r" "$interface"
        break
      done
    fi
    if [ -n "$dns" ]; then
      mkdir -p /etc
      : >/etc/resolv.conf
      for d in $dns; do
        echo "nameserver $d" >>/etc/resolv.conf
      done
    fi
    ;;
esac
exit 0
`

func configureKernelDHCPDNS() error {
	if err := ensureProcMounted(); err != nil {
		return err
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil || !cmdlineRequestsDHCP(string(cmdline)) {
		return nil
	}
	allowGatewayFallback := cmdlineAllowsGatewayDNSFallback(string(cmdline))
	nameservers := cmdlineDNSNameservers(string(cmdline))
	deadline := time.Now().Add(10 * time.Second)
	for len(nameservers) == 0 {
		pnp, err := os.ReadFile("/proc/net/pnp")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read kernel DHCP nameservers: %w", err)
		}
		if err == nil {
			nameservers = parseKernelPNPNameservers(string(pnp))
		}
		if len(nameservers) == 0 && allowGatewayFallback {
			if nameserver, ok := defaultGatewayNameserver("/proc/net/route"); ok {
				nameservers = append(nameservers, nameserver)
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(nameservers) == 0 {
		return nil
	}
	var resolv strings.Builder
	resolv.WriteString("# written by microagent-init from kernel DHCP\n")
	for _, nameserver := range nameservers {
		resolv.WriteString("nameserver ")
		resolv.WriteString(nameserver)
		resolv.WriteByte('\n')
	}
	if err := os.MkdirAll("/etc", 0o755); err != nil {
		return fmt.Errorf("create /etc for resolver config: %w", err)
	}
	if err := os.WriteFile("/etc/resolv.conf", []byte(resolv.String()), 0o644); err != nil {
		return fmt.Errorf("write /etc/resolv.conf from kernel DHCP: %w", err)
	}
	return nil
}

func ensureProcMounted() error {
	if _, err := os.Stat("/proc/cmdline"); err == nil {
		return nil
	}
	if err := os.MkdirAll("/proc", 0o555); err != nil {
		return fmt.Errorf("create /proc: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil && err != unix.EBUSY {
		return fmt.Errorf("mount /proc: %w", err)
	}
	return nil
}

func cmdlineRequestsDHCP(cmdline string) bool {
	for _, field := range strings.Fields(cmdline) {
		if field == "ip=dhcp" || field == "ip=on" || field == "ip=any" {
			return true
		}
	}
	return false
}

func cmdlineAllowsGatewayDNSFallback(cmdline string) bool {
	values := microagentCmdlineValues(cmdline)
	return values["microagent_dns_fallback_gateway"] == "1"
}

func cmdlineDNSNameservers(cmdline string) []string {
	values := microagentCmdlineValues(cmdline)
	raw := strings.TrimSpace(values["microagent_dns"])
	if raw == "" {
		return nil
	}
	var nameservers []string
	seen := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		ip := strings.TrimSpace(item)
		if net.ParseIP(ip) == nil || seen[ip] {
			continue
		}
		seen[ip] = true
		nameservers = append(nameservers, ip)
	}
	return nameservers
}

func parseKernelPNPNameservers(raw string) []string {
	var nameservers []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "#"))
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip := strings.TrimSpace(fields[1])
		if net.ParseIP(ip) == nil || seen[ip] {
			continue
		}
		seen[ip] = true
		nameservers = append(nameservers, ip)
	}
	return nameservers
}

func defaultGatewayNameserver(routePath string) (string, bool) {
	data, err := os.ReadFile(routePath)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "Iface" || fields[1] != "00000000" {
			continue
		}
		ip := littleEndianHexIPv4(fields[2])
		if ip == "" || ip == "0.0.0.0" {
			continue
		}
		return ip, true
	}
	return "", false
}

func littleEndianHexIPv4(raw string) string {
	if len(raw) != 8 {
		return ""
	}
	bytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		value, err := strconv.ParseUint(raw[i*2:i*2+2], 16, 8)
		if err != nil {
			return ""
		}
		bytes[3-i] = byte(value)
	}
	return net.IPv4(bytes[0], bytes[1], bytes[2], bytes[3]).String()
}

func guestEnv(extra []string) []string {
	env := os.Environ()
	for _, entry := range extra {
		if entry != "" {
			env = append(env, entry)
		}
	}
	return env
}

type tcpVsockBridge struct {
	Listen string
	Port   uint32
}

type hostForward struct {
	Protocol  string `json:"protocol"`
	HostPort  uint16 `json:"hostPort"`
	GuestPort uint16 `json:"guestPort"`
}

type networkBootConfig struct {
	Interface string
	IP        string
	Gateway   string
	DNS       []string
	Hosts     []hostEntry
}

type hostEntry struct {
	Name string
	IP   string
}

func startTCPVsockBridges(env []string) error {
	specs, err := parseTCPVsockBridges(envValue(env, tcpVsockListenersEnv))
	if err != nil {
		return err
	}
	if len(specs) > 0 {
		if err := bringUpLoopback(); err != nil {
			return err
		}
	}
	for _, spec := range specs {
		listener, err := net.Listen("tcp", spec.Listen)
		if err != nil {
			return fmt.Errorf("listen %s for vsock bridge: %w", spec.Listen, err)
		}
		go serveTCPVsockBridge(listener, spec.Port)
	}
	return nil
}

func startHostForwards(forwards []hostForward) error {
	if len(forwards) > 0 {
		if err := bringUpLoopback(); err != nil {
			return err
		}
	}
	for _, forward := range forwards {
		fd, err := openHostForwardListener(forward)
		if err != nil {
			return err
		}
		go serveHostForward(fd, forward.GuestPort)
	}
	return nil
}

func startConfiguredHostForwards(cfg config) error {
	if cfg.Mode == "service" && len(cfg.Command) > 0 {
		return startHostForwardHelpers(cfg.HostForwards)
	}
	return startHostForwards(cfg.HostForwards)
}

func startShellHelper(port uint16, shellPath string, env []string) error {
	if port == 0 {
		return nil
	}
	cmd := exec.Command(os.Args[0], "shell-helper", strconv.Itoa(int(port)), shellPath)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start shell helper on port %d: %w", port, err)
	}
	return nil
}

func runShellHelper(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: microagent-init shell-helper <port> [shell]")
		return 127
	}
	port, err := parseUint16(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse shell port: %v\n", err)
		return 127
	}
	shellPath := ""
	if len(args) == 2 {
		shellPath = args[1]
	}
	fd, err := openShellListener(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	serveShellSessions(fd, shellPath)
	return 0
}

func openShellListener(port uint16) (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("open vsock shell listener for port %d: %w", port, err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: uint32(port)}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("bind vsock shell listener for port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 8); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("listen on vsock shell port %d: %w", port, err)
	}
	log.Printf("microagent-init: shell helper listening on vsock port %d", port)
	return fd, nil
}

func serveShellSessions(fd int, shellPath string) {
	for {
		connFD, _, err := unix.Accept(fd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept shell session: %v\n", err)
			_ = unix.Close(fd)
			return
		}
		go runShellSession(connFD, shellPath)
	}
}

func runShellSession(fd int, shellPath string) {
	file := os.NewFile(uintptr(fd), "shell-session-vsock")
	if file == nil {
		_ = unix.Close(fd)
		return
	}
	defer file.Close()
	command, err := consoleShellCommand(shellPath)
	if err != nil {
		fmt.Fprintf(file, "microagent-init: %v\n", err)
		return
	}
	master, slave, err := openPTY()
	if err != nil {
		fmt.Fprintf(file, "microagent-init: open shell pty: %v\n", err)
		return
	}
	defer master.Close()
	defer slave.Close()
	log.Printf("microagent-init: starting connect shell %v", command)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = shellSessionEnv()
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(file, "microagent-init: start connect shell: %v\n", err)
		return
	}
	_ = slave.Close()
	inputDone := make(chan struct{}, 1)
	outputDone := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(master, file)
		_ = master.Close()
		inputDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(file, master)
		closeWriteFile(file)
		outputDone <- struct{}{}
	}()
	err = cmd.Wait()
	select {
	case <-outputDone:
	case <-time.After(2 * time.Second):
		_ = master.Close()
	}
	_ = master.Close()
	_ = file.Close()
	select {
	case <-inputDone:
	default:
	}
	if err != nil {
		log.Printf("microagent-init: connect shell exited: %v", err)
		return
	}
	log.Println("microagent-init: connect shell exited")
}

func openPTY() (*os.File, *os.File, error) {
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	unlock := 0
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, unlock); err != nil {
		_ = unix.Close(masterFD)
		return nil, nil, err
	}
	ptyNumber, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(masterFD)
		return nil, nil, err
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", ptyNumber)
	slaveFD, err := unix.Open(slavePath, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(masterFD)
		return nil, nil, err
	}
	master := os.NewFile(uintptr(masterFD), "connect-pty-master")
	slave := os.NewFile(uintptr(slaveFD), "connect-pty-slave")
	if master == nil || slave == nil {
		if master != nil {
			_ = master.Close()
		} else {
			_ = unix.Close(masterFD)
		}
		if slave != nil {
			_ = slave.Close()
		} else {
			_ = unix.Close(slaveFD)
		}
		return nil, nil, fmt.Errorf("wrap pty files")
	}
	_ = unix.IoctlSetWinsize(masterFD, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})
	return master, slave, nil
}

func shellSessionEnv() []string {
	env := os.Environ()
	for _, item := range env {
		if strings.HasPrefix(item, "TERM=") {
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}

func startHostForwardHelpers(forwards []hostForward) error {
	if len(forwards) > 0 {
		if err := bringUpLoopback(); err != nil {
			return err
		}
	}
	for _, forward := range forwards {
		if err := validateHostForward(forward); err != nil {
			return err
		}
		cmd := exec.Command(os.Args[0], "host-forward-helper", strconv.Itoa(int(forward.HostPort)), strconv.Itoa(int(forward.GuestPort)), nonEmpty(forward.Protocol, "tcp"))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start host forward helper for host port %d: %w", forward.HostPort, err)
		}
	}
	return nil
}

func runHostForwardHelper(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: microagent-init host-forward-helper <host-port> <guest-port> <protocol>")
		return 127
	}
	hostPort, err := parseUint16(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse host port: %v\n", err)
		return 127
	}
	guestPort, err := parseUint16(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse guest port: %v\n", err)
		return 127
	}
	forward := hostForward{Protocol: args[2], HostPort: hostPort, GuestPort: guestPort}
	fd, err := openHostForwardListener(forward)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	serveHostForward(fd, forward.GuestPort)
	return 0
}

// startModelForwarder spawns a model-forward-helper subprocess that listens on
// 127.0.0.1:guestPort and tunnels each accepted connection to the host model
// server over host vsock vsockPort. Using a subprocess (rather than a goroutine)
// ensures the forwarder survives modes where run() hands off via syscall.Exec
// (e.g. "service" mode), which would kill any goroutines.
func startModelForwarder(guestPort uint16, vsockPort uint32) error {
	if err := bringUpLoopback(); err != nil {
		return err
	}
	cmd := exec.Command(os.Args[0], "model-forward-helper", strconv.Itoa(int(guestPort)), strconv.Itoa(int(vsockPort)))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start model forward helper on guest port %d: %w", guestPort, err)
	}
	return nil
}

// runModelForwardHelper is the blocking accept loop run by the model-forward-helper
// subprocess. It listens on 127.0.0.1:guestPort and proxies each connection to
// the host vsock at vsockPort.
func runModelForwardHelper(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: microagent-init model-forward-helper <guestPort> <vsockPort>")
		return 127
	}
	guestPort, err := parseUint16(args[0])
	if err != nil || guestPort == 0 {
		fmt.Fprintf(os.Stderr, "parse guest port: %v\n", err)
		return 127
	}
	vsockPort64, err := strconv.ParseUint(strings.TrimSpace(args[1]), 10, 32)
	if err != nil || vsockPort64 == 0 {
		fmt.Fprintf(os.Stderr, "parse vsock port: %v\n", err)
		return 127
	}
	vsockPort := uint32(vsockPort64)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", guestPort))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen model forwarder on 127.0.0.1:%d: %v\n", guestPort, err)
		return 127
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 0
		}
		go proxyTCPToHostVsock(conn, vsockPort)
	}
}

func openHostForwardListener(forward hostForward) (int, error) {
	if err := validateHostForward(forward); err != nil {
		return -1, err
	}
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("open vsock listener for host port %d: %w", forward.HostPort, err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: uint32(forward.HostPort)}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("bind vsock listener for host port %d: %w", forward.HostPort, err)
	}
	if err := unix.Listen(fd, 128); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("listen on vsock host port %d: %w", forward.HostPort, err)
	}
	return fd, nil
}

func validateHostForward(forward hostForward) error {
	if forward.Protocol != "" && forward.Protocol != "tcp" {
		return fmt.Errorf("host forward protocol must be tcp")
	}
	if forward.HostPort == 0 || forward.GuestPort == 0 {
		return fmt.Errorf("host forward ports must be positive")
	}
	return nil
}

func parseUint16(raw string) (uint16, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 16)
	return uint16(value), err
}

func applyKernelConfigOverrides(cfg *config) error {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("read kernel command line: %w", err)
	}
	return applyKernelConfigOverridesFromCmdline(cfg, string(data))
}

func applyKernelConfigOverridesFromCmdline(cfg *config, cmdline string) error {
	values := microagentCmdlineValues(cmdline)
	if raw := values["microagent_shell_port"]; strings.TrimSpace(raw) != "" {
		port, err := parseUint16(raw)
		if err != nil || port == 0 {
			return fmt.Errorf("microagent_shell_port must be a positive uint16")
		}
		cfg.ShellPort = port
	}
	if raw := values["microagent_exec_port"]; strings.TrimSpace(raw) != "" {
		port, err := parseUint16(raw)
		if err != nil || port == 0 {
			return fmt.Errorf("microagent_exec_port must be a positive uint16")
		}
		cfg.ExecPort = port
	}
	if raw := values["microagent_secrets_port"]; strings.TrimSpace(raw) != "" {
		port, err := parseUint16(raw)
		if err != nil || port == 0 {
			return fmt.Errorf("microagent_secrets_port must be a positive uint16")
		}
		cfg.SecretsPort = port
	}
	if values["microagent_secrets_api"] == "1" {
		cfg.SecretsAPI = true
	}
	if raw := values["microagent_secrets_ctl_port"]; strings.TrimSpace(raw) != "" {
		port, err := parseUint16(raw)
		if err != nil || port == 0 {
			return fmt.Errorf("microagent_secrets_ctl_port must be a positive uint16")
		}
		cfg.SecretsControlPort = port
	}
	if raw := values["microagent_model_fwd"]; strings.TrimSpace(raw) != "" {
		guestRaw, vsockRaw, ok := strings.Cut(raw, ":")
		if !ok {
			return fmt.Errorf("microagent_model_fwd must be guestPort:vsockPort")
		}
		gp, err := parseUint16(guestRaw)
		if err != nil || gp == 0 {
			return fmt.Errorf("microagent_model_fwd guest port must be a positive uint16")
		}
		vp, err := strconv.ParseUint(strings.TrimSpace(vsockRaw), 10, 32)
		if err != nil || vp == 0 {
			return fmt.Errorf("microagent_model_fwd vsock port must be a positive uint32")
		}
		cfg.ModelGuestPort = gp
		cfg.ModelVsockPort = uint32(vp)
	}
	return nil
}

func microagentCmdlineValues(cmdline string) map[string]string {
	values := map[string]string{}
	for _, field := range strings.Fields(cmdline) {
		key, value, ok := strings.Cut(field, "=")
		if ok && strings.HasPrefix(key, "microagent_") {
			values[key] = value
		}
	}
	return values
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func configureBootNetwork() error {
	cfg, err := readNetworkBootConfig()
	if err != nil {
		return err
	}
	log.Printf("microagent-init: network = %+v", cfg)
	if cfg.Interface == "" {
		return nil
	}
	log.Printf("microagent-init: /sys/class/net = %v", listNetInterfaces())
	if err := bringUpLoopback(); err != nil {
		return err
	}
	if err := configureStaticIPv4(cfg); err != nil {
		return err
	}
	if len(cfg.DNS) != 0 {
		if err := writeResolvConf(cfg.DNS); err != nil {
			return err
		}
		log.Println("microagent-init: resolv.conf written")
	}
	if len(cfg.Hosts) != 0 {
		if err := writeHostsFile(cfg.Hosts); err != nil {
			return err
		}
		log.Printf("microagent-init: /etc/hosts written with %d named-network entries", len(cfg.Hosts))
	}
	return nil
}

// writeHostsFile writes /etc/hosts with the standard loopback entries plus one
// line per named-network member so members resolve each other by name.
func writeHostsFile(hosts []hostEntry) error {
	var b strings.Builder
	b.WriteString("127.0.0.1\tlocalhost\n")
	b.WriteString("::1\tlocalhost ip6-localhost ip6-loopback\n")
	for _, h := range hosts {
		if net.ParseIP(h.IP) == nil {
			return fmt.Errorf("invalid host entry IP %q", h.IP)
		}
		b.WriteString(h.IP)
		b.WriteByte('\t')
		b.WriteString(h.Name)
		b.WriteByte('\n')
	}
	if err := os.WriteFile("/etc/hosts", []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write /etc/hosts: %w", err)
	}
	return nil
}

func readNetworkBootConfig() (networkBootConfig, error) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return networkBootConfig{}, fmt.Errorf("read kernel command line: %w", err)
	}
	log.Printf("microagent-init: cmdline = %q", strings.TrimSpace(string(data)))
	values := microagentCmdlineValues(string(data))
	cfg := networkBootConfig{
		Interface: values["microagent_net_if"],
		IP:        values["microagent_net_ip"],
		Gateway:   values["microagent_net_gw"],
	}
	if cfg.Interface == "" {
		return cfg, nil
	}
	if cfg.IP == "" || cfg.Gateway == "" {
		return networkBootConfig{}, fmt.Errorf("microagent network boot config requires ip and gateway")
	}
	for _, dns := range strings.Split(values["microagent_net_dns"], ",") {
		dns = strings.TrimSpace(dns)
		if dns != "" {
			cfg.DNS = append(cfg.DNS, dns)
		}
	}
	for _, entry := range strings.Split(values["microagent_net_hosts"], ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, ip, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(ip) == "" {
			continue
		}
		cfg.Hosts = append(cfg.Hosts, hostEntry{Name: strings.TrimSpace(name), IP: strings.TrimSpace(ip)})
	}
	return cfg, nil
}

func listNetInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func firstGuestNetworkInterface() string {
	for _, name := range listNetInterfaces() {
		if name == "lo" || strings.HasPrefix(name, "sit") || strings.HasPrefix(name, "ip6") {
			continue
		}
		return name
	}
	return ""
}

func findGuestExecutable(name string) (string, error) {
	for _, dir := range filepath.SplitList(defaultGuestPath) {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func configureStaticIPv4(cfg networkBootConfig) error {
	iface, err := net.InterfaceByName(cfg.Interface)
	if err != nil {
		return fmt.Errorf("find guest network interface %s: %w", cfg.Interface, err)
	}
	ip, ipNet, err := net.ParseCIDR(cfg.IP)
	if err != nil {
		return fmt.Errorf("parse guest network ip %q: %w", cfg.IP, err)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("guest network ip %q must be IPv4", cfg.IP)
	}
	gateway := net.ParseIP(cfg.Gateway).To4()
	if gateway == nil {
		return fmt.Errorf("guest network gateway %q must be IPv4", cfg.Gateway)
	}
	if err := setInterfaceIPv4(cfg.Interface, ip4, net.IP(ipNet.Mask).To4()); err != nil {
		return err
	}
	if err := setInterfaceUp(cfg.Interface); err != nil {
		return err
	}
	log.Printf("microagent-init: brought up %s", cfg.Interface)
	if err := addDefaultRoute(iface.Index, gateway); err != nil && !os.IsExist(err) {
		return err
	}
	log.Println("microagent-init: route configured")
	return nil
}

func setInterfaceIPv4(name string, ip, mask net.IP) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket for %s: %w", name, err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("prepare address request for %s: %w", name, err)
	}
	if err := ifr.SetInet4Addr(ip); err != nil {
		return fmt.Errorf("prepare IPv4 address for %s: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFADDR, ifr); err != nil {
		return fmt.Errorf("set IPv4 address on %s: %w", name, err)
	}
	ifr, err = unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("prepare netmask request for %s: %w", name, err)
	}
	if err := ifr.SetInet4Addr(mask); err != nil {
		return fmt.Errorf("prepare IPv4 netmask for %s: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFNETMASK, ifr); err != nil {
		return fmt.Errorf("set IPv4 netmask on %s: %w", name, err)
	}
	return nil
}

func setInterfaceUp(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket for %s: %w", name, err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("prepare flags request for %s: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("read flags for %s: %w", name, err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("bring %s up: %w", name, err)
	}
	return nil
}

func addDefaultRoute(ifindex int, gateway net.IP) error {
	req := make([]byte, unix.SizeofNlMsghdr+unix.SizeofRtMsg)
	header := (*unix.NlMsghdr)(unsafe.Pointer(&req[0]))
	header.Type = unix.RTM_NEWROUTE
	header.Flags = unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL
	header.Seq = 1
	msg := (*unix.RtMsg)(unsafe.Pointer(&req[unix.SizeofNlMsghdr]))
	msg.Family = unix.AF_INET
	msg.Table = unix.RT_TABLE_MAIN
	msg.Protocol = unix.RTPROT_BOOT
	msg.Scope = unix.RT_SCOPE_UNIVERSE
	msg.Type = unix.RTN_UNICAST
	req = appendNetlinkAttr(req, unix.RTA_GATEWAY, gateway.To4())
	ifIndexBytes := make([]byte, 4)
	binary.NativeEndian.PutUint32(ifIndexBytes, uint32(ifindex))
	req = appendNetlinkAttr(req, unix.RTA_OIF, ifIndexBytes)
	header = (*unix.NlMsghdr)(unsafe.Pointer(&req[0]))
	header.Len = uint32(len(req))
	return sendNetlinkRequest(req, header.Seq, "add default route")
}

func appendNetlinkAttr(msg []byte, attrType uint16, data []byte) []byte {
	start := len(msg)
	length := unix.SizeofRtAttr + len(data)
	msg = append(msg, make([]byte, nlAlign(length))...)
	attr := (*unix.RtAttr)(unsafe.Pointer(&msg[start]))
	attr.Len = uint16(length)
	attr.Type = attrType
	copy(msg[start+unix.SizeofRtAttr:], data)
	return msg
}

func sendNetlinkRequest(req []byte, seq uint32, action string) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("%s: open netlink socket: %w", action, err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("%s: bind netlink socket: %w", action, err)
	}
	tv := unix.Timeval{Sec: 5}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return fmt.Errorf("%s: set netlink receive timeout: %w", action, err)
	}
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("%s: send netlink request: %w", action, err)
	}
	buf := make([]byte, 4096)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) {
				return fmt.Errorf("%s: timed out waiting for netlink ack", action)
			}
			return fmt.Errorf("%s: receive netlink ack: %w", action, err)
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return fmt.Errorf("%s: parse netlink ack: %w", action, err)
		}
		for _, msg := range msgs {
			if msg.Header.Seq != seq {
				continue
			}
			switch msg.Header.Type {
			case unix.NLMSG_DONE:
				return nil
			case unix.NLMSG_ERROR:
				if len(msg.Data) < 4 {
					return fmt.Errorf("%s: short netlink error", action)
				}
				code := int32(binary.NativeEndian.Uint32(msg.Data[:4]))
				if code == 0 {
					return nil
				}
				errno := unix.Errno(-code)
				if errno == unix.EEXIST {
					return os.ErrExist
				}
				return fmt.Errorf("%s: %w", action, errno)
			}
		}
		if len(msgs) == 0 {
			return nil
		}
	}
}

func nlAlign(length int) int {
	return (length + unix.NLMSG_ALIGNTO - 1) & ^(unix.NLMSG_ALIGNTO - 1)
}

func writeResolvConf(dns []string) error {
	var builder strings.Builder
	for _, server := range dns {
		ip := net.ParseIP(server)
		if ip == nil {
			return fmt.Errorf("invalid DNS server %q", server)
		}
		builder.WriteString("nameserver ")
		builder.WriteString(server)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile("/etc/resolv.conf", []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write /etc/resolv.conf: %w", err)
	}
	return nil
}

func parseTCPVsockBridges(raw string) ([]tcpVsockBridge, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	bridges := make([]tcpVsockBridge, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		left, right, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%s entry %q must be listen=vsockPort", tcpVsockListenersEnv, part)
		}
		listen := normalizeTCPListen(left)
		port, err := strconv.ParseUint(strings.TrimSpace(right), 10, 32)
		if err != nil || port == 0 {
			return nil, fmt.Errorf("%s entry %q has invalid vsock port", tcpVsockListenersEnv, part)
		}
		bridges = append(bridges, tcpVsockBridge{Listen: listen, Port: uint32(port)})
	}
	return bridges, nil
}

func bringUpLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket for loopback: %w", err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return fmt.Errorf("prepare loopback interface request: %w", err)
	}
	ifr.SetUint16(unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("bring loopback up: %w", err)
	}
	return nil
}

func normalizeTCPListen(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "127.0.0.1:0"
	}
	if strings.Contains(value, ":") {
		return value
	}
	return "127.0.0.1:" + value
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return os.Getenv(key)
}

func serveTCPVsockBridge(listener net.Listener, port uint32) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go proxyTCPToHostVsock(conn, port)
	}
}

func proxyTCPToHostVsock(conn net.Conn, port uint32) {
	defer conn.Close()
	fd, err := dialHostVsock(port, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect vsock bridge port %d: %v\n", port, err)
		return
	}
	file := os.NewFile(uintptr(fd), "vsock")
	if file == nil {
		_ = unix.Close(fd)
		return
	}
	defer file.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(file, conn)
		closeWriteFile(file)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, file)
		closeWriteConn(conn)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func serveHostForward(fd int, guestPort uint16) {
	for {
		connFD, _, err := unix.Accept(fd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept host forward vsock for guest tcp port %d: %v\n", guestPort, err)
			_ = unix.Close(fd)
			return
		}
		go proxyHostVsockToGuestTCP(connFD, guestPort)
	}
}

func proxyHostVsockToGuestTCP(fd int, guestPort uint16) {
	file := os.NewFile(uintptr(fd), "host-forward-vsock")
	if file == nil {
		_ = unix.Close(fd)
		return
	}
	defer file.Close()
	target, conn, err := dialGuestTCP(guestPort, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect guest tcp port %d: %v\n", guestPort, err)
		return
	}
	log.Printf("microagent-init: host forward connected guest tcp %s", target)
	defer conn.Close()
	done := make(chan struct{}, 2)
	go func() {
		if _, err := io.Copy(file, conn); err != nil {
			fmt.Fprintf(os.Stderr, "copy guest tcp port %d to host forward vsock: %v\n", guestPort, err)
		}
		closeWriteFile(file)
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(conn, file); err != nil {
			fmt.Fprintf(os.Stderr, "copy host forward vsock to guest tcp port %d: %v\n", guestPort, err)
		}
		closeWriteConn(conn)
		done <- struct{}{}
	}()
	<-done
	_ = conn.Close()
	_ = file.Close()
}

func dialGuestTCP(port uint16, timeout time.Duration) (string, net.Conn, error) {
	var lastErr error
	for _, host := range guestTCPForwardTargets() {
		target := net.JoinHostPort(host, strconv.Itoa(int(port)))
		conn, err := net.DialTimeout("tcp", target, timeout)
		if err == nil {
			return target, conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no guest tcp targets")
	}
	return "", nil, lastErr
}

func guestTCPForwardTargets() []string {
	targets := []string{"127.0.0.1"}
	interfaces, err := net.Interfaces()
	if err != nil {
		return targets
	}
	seen := map[string]bool{"127.0.0.1": true}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			raw := ip.String()
			if seen[raw] {
				continue
			}
			seen[raw] = true
			targets = append(targets, raw)
		}
	}
	return targets
}

func closeWriteConn(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if writer, ok := conn.(closeWriter); ok {
		_ = writer.CloseWrite()
	}
}

func closeWriteFile(file *os.File) {
	_ = unix.Shutdown(int(file.Fd()), unix.SHUT_WR)
}

func dialHostVsock(port uint32, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err == nil {
			err = unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port})
			if err == nil {
				return fd, nil
			}
			_ = unix.Close(fd)
		}
		lastErr = err
		if time.Now().After(deadline) {
			return -1, lastErr
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func attachConsole() error {
	for _, path := range []string{"/dev/hvc0", "/dev/console"} {
		fd, err := unix.Open(path, unix.O_RDWR, 0)
		if err != nil {
			continue
		}
		if _, err := unix.Setsid(); err != nil && err != unix.EPERM {
			_ = unix.Close(fd)
			return fmt.Errorf("start console session: %w", err)
		}
		_ = unix.IoctlSetInt(fd, unix.TIOCSCTTY, 0)
		for target := 0; target <= 2; target++ {
			if err := unix.Dup2(fd, target); err != nil {
				_ = unix.Close(fd)
				return fmt.Errorf("attach %s to fd %d: %w", path, target, err)
			}
		}
		if fd > 2 {
			_ = unix.Close(fd)
		}
		return nil
	}
	return fmt.Errorf("open guest console: /dev/hvc0 and /dev/console are unavailable")
}

func markExtraFilesCloseOnExec() {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd <= 2 {
			continue
		}
		unix.CloseOnExec(fd)
	}
}

func readConfig() (config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return config{}, fmt.Errorf("read %s: %w", configPath, err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", configPath, err)
	}
	return cfg, nil
}

func sendResult(port uint32, res result) error {
	if port == 0 {
		return nil
	}
	addr := &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}
	deadline := time.Now().Add(resultConnectTimeout)
	var fd int
	var err error
	for {
		fd, err = unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err != nil {
			return fmt.Errorf("open vsock: %w", err)
		}
		if err = unix.Connect(fd, addr); err == nil {
			break
		}
		_ = unix.Close(fd)
		if time.Now().After(deadline) {
			return fmt.Errorf("connect vsock port %d: %w", port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer unix.Close(fd)
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		data = data[n:]
	}
	return nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 1
}

func poweroff() {
	unix.Sync()
	time.Sleep(250 * time.Millisecond)
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
}
