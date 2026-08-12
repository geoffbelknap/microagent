//go:build linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveWorkloadIdentityByNameAndGroup(t *testing.T) {
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	groupPath := filepath.Join(dir, "group")
	if err := os.WriteFile(passwdPath, []byte("root:x:0:0::/root:/bin/sh\nhomebridge:x:1001:1002::/homebridge:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(groupPath, []byte("homebridge:x:1002:\naudio:x:29:homebridge\noverride:x:2000:\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	identity, err := resolveWorkloadIdentity("homebridge:override", passwdPath, groupPath)
	if err != nil {
		t.Fatal(err)
	}
	if identity.UID != 1001 || identity.GID != 2000 || identity.Name != "homebridge" || len(identity.Groups) != 0 {
		t.Fatalf("identity = %#v", identity)
	}
	identity, err = resolveWorkloadIdentity("homebridge", passwdPath, groupPath)
	if err != nil {
		t.Fatal(err)
	}
	if identity.GID != 1002 || !reflect.DeepEqual(identity.Groups, []uint32{29}) {
		t.Fatalf("default groups = %#v", identity)
	}
}

func TestResolveWorkloadIdentityNumericFallback(t *testing.T) {
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "missing-passwd")
	identity, err := resolveWorkloadIdentity("1234:5678", passwdPath, filepath.Join(dir, "missing-group"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.UID != 1234 || identity.GID != 5678 || len(identity.Groups) != 0 {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestResolveWorkloadIdentityNumericWithoutGroupDefaultsToRootGroup(t *testing.T) {
	dir := t.TempDir()
	identity, err := resolveWorkloadIdentity("1234", filepath.Join(dir, "missing-passwd"), filepath.Join(dir, "missing-group"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.UID != 1234 || identity.GID != 0 {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestResolveWorkloadIdentityRejectsUnknownName(t *testing.T) {
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwdPath, []byte("root:x:0:0::/root:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkloadIdentity("missing", passwdPath, filepath.Join(dir, "group")); err == nil {
		t.Fatal("unknown named user was accepted")
	}
}
