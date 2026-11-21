//go:build linux

package php

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusrb2/xabi/internal/sysutil"
)

func discoverPlatform(ctx context.Context, opts DiscoverOptions) []Installation {
	byVer := make(map[string]Installation)
	managedBinDir := filepath.Clean(opts.Paths.BinDir())

	// 1. Scan ~/.local/share/xabi/php/<ver>/
	if phpData := opts.Paths.PHPDataDir(); phpData != "" {
		if entries, err := os.ReadDir(phpData); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				ver, err := NormalizeVersion(e.Name())
				if err != nil || ver == "system" {
					continue
				}
				base := filepath.Join(phpData, e.Name())
				cli := firstExistingExec(filepath.Join(base, "bin", "php"), filepath.Join(base, "php"))
				fpm := firstExistingExec(filepath.Join(base, "sbin", "php-fpm"), filepath.Join(base, "bin", "php-fpm"), filepath.Join(base, "php-fpm"))
				if cli != "" || fpm != "" {
					mergeInstallation(byVer, Installation{Version: ver, CLI: cli, FPM: fpm, Source: "xabi"})
				}
			}
		}
	}

	// 2. Scan PATH + standard system binary directories (skipping xabi's own BinDir symlinks).
	dirs := append(filepath.SplitList(opts.PathEnv), "/usr/bin", "/usr/local/bin", "/usr/sbin", "/usr/local/sbin")
	dirs = append(dirs, opts.SearchRoots...)

	seenFiles := make(map[string]bool)
	for _, dir := range dirs {
		if dir == "" || sameCleanPath(dir, managedBinDir) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, "php") {
				continue
			}
			full := filepath.Join(dir, name)
			if !isExecFile(full) {
				continue
			}
			resolved, err := filepath.EvalSymlinks(full)
			if err == nil {
				full = resolved
			}
			if seenFiles[full] {
				continue
			}
			seenFiles[full] = true

			if ver, kind := classifyUnixPHPBinary(ctx, opts.Sys, name, full); ver != "" {
				inst := Installation{Version: ver, Source: "system"}
				if kind == "fpm" {
					inst.FPM = full
				} else {
					inst.CLI = full
				}
				mergeInstallation(byVer, inst)
			}
		}
	}

	return mapValues(byVer)
}

func classifyUnixPHPBinary(ctx context.Context, sys sysutil.System, name, full string) (ver string, kind string) {
	lower := strings.ToLower(name)
	switch {
	case lower == "php":
		return probeCLIVersion(ctx, sys, full), "cli"
	case lower == "php-fpm":
		return probeCLIVersion(ctx, sys, full), "fpm"
	case strings.HasPrefix(lower, "php-fpm"):
		v, err := NormalizeVersion(strings.TrimPrefix(lower, "php-fpm"))
		if err == nil && v != "system" {
			return v, "fpm"
		}
	case strings.HasPrefix(lower, "php") && strings.HasSuffix(lower, "-fpm"):
		mid := strings.TrimSuffix(strings.TrimPrefix(lower, "php"), "-fpm")
		if v, err := NormalizeVersion(mid); err == nil && v != "system" {
			return v, "fpm"
		}
	case strings.HasPrefix(lower, "php"):
		suffix := strings.TrimPrefix(lower, "php")
		if v, err := NormalizeVersion(suffix); err == nil && v != "system" {
			return v, "cli"
		}
	}
	return "", ""
}

func activateShimPlatform(binDir string, inst Installation) error {
	if inst.CLI != "" {
		if err := replaceSymlink(inst.CLI, filepath.Join(binDir, "php")); err != nil {
			return err
		}
	}
	if inst.FPM != "" {
		if err := replaceSymlink(inst.FPM, filepath.Join(binDir, "php-fpm")); err != nil {
			return err
		}
	}
	return nil
}

func replaceSymlink(target, linkPath string) error {
	tmp := linkPath + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, linkPath)
}

func firstExistingExec(candidates ...string) string {
	for _, p := range candidates {
		if isExecFile(p) {
			return p
		}
	}
	return ""
}
