package network

import (
	"testing"
)

func TestCreateAutoAllocatesSubnets(t *testing.T) {
	dir := t.TempDir()
	a, err := Create(dir, "alpha", "")
	if err != nil {
		t.Fatalf("Create alpha: %v", err)
	}
	if a.Subnet != "10.44.1.0/24" || a.Gateway != "10.44.1.1" {
		t.Fatalf("alpha = %+v, want 10.44.1.0/24 gw 10.44.1.1", a)
	}
	b, err := Create(dir, "beta", "")
	if err != nil {
		t.Fatalf("Create beta: %v", err)
	}
	if b.Subnet != "10.44.2.0/24" {
		t.Fatalf("beta subnet = %s, want 10.44.2.0/24", b.Subnet)
	}
	if a.CreatedAt == "" {
		t.Error("CreatedAt should be set")
	}
}

func TestCreateExplicitSubnet(t *testing.T) {
	dir := t.TempDir()
	r, err := Create(dir, "lab", "192.168.50.0/24")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.Subnet != "192.168.50.0/24" || r.Gateway != "192.168.50.1" {
		t.Fatalf("record = %+v", r)
	}
}

func TestCreateRejectsDuplicateAndOverlap(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, "one", "10.50.0.0/24"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, "one", ""); err == nil {
		t.Error("expected duplicate name error")
	}
	if _, err := Create(dir, "two", "10.50.0.0/24"); err == nil {
		t.Error("expected overlapping subnet error")
	}
	// A non-overlapping subnet is fine.
	if _, err := Create(dir, "three", "10.51.0.0/24"); err != nil {
		t.Errorf("non-overlapping subnet rejected: %v", err)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", "-bad", "bad-", "Bad", "a/b", "has space"} {
		if _, err := Create(dir, name, ""); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
	if _, err := Create(dir, "ok", "not-a-cidr"); err == nil {
		t.Error("invalid CIDR should be rejected")
	}
	if _, err := Create(dir, "ok6", "fd00::/64"); err == nil {
		t.Error("IPv6 subnet should be rejected")
	}
	if _, err := Create(dir, "ok31", "10.60.0.0/31"); err == nil {
		t.Error("/31 subnet should be rejected (too small)")
	}
}

func TestListGetRemove(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, "zeta", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, "alpha", ""); err != nil {
		t.Fatal(err)
	}
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "zeta" {
		t.Fatalf("list not sorted by name: %+v", list)
	}

	got, err := Get(dir, "zeta")
	if err != nil || got.Name != "zeta" {
		t.Fatalf("Get zeta = %+v, %v", got, err)
	}
	if _, err := Get(dir, "missing"); err == nil {
		t.Error("Get missing should error")
	}

	if err := Remove(dir, "zeta", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Get(dir, "zeta"); err == nil {
		t.Error("zeta should be gone after Remove")
	}
	if err := Remove(dir, "zeta", false); err == nil {
		t.Error("removing missing network should error")
	}
}

func TestRemoveFailsClosedWithMembers(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, "team", ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a member by writing the index directly.
	idx, _ := ReadIndex(dir)
	idx.Networks[0].Members = []Member{{Workspace: "w1", IP: "10.44.1.2"}}
	if err := WriteIndex(dir, idx); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "team", false); err == nil {
		t.Error("Remove should fail closed while members exist")
	}
	if err := Remove(dir, "team", true); err != nil {
		t.Errorf("Remove --force should succeed: %v", err)
	}
}

func TestReadIndexMissingIsEmpty(t *testing.T) {
	idx, err := ReadIndex(t.TempDir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(idx.Networks) != 0 {
		t.Fatalf("expected empty index, got %+v", idx)
	}
}
