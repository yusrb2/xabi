//go:build windows

package php

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusrb2/xabi/internal/sysutil"
)

func discoverPlatform(ctx context.Context, opts DiscoverOptions) []Installation {
	byVer := make(map[string]Installation)
	managedBinDir := filepath.Clean(opts.Paths.BinDir())

	type dirSource struct {
		dir    string
		source string
		hint   string
	}
	var candidates []dirSource

	// 1. ~/.local/share/xabi/php/<ver>/
	if phpData := opts.Paths.PHPDataDir(); phpData != "" {
		if entries, err := os.ReadDir(phpData); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					candidates = append(candidates, dirSource{
						dir:    filepath.Join(phpData, e.Name()),
						source: "xabi",
						hint:   e.Name(),
					})
				}
			}
		}
	}

	// 2. Common Windows PHP locations + custom test search roots
	globs := []string{
		`C:\php*`,
		`C:\tools\php*`,
		`C:\laragon\bin\php\php-*`,
		`C:\ProgramData\chocolatey\lib\php*\tools`,
	}
	if opts.HomeDir != "" {
		globs = append(globs,
			filepath.Join(opts.HomeDir, "scoop", "apps", "php*", "current"),
			filepath.Join(opts.HomeDir, "scoop", "apps", "php*", "*"),
		)
	}
	for _, pattern := range globs {
		if matches, err := filepath.Glob(pattern); err == nil {
			for _, m := range matches {
				src := "system"
				if strings.Contains(strings.ToLower(m), "laragon") {
					src = "laragon"
				} else if strings.Contains(strings.ToLower(m), "scoop") {
					src = "scoop"
				}
				candidates = append(candidates, dirSource{dir: m, source: src, hint: filepath.Base(m)})
			}
		}
	}

	for _, root := range opts.SearchRoots {
		if root == "" {
			continue
		}
		candidates = append(candidates, dirSource{dir: root, source: "custom", hint: filepath.Base(root)})
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				if e.IsDir() && (strings.Contains(strings.ToLower(e.Name()), "php") || ParseVersionFromText(e.Name()) != "") {
					candidates = append(candidates, dirSource{
						dir:    filepath.Join(root, e.Name()),
						source: "custom",
						hint:   e.Name(),
					})
				}
			}
		}
	}

	// 3. PATH directories (excluding xabi's own BinDir shims)
	for _, dir := range filepath.SplitList(opts.PathEnv) {
		if dir == "" || sameCleanPath(dir, managedBinDir) {
			continue
		}
		candidates = append(candidates, dirSource{dir: dir, source: "path", hint: filepath.Base(dir)})
	}

	seenDirs := make(map[string]bool)
	for _, cand := range candidates {
		cleanDir := filepath.Clean(cand.dir)
		key := strings.ToLower(cleanDir)
		if seenDirs[key] {
			continue
		}
		seenDirs[key] = true

		cliPath := firstWinExec(filepath.Join(cleanDir, "php.exe"), filepath.Join(cleanDir, "bin", "php.exe"))
		cgiPath := firstWinExec(filepath.Join(cleanDir, "php-cgi.exe"), filepath.Join(cleanDir, "bin", "php-cgi.exe"))
		if cliPath == "" && cgiPath == "" {
			continue
		}

		ver := ""
		if cliPath != "" {
			ver = probeCLIVersion(ctx, opts.Sys, cliPath)
		}
		if ver == "" {
			if norm, err := NormalizeVersion(cand.hint); err == nil && norm != "system" {
				ver = norm
			} else {
				ver = ParseVersionFromText(cand.hint)
			}
		}
		if ver == "" {
			continue
		}

		mergeInstallation(byVer, Installation{
			Version: ver,
			CLI:     cliPath,
			FPM:     cgiPath,
			Source:  cand.source,
		})
	}

	return mapValues(byVer)
}

func firstWinExec(candidates ...string) string {
	for _, p := range candidates {
		if isExecFile(p) {
			return p
		}
	}
	return ""
}

func activateShimPlatform(binDir string, inst Installation) error {
	if inst.CLI != "" {
		cmdContent := fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", inst.CLI)
		if err := sysutil.WriteFileAtomic(filepath.Join(binDir, "php.cmd"), []byte(cmdContent), 0o755); err != nil {
			return err
		}
	}
	if inst.FPM != "" {
		cgiContent := fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", inst.FPM)
		if err := sysutil.WriteFileAtomic(filepath.Join(binDir, "php-cgi.cmd"), []byte(cgiContent), 0o755); err != nil {
			return err
		}
	}
	return nil
}
