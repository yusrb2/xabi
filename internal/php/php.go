// Package php discovers installed PHP runtimes, manages CLI shims/symlinks,
// and resolves per-project .php-version FastCGI endpoints across Linux and Windows.
package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

// Installation describes a discovered PHP runtime version on the host.
type Installation struct {
	Version string // Normalized major.minor (e.g. "8.3")
	CLI     string // Path to php / php.exe
	FPM     string // Path to php-fpm* (Linux) or php-cgi.exe (Windows)
	Source  string // Origin label ("xabi", "system", "laragon", "scoop", "path")
}

// DiscoverOptions configures discovery roots for testing or custom layouts.
type DiscoverOptions struct {
	Paths       xdg.Paths
	Sys         sysutil.System
	PathEnv     string
	HomeDir     string
	SearchRoots []string
}

// NormalizeVersion normalizes inputs like "8.3", "83", "v8.3.14", "php8.3" -> "8.3".
func NormalizeVersion(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "php-fpm")
	s = strings.TrimPrefix(s, "php")
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty PHP version")
	}
	if s == "system" {
		return "system", nil
	}

	if strings.Contains(s, ".") {
		parts := strings.Split(s, ".")
		if len(parts) < 2 || !isDigits(parts[0]) || !isDigits(parts[1]) {
			return "", fmt.Errorf("invalid PHP version %q", raw)
		}
		return parts[0] + "." + parts[1], nil
	}

	if len(s) == 2 && isDigits(s) {
		return s[:1] + "." + s[1:], nil
	}
	if len(s) == 3 && isDigits(s) {
		// e.g. 810 -> 8.10
		return s[:1] + "." + s[1:], nil
	}
	return "", fmt.Errorf("invalid PHP version %q (expected e.g. 8.3)", raw)
}

// ParseVersionFromText extracts major.minor from command output or folder names.
func ParseVersionFromText(text string) string {
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '-' || r == '_' || r == '/' || r == '\\'
	}) {
		tok = strings.TrimPrefix(strings.ToLower(tok), "php")
		tok = strings.TrimPrefix(tok, "v")
		parts := strings.Split(tok, ".")
		if len(parts) >= 2 && isDigits(parts[0]) && isDigits(parts[1]) {
			return parts[0] + "." + parts[1]
		}
	}
	return ""
}

// WindowsPortForVersion deterministically maps a PHP version to a local FastCGI port (e.g. 8.3 -> 9083).
func WindowsPortForVersion(ver string) int {
	norm, err := NormalizeVersion(ver)
	if err != nil || norm == "system" {
		return 9000
	}
	parts := strings.Split(norm, ".")
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 9000
	}
	if min < 10 {
		return 9000 + maj*10 + min
	}
	return 9100 + maj*10 + min
}

// FastCGIUpstream returns the Nginx fastcgi_pass target for a specific PHP version and OS.
func FastCGIUpstream(paths xdg.Paths, version, goos string) string {
	norm, err := NormalizeVersion(version)
	if err != nil || norm == "system" {
		if goos == "windows" {
			return "127.0.0.1:9000"
		}
		return "unix:" + filepath.ToSlash(paths.SocketFile("php"))
	}
	if goos == "windows" {
		return fmt.Sprintf("127.0.0.1:%d", WindowsPortForVersion(norm))
	}
	return "unix:" + filepath.ToSlash(paths.SocketFile("php-"+norm))
}

// ReadDotPHPVersion searches dir and its nearest parent directories for a .php-version file.
func ReadDotPHPVersion(dir string) (string, bool, error) {
	cur, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}

	for {
		candidate := filepath.Join(cur, ".php-version")
		data, readErr := os.ReadFile(candidate)
		if readErr == nil {
			for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
				if idx := strings.IndexByte(line, '#'); idx >= 0 {
					line = line[:idx]
				}
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				norm, normErr := NormalizeVersion(line)
				if normErr != nil {
					return "", true, fmt.Errorf("%s: %w", candidate, normErr)
				}
				return norm, true, nil
			}
			return "", true, fmt.Errorf("%s is empty", candidate)
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", false, readErr
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "", false, nil
}

