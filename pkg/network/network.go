// Package network manages user-defined named networks: VM-independent records
// that workspaces can join so multiple workspaces share a subnet and address
// each other by name. This package owns the registry and address allocation
// only — a backend-neutral data model persisted under the state directory.
// Live realization (host bridge, cross-VM connectivity, name resolution) is a
// separate supervisor-side concern layered on top of these records.
package network

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// allocationBase is the address space named networks carve /24 subnets from.
// It is distinct from the per-VM NAT range (10.43.0.0/16) so auto-allocated
// named networks never collide with disposable NAT subnets.
const (
	allocationPrefix = "10.44."
	subnetSuffix     = ".0/24"
	minSubnetOctet   = 1
	maxSubnetOctet   = 254
)

// Member is a workspace's stable address on a network.
type Member struct {
	Workspace string `json:"workspace"`
	IP        string `json:"ip"`
}

// Record is one named network.
type Record struct {
	Name      string   `json:"name"`
	Subnet    string   `json:"subnet"`  // CIDR, e.g. 10.44.1.0/24
	Gateway   string   `json:"gateway"` // first usable host, e.g. 10.44.1.1
	CreatedAt string   `json:"created_at,omitempty"`
	Members   []Member `json:"members,omitempty"`
}

// Index is the persisted set of named networks.
type Index struct {
	Networks []Record `json:"networks"`
}

// IndexPath returns the registry file path for a state directory.
func IndexPath(stateDir string) string {
	return filepath.Join(stateDir, "networks", "index.json")
}

// ReadIndex loads the registry, returning an empty Index when none exists.
func ReadIndex(stateDir string) (Index, error) {
	data, err := os.ReadFile(IndexPath(stateDir))
	if os.IsNotExist(err) {
		return Index{}, nil
	}
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

// WriteIndex persists the registry.
func WriteIndex(stateDir string, idx Index) error {
	if err := os.MkdirAll(filepath.Dir(IndexPath(stateDir)), 0o755); err != nil {
		return err
	}
	sortRecords(idx.Networks)
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(IndexPath(stateDir), data, 0o644)
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
}

// ValidName reports whether name is a usable network name: a DNS-label-like
// token (lowercase letters, digits, hyphens; must start alphanumeric).
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(name)-1:
		default:
			return false
		}
	}
	return true
}

// Create registers a new named network. When subnet is empty an unused /24 is
// allocated from the managed range. It fails closed on a duplicate name or an
// overlapping subnet.
func Create(stateDir, name, subnet string) (Record, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Record{}, fmt.Errorf("invalid network name %q: use lowercase letters, digits, and hyphens (1-63 chars, not starting or ending with a hyphen)", name)
	}
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, r := range idx.Networks {
		if r.Name == name {
			return Record{}, fmt.Errorf("network %q already exists", name)
		}
	}

	var prefix netip.Prefix
	if strings.TrimSpace(subnet) == "" {
		allocated, err := nextFreeSubnet(idx.Networks)
		if err != nil {
			return Record{}, err
		}
		prefix = allocated
	} else {
		prefix, err = netip.ParsePrefix(strings.TrimSpace(subnet))
		if err != nil {
			return Record{}, fmt.Errorf("invalid subnet %q: %w", subnet, err)
		}
		if !prefix.Addr().Is4() {
			return Record{}, fmt.Errorf("invalid subnet %q: only IPv4 networks are supported", subnet)
		}
		prefix = prefix.Masked()
		if prefix.Bits() > 30 {
			return Record{}, fmt.Errorf("invalid subnet %q: prefix must be /30 or larger to hold a gateway and members", subnet)
		}
		for _, r := range idx.Networks {
			existing, err := netip.ParsePrefix(r.Subnet)
			if err != nil {
				continue
			}
			if prefixesOverlap(prefix, existing) {
				return Record{}, fmt.Errorf("subnet %s overlaps existing network %q (%s)", prefix, r.Name, r.Subnet)
			}
		}
	}

	gateway := firstHost(prefix)
	record := Record{
		Name:      name,
		Subnet:    prefix.String(),
		Gateway:   gateway.String(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	idx.Networks = append(idx.Networks, record)
	if err := WriteIndex(stateDir, idx); err != nil {
		return Record{}, err
	}
	return record, nil
}

// List returns all networks sorted by name.
func List(stateDir string) ([]Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return nil, err
	}
	sortRecords(idx.Networks)
	return idx.Networks, nil
}

