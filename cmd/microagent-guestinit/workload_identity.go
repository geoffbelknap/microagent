//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type workloadIdentity struct {
	UID    uint32
	GID    uint32
	Groups []uint32
	Name   string
}

type passwdEntry struct {
	name string
	uid  uint32
	gid  uint32
}

func configureWorkloadCommand(cmd *exec.Cmd, userSpec, workingDir string) error {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir != "" {
		if !filepath.IsAbs(workingDir) {
			return fmt.Errorf("OCI working directory must be absolute: %q", workingDir)
		}
		cmd.Dir = workingDir
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	identity, err := resolveWorkloadIdentity(userSpec, "/etc/passwd", "/etc/group")
	if err != nil {
		return err
	}
	if identity == nil {
		return nil
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid: identity.UID, Gid: identity.GID, Groups: identity.Groups,
	}
	return nil
}

func resolveWorkloadIdentity(userSpec, passwdPath, groupPath string) (*workloadIdentity, error) {
	userSpec = strings.TrimSpace(userSpec)
	if userSpec == "" {
		return nil, nil
	}
	userPart, groupPart, hasGroup := strings.Cut(userSpec, ":")
	if userPart == "" || strings.Contains(groupPart, ":") {
		return nil, fmt.Errorf("invalid OCI user %q", userSpec)
	}
	passwdEntries, err := readPasswdEntries(passwdPath)
	if err != nil {
		return nil, err
	}

	identity := &workloadIdentity{}
	if uid, numeric := parseID(userPart); numeric {
		identity.UID = uid
		for _, entry := range passwdEntries {
			if entry.uid == uid {
				identity.Name = entry.name
				identity.GID = entry.gid
				break
			}
		}
	} else {
		found := false
		for _, entry := range passwdEntries {
			if entry.name == userPart {
				identity.Name = entry.name
				identity.UID = entry.uid
				identity.GID = entry.gid
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("OCI user %q is not present in /etc/passwd", userPart)
		}
	}

	groupEntries, err := readGroupEntries(groupPath)
	if err != nil {
		return nil, err
	}
	if hasGroup && groupPart != "" {
		if gid, numeric := parseID(groupPart); numeric {
			identity.GID = gid
		} else {
			gid, ok := groupEntries.byName[groupPart]
			if !ok {
				return nil, fmt.Errorf("OCI group %q is not present in /etc/group", groupPart)
			}
			identity.GID = gid
		}
	}
	if identity.Name != "" && (!hasGroup || groupPart == "") {
		seen := map[uint32]bool{identity.GID: true}
		for _, group := range groupEntries.membership[identity.Name] {
			if !seen[group] {
				identity.Groups = append(identity.Groups, group)
				seen[group] = true
			}
		}
	}
	return identity, nil
}

func parseID(value string) (uint32, bool) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	return uint32(parsed), err == nil
}

func readPasswdEntries(filename string) ([]passwdEntry, error) {
	file, err := os.Open(filename)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read OCI user database: %w", err)
	}
	defer file.Close()
	var entries []passwdEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 4 {
			continue
		}
		uid, uidOK := parseID(fields[2])
		gid, gidOK := parseID(fields[3])
		if uidOK && gidOK {
			entries = append(entries, passwdEntry{name: fields[0], uid: uid, gid: gid})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read OCI user database: %w", err)
	}
	return entries, nil
}

type groupEntries struct {
	byName     map[string]uint32
	membership map[string][]uint32
}

func readGroupEntries(filename string) (groupEntries, error) {
	result := groupEntries{byName: map[string]uint32{}, membership: map[string][]uint32{}}
	file, err := os.Open(filename)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read OCI group database: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 4 {
			continue
		}
		gid, ok := parseID(fields[2])
		if !ok {
			continue
		}
		result.byName[fields[0]] = gid
		for _, member := range strings.Split(fields[3], ",") {
			if member = strings.TrimSpace(member); member != "" {
				result.membership[member] = append(result.membership[member], gid)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read OCI group database: %w", err)
	}
	return result, nil
}