// WriteDotPHPVersion writes a normalized .php-version file in dir atomically.
func WriteDotPHPVersion(dir, version string) (string, error) {
	norm, err := NormalizeVersion(version)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, ".php-version")
	if err := sysutil.WriteFileAtomic(target, []byte(norm+"\n"), 0o644); err != nil {
		return "", err
	}
	return norm, nil
}

// Discover scans the current host for installed PHP runtimes.
func Discover(ctx context.Context, paths xdg.Paths, sys sysutil.System) []Installation {
	home, _ := os.UserHomeDir()
	return DiscoverWith(ctx, DiscoverOptions{
		Paths:   paths,
		Sys:     sys,
		PathEnv: os.Getenv("PATH"),
		HomeDir: home,
	})
}

// DiscoverWith scans using explicit options (useful for deterministic testing).
func DiscoverWith(ctx context.Context, opts DiscoverOptions) []Installation {
	if opts.Sys == nil {
		opts.Sys = sysutil.OS{BinDir: opts.Paths.BinDir()}
	}
	list := discoverPlatform(ctx, opts)
	SortInstallations(list)
	return list
}

// FindVersion returns the discovered Installation matching ver (or normalized ver).
func FindVersion(list []Installation, ver string) (Installation, bool) {
	norm, err := NormalizeVersion(ver)
	if err != nil {
		return Installation{}, false
	}
	for _, inst := range list {
		if inst.Version == norm {
			return inst, true
		}
	}
	return Installation{}, false
}

// ActivateShim points ~/.local/share/xabi/bin shims/symlinks to inst.
func ActivateShim(binDir string, inst Installation) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir %s: %w", binDir, err)
	}
	return activateShimPlatform(binDir, inst)
}

// SortInstallations orders PHP installations from newest to oldest version.
func SortInstallations(list []Installation) {
	sort.Slice(list, func(i, j int) bool {
		amaj, amin := parseMajMin(list[i].Version)
		bmaj, bmin := parseMajMin(list[j].Version)
		if amaj != bmaj {
			return amaj > bmaj
		}
		if amin != bmin {
			return amin > bmin
		}
		return list[i].Version > list[j].Version
	})
}

func probeCLIVersion(ctx context.Context, sys sysutil.System, phpBin string) string {
	if sys == nil || phpBin == "" {
		return ""
	}
	out, err := sys.Run(ctx, phpBin, "-r", "echo PHP_MAJOR_VERSION.'.'.PHP_MINOR_VERSION;")
	if err == nil {
		if norm, nerr := NormalizeVersion(strings.TrimSpace(string(out))); nerr == nil && norm != "system" {
			return norm
		}
	}
	out, err = sys.Run(ctx, phpBin, "-v")
	if err == nil {
		return ParseVersionFromText(string(out))
	}
	return ""
}

func mergeInstallation(byVer map[string]Installation, cand Installation) {
	if cand.Version == "" {
		return
	}
	existing, ok := byVer[cand.Version]
	if !ok {
		byVer[cand.Version] = cand
		return
	}
	// Prefer xabi-managed installs first, then fill missing CLI/FPM fields.
	if existing.Source != "xabi" && cand.Source == "xabi" {
		if cand.CLI == "" {
			cand.CLI = existing.CLI
		}
		if cand.FPM == "" {
			cand.FPM = existing.FPM
		}
		byVer[cand.Version] = cand
		return
	}
	if existing.CLI == "" && cand.CLI != "" {
		existing.CLI = cand.CLI
	}
	if existing.FPM == "" && cand.FPM != "" {
		existing.FPM = cand.FPM
	}
	byVer[cand.Version] = existing
}

func mapValues(m map[string]Installation) []Installation {
	out := make([]Installation, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func parseMajMin(ver string) (int, int) {
	parts := strings.Split(ver, ".")
	if len(parts) != 2 {
		return 0, 0
	}
	maj, _ := strconv.Atoi(parts[0])
	min, _ := strconv.Atoi(parts[1])
	return maj, min
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func sameCleanPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

func isExecFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(path))
		return ext == ".exe" || ext == ".cmd" || ext == ".bat"
	}
	return info.Mode()&0o111 != 0
}