// Get returns one network by name.
func Get(stateDir, name string) (Record, error) {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return Record{}, err
	}
	for _, r := range idx.Networks {
		if r.Name == name {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("network %q not found", name)
}

// Remove deletes a network. It fails closed while the network still has
// members unless force is set.
func Remove(stateDir, name string, force bool) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	for i, r := range idx.Networks {
		if r.Name != name {
			continue
		}
		if len(r.Members) > 0 && !force {
			members := make([]string, len(r.Members))
			for j, m := range r.Members {
				members[j] = m.Workspace
			}
			return fmt.Errorf("network %q still has members: %s (use --force to remove anyway)", name, strings.Join(members, ", "))
		}
		idx.Networks = append(idx.Networks[:i], idx.Networks[i+1:]...)
		return WriteIndex(stateDir, idx)
	}
	return fmt.Errorf("network %q not found", name)
}

// Join allocates a stable member IP for workspace on the named network and
// persists it. If the workspace is already a member, its existing IP is
// returned unchanged so an address survives stop/start. It fails closed when
// the subnet has no free host address.
func Join(stateDir, name, workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("workspace name is required to join a network")
	}
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return "", err
	}
	for i := range idx.Networks {
		r := &idx.Networks[i]
		if r.Name != name {
			continue
		}
		for _, m := range r.Members {
			if m.Workspace == workspace {
				return m.IP, nil
			}
		}
		ip, err := nextFreeHost(*r)
		if err != nil {
			return "", err
		}
		r.Members = append(r.Members, Member{Workspace: workspace, IP: ip})
		if err := WriteIndex(stateDir, idx); err != nil {
			return "", err
		}
		return ip, nil
	}
	return "", fmt.Errorf("network %q not found", name)
}

// Leave removes a workspace's membership from a network, freeing its IP. It is
// idempotent and never errors on an unknown network or non-member.
func Leave(stateDir, name, workspace string) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	changed := false
	for i := range idx.Networks {
		if idx.Networks[i].Name != name {
			continue
		}
		if removed := removeMember(&idx.Networks[i], workspace); removed {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return WriteIndex(stateDir, idx)
}

// LeaveAll removes a workspace from every network it belongs to. Use it when a
// workspace is deleted so no network keeps a member that no longer exists.
func LeaveAll(stateDir, workspace string) error {
	idx, err := ReadIndex(stateDir)
	if err != nil {
		return err
	}
	changed := false
	for i := range idx.Networks {
		if removeMember(&idx.Networks[i], workspace) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return WriteIndex(stateDir, idx)
}

func removeMember(record *Record, workspace string) bool {
	for i, m := range record.Members {
		if m.Workspace == workspace {
			record.Members = append(record.Members[:i], record.Members[i+1:]...)
			return true
		}
	}
	return false
}

// nextFreeHost returns the lowest unused host address in the record's subnet,
// skipping the network address and the gateway.
func nextFreeHost(record Record) (string, error) {
	prefix, err := netip.ParsePrefix(record.Subnet)
	if err != nil {
		return "", fmt.Errorf("parse network %q subnet %q: %w", record.Name, record.Subnet, err)
	}
	used := map[string]bool{record.Gateway: true}
	for _, m := range record.Members {
		used[m.IP] = true
	}
	addr := prefix.Masked().Addr().Next() // first host
	for prefix.Contains(addr) {
		next := addr.Next()
		if !next.IsValid() || !prefix.Contains(next) {
			// addr is the broadcast/last address; stop before using it.
			break
		}
		if !used[addr.String()] {
			return addr.String(), nil
		}
		addr = next
	}
	return "", fmt.Errorf("network %q subnet %s has no free address", record.Name, record.Subnet)
}

// nextFreeSubnet returns the lowest unused /24 in the managed range.
func nextFreeSubnet(existing []Record) (netip.Prefix, error) {
	used := make(map[string]bool, len(existing))
	for _, r := range existing {
		if p, err := netip.ParsePrefix(r.Subnet); err == nil {
			used[p.Masked().String()] = true
		}
	}
	for octet := minSubnetOctet; octet <= maxSubnetOctet; octet++ {
		candidate, err := netip.ParsePrefix(fmt.Sprintf("%s%d%s", allocationPrefix, octet, subnetSuffix))
		if err != nil {
			return netip.Prefix{}, err
		}
		if !used[candidate.Masked().String()] {
			return candidate.Masked(), nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("no free subnet available in %s0.0/16", allocationPrefix)
}

// firstHost returns the first usable host address (network address + 1).
func firstHost(prefix netip.Prefix) netip.Addr {
	return prefix.Masked().Addr().Next()
}

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Overlaps(b)
}
