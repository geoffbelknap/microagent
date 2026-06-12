// Package superviseunit generates and installs an OS init unit that runs
// `microagent supervise <name>` at boot, so a long-running workspace survives a
// host reboot — without microagent itself adding a persistent daemon. The unit
// is owned by the OS init (systemd user manager on Linux, launchd on macOS,
// the Task Scheduler on Windows); microagent only writes and registers it.
//
// Unit content, path, and the enable/disable commands are produced by Build
// (pure and testable); Install/Uninstall apply the side effects.
package superviseunit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options describes the unit to build.
type Options struct {
	Name     string // workspace name
	ExecPath string // absolute path to the microagent binary
	StateDir string // state directory passed to supervise
	Home     string // user home directory
	GOOS     string // target OS: "linux" or "darwin"
}

// Unit is a generated init unit and how to register it.
type Unit struct {
	Label       string   // unit/label identifier
	Path        string   // file path to write
	Content     string   // file content
	EnableArgs  []string // command that registers the unit for boot
	DisableArgs []string // command that unregisters it
}

// Build produces the unit for the given options without touching the
// filesystem. Only linux (systemd user unit) and darwin (launchd agent) are
// supported.
func Build(opts Options) (Unit, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return Unit{}, fmt.Errorf("workspace name is required")
	}
	if strings.TrimSpace(opts.ExecPath) == "" {
		return Unit{}, fmt.Errorf("executable path is required")
	}
	if strings.TrimSpace(opts.Home) == "" {
		return Unit{}, fmt.Errorf("home directory is required")
	}
	switch opts.GOOS {
	case "linux":
		return buildSystemd(name, opts), nil
	case "darwin":
		return buildLaunchd(name, opts), nil
	case "windows":
		return buildScheduledTask(name, opts), nil
	default:
		return Unit{}, fmt.Errorf("survive-reboot units are unsupported on %s (linux, darwin, and windows only)", opts.GOOS)
	}
}

func buildSystemd(name string, opts Options) Unit {
	label := "microagent-supervise-" + name + ".service"
	path := filepath.Join(opts.Home, ".config", "systemd", "user", label)
	exec := opts.ExecPath + " supervise " + name
	if strings.TrimSpace(opts.StateDir) != "" {
		exec += " --state-dir " + opts.StateDir
	}
	content := "[Unit]\n" +
		"Description=microagent supervise " + name + "\n" +
		"After=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + exec + "\n" +
		"Restart=on-failure\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	return Unit{
		Label:       label,
		Path:        path,
		Content:     content,
		EnableArgs:  []string{"systemctl", "--user", "enable", "--now", label},
		DisableArgs: []string{"systemctl", "--user", "disable", "--now", label},
	}
}

func buildLaunchd(name string, opts Options) Unit {
	label := "com.microagent.supervise." + name
	path := filepath.Join(opts.Home, "Library", "LaunchAgents", label+".plist")
	args := []string{opts.ExecPath, "supervise", name}
	if strings.TrimSpace(opts.StateDir) != "" {
		args = append(args, "--state-dir", opts.StateDir)
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n")
	b.WriteString("<dict>\n")
	b.WriteString("  <key>Label</key>\n  <string>" + label + "</string>\n")
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range args {
		b.WriteString("    <string>" + a + "</string>\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return Unit{
		Label:       label,
		Path:        path,
		Content:     b.String(),
		EnableArgs:  []string{"launchctl", "load", "-w", path},
		DisableArgs: []string{"launchctl", "unload", "-w", path},
	}
}

func buildScheduledTask(name string, opts Options) Unit {
	label := "microagent-supervise-" + name
	path := filepath.Join(opts.Home, ".microagent", "tasks", label+".xml")
	arguments := "supervise " + name
	if strings.TrimSpace(opts.StateDir) != "" {
		arguments += " --state-dir " + opts.StateDir
	}
	var b strings.Builder
	// No encoding declaration: the Task Scheduler XML reader rejects a UTF-8
	// declaration ("unable to switch the encoding") but accepts undeclared
	// UTF-8 content.
	b.WriteString("<?xml version=\"1.0\"?>\r\n")
	b.WriteString("<Task version=\"1.2\" xmlns=\"http://schemas.microsoft.com/windows/2004/02/mit/task\">\r\n")
	b.WriteString("  <RegistrationInfo>\r\n")
	b.WriteString("    <Description>microagent supervise " + xmlEscape(name) + "</Description>\r\n")
	b.WriteString("  </RegistrationInfo>\r\n")
	b.WriteString("  <Triggers>\r\n")
	b.WriteString("    <LogonTrigger>\r\n      <Enabled>true</Enabled>\r\n    </LogonTrigger>\r\n")
	b.WriteString("  </Triggers>\r\n")
	b.WriteString("  <Principals>\r\n")
	b.WriteString("    <Principal id=\"Author\">\r\n")
	b.WriteString("      <LogonType>InteractiveToken</LogonType>\r\n")
	b.WriteString("      <RunLevel>LeastPrivilege</RunLevel>\r\n")
	b.WriteString("    </Principal>\r\n")
	b.WriteString("  </Principals>\r\n")
	b.WriteString("  <Settings>\r\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\r\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\r\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\r\n")
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\r\n")
	b.WriteString("    <RestartOnFailure>\r\n")
	b.WriteString("      <Interval>PT1M</Interval>\r\n")
	b.WriteString("      <Count>3</Count>\r\n")
	b.WriteString("    </RestartOnFailure>\r\n")
	b.WriteString("  </Settings>\r\n")
	b.WriteString("  <Actions Context=\"Author\">\r\n")
	b.WriteString("    <Exec>\r\n")
	b.WriteString("      <Command>" + xmlEscape(opts.ExecPath) + "</Command>\r\n")
	b.WriteString("      <Arguments>" + xmlEscape(arguments) + "</Arguments>\r\n")
	b.WriteString("    </Exec>\r\n")
	b.WriteString("  </Actions>\r\n")
	b.WriteString("</Task>\r\n")
	return Unit{
		Label:       label,
		Path:        path,
		Content:     b.String(),
		EnableArgs:  []string{"schtasks", "/Create", "/TN", label, "/XML", path, "/F"},
		DisableArgs: []string{"schtasks", "/Delete", "/TN", label, "/F"},
	}
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

// Install writes the unit file and registers it for boot. It returns the unit
// (so the caller can report the path) and a separate enableErr: the file is
// always written on success, but registration may fail on hosts without an
// active user init session, in which case the caller should surface the manual
// EnableArgs rather than treat the install as complete.
func Install(unit Unit) (enableErr error, err error) {
	if err := os.MkdirAll(filepath.Dir(unit.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create unit directory: %w", err)
	}
	if err := os.WriteFile(unit.Path, []byte(unit.Content), 0o644); err != nil {
		return nil, fmt.Errorf("write unit %s: %w", unit.Path, err)
	}
	if len(unit.EnableArgs) > 0 {
		cmd := exec.Command(unit.EnableArgs[0], unit.EnableArgs[1:]...)
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			enableErr = fmt.Errorf("%w: %s", runErr, strings.TrimSpace(string(out)))
		}
	}
	return enableErr, nil
}

// Uninstall unregisters the unit and removes its file.
func Uninstall(unit Unit) error {
	if len(unit.DisableArgs) > 0 {
		cmd := exec.Command(unit.DisableArgs[0], unit.DisableArgs[1:]...)
		_, _ = cmd.CombinedOutput() // best effort: still remove the file below
	}
	if err := os.Remove(unit.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit %s: %w", unit.Path, err)
	}
	return nil
}
