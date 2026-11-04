package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"syscall"
)

func freeDisk(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// findmnt resolves the actual scratch mount, including bind mounts. A missing
// identity is a preflight failure rather than a misleading replication record.
func diskPlatform(ctx context.Context, path string) (kernel, filesystem, device string, err error) {
	kernel, err = commandOutput(ctx, "uname", "-srvo")
	if err != nil {
		return
	}
	var raw string
	raw, err = commandOutput(ctx, "findmnt", "--json", "--target", path, "--output", "FSTYPE,SOURCE")
	if err != nil {
		return
	}
	var mount struct {
		Filesystems []struct {
			FSType string `json:"fstype"`
			Source string `json:"source"`
		} `json:"filesystems"`
	}
	if err = json.Unmarshal([]byte(raw), &mount); err != nil {
		return
	}
	if len(mount.Filesystems) != 1 {
		err = fmt.Errorf("findmnt: expected one scratch mount")
		return
	}
	filesystem, device = mount.Filesystems[0].FSType, mount.Filesystems[0].Source
	if strings.TrimSpace(filesystem) == "" || strings.TrimSpace(device) == "" {
		err = fmt.Errorf("findmnt: missing filesystem/device")
	}
	return
}
