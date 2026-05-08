//go:build linux

package main

import (
	"errors"
	"reflect"
	"syscall"
	"testing"
)

func TestMountGuestFilesystemsMountsProcSysAndDevPTS(t *testing.T) {
	oldMount := mountGuestFilesystem
	t.Cleanup(func() { mountGuestFilesystem = oldMount })
	var calls []guestFilesystem
	mountGuestFilesystem = func(source, target, fsType string, flags uintptr, data string) error {
		calls = append(calls, guestFilesystem{Source: source, Target: target, FSType: fsType, Flags: flags})
		return nil
	}
	if err := mountGuestFilesystems(); err != nil {
		t.Fatalf("mountGuestFilesystems: %v", err)
	}
	if !reflect.DeepEqual(calls, guestFilesystems) {
		t.Fatalf("mount calls = %#v, want %#v", calls, guestFilesystems)
	}
}

func TestMountGuestFilesystemsAllowsAlreadyMounted(t *testing.T) {
	oldMount := mountGuestFilesystem
	t.Cleanup(func() { mountGuestFilesystem = oldMount })
	mountGuestFilesystem = func(string, string, string, uintptr, string) error {
		return syscall.EBUSY
	}
	if err := mountGuestFilesystems(); err != nil {
		t.Fatalf("mountGuestFilesystems: %v", err)
	}
}

func TestMountGuestFilesystemsRejectsMountFailure(t *testing.T) {
	oldMount := mountGuestFilesystem
	t.Cleanup(func() { mountGuestFilesystem = oldMount })
	mountGuestFilesystem = func(string, string, string, uintptr, string) error {
		return syscall.EPERM
	}
	err := mountGuestFilesystems()
	if err == nil {
		t.Fatal("mountGuestFilesystems error = nil")
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("mountGuestFilesystems err = %v", err)
	}
}

func TestParseTCPVsockBridges(t *testing.T) {
	got, err := parseTCPVsockBridges("3128=3128,127.0.0.1:8081=8081")
	if err != nil {
		t.Fatalf("parseTCPVsockBridges: %v", err)
	}
	want := []tcpVsockBridge{
		{Listen: "127.0.0.1:3128", Port: 3128},
		{Listen: "127.0.0.1:8081", Port: 8081},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bridges = %#v, want %#v", got, want)
	}
}

func TestParseTCPVsockBridgesRejectsBadEntries(t *testing.T) {
	for _, raw := range []string{"3128", "3128=0", "3128=nope"} {
		if _, err := parseTCPVsockBridges(raw); err == nil {
			t.Fatalf("parseTCPVsockBridges(%q) error = nil, want error", raw)
		}
	}
}

func TestEnvValuePrefersConfigEnv(t *testing.T) {
	t.Setenv(tcpVsockListenersEnv, "from-process")
	got := envValue([]string{tcpVsockListenersEnv + "=from-config"}, tcpVsockListenersEnv)
	if got != "from-config" {
		t.Fatalf("envValue = %q, want from-config", got)
	}
}

func TestCmdlineRequestsDHCP(t *testing.T) {
	for _, cmdline := range []string{
		"console=hvc0 root=/dev/vda ip=dhcp",
		"ip=on console=hvc0",
		"root=/dev/vda ip=any",
	} {
		if !cmdlineRequestsDHCP(cmdline) {
			t.Fatalf("cmdlineRequestsDHCP(%q) = false, want true", cmdline)
		}
	}
	if cmdlineRequestsDHCP("console=hvc0 root=/dev/vda ip=off") {
		t.Fatal("cmdlineRequestsDHCP(ip=off) = true, want false")
	}
}

func TestParseKernelPNPNameservers(t *testing.T) {
	got := parseKernelPNPNameservers(`
#PROTO: DHCP
domain local
nameserver 192.168.64.1
# nameserver 8.8.8.8
nameserver not-an-ip
nameserver 192.168.64.1
`)
	want := []string{"192.168.64.1", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %#v, want %#v", got, want)
	}
}
