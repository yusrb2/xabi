//go:build !linux && !windows

package php

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func discoverPlatform(ctx context.Context, opts DiscoverOptions) []Installation {
	byVer := make(map[string]Installation)
	managedBinDir := filepath.Clean(opts.Paths.BinDir())

	for _, dir := range append(filepath.SplitList(opts.PathEnv), opts.SearchRoots...) {
		if dir == "" || sameCleanPath(dir, managedBinDir) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "php") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if !isExecFile(full) {
				continue
			}
			if ver := probeCLIVersion(ctx, opts.Sys, full); ver != "" {
				mergeInstallation(byVer, Installation{Version: ver, CLI: full, Source: "path"})
			}
		}
	}
	return mapValues(byVer)
}

func activateShimPlatform(binDir string, inst Installation) error {
	if inst.CLI != "" {
		link := filepath.Join(binDir, "php")
		_ = os.Remove(link)
		return os.Symlink(inst.CLI, link)
	}
	return nil
}
