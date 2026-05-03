//go:build linux

package main

import (
	"reflect"
	"testing"
)

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
