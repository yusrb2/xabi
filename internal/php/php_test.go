package php

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

type stubPHPSys struct {
	verByBin map[string]string
}

func (s *stubPHPSys) LookBinary(_ ...string) (string, bool)    { return "", false }
func (s *stubPHPSys) EvalSymlinks(path string) (string, error) { return path, nil }
func (s *stubPHPSys) CheckPort(port int, _ time.Duration) sysutil.PortStatus {
	return sysutil.PortStatus{Port: port}
}
func (s *stubPHPSys) CheckHosts(path string) sysutil.HostsStatus {
	return sysutil.HostsStatus{Path: path}
}
func (s *stubPHPSys) HostsPath() string                               { return "" }
func (s *stubPHPSys) CheckNetBindCap(_ string) sysutil.CapStatus      { return sysutil.CapStatus{} }
func (s *stubPHPSys) SetNetBindCap(_ context.Context, _ string) error { return nil }
func (s *stubPHPSys) OpenURL(_ context.Context, _ string) error       { return nil }
func (s *stubPHPSys) RunAttached(_ context.Context, _ string, _ []string, _ io.Reader, _, _ io.Writer) error {
	return nil
}
func (s *stubPHPSys) IsRoot() bool { return false }
func (s *stubPHPSys) Run(_ context.Context, name string, _ ...string) ([]byte, error) {
	if v, ok := s.verByBin[name]; ok {
		return []byte(v), nil
	}
	return nil, os.ErrNotExist
}

func TestNormalizeVersionAndFastCGIEndpoint(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"8.3", "8.3"},
		{"83", "8.3"},
		{"v8.4.2", "8.4"},
		{"php8.2", "8.2"},
		{"system", "system"},
	} {
		got, err := NormalizeVersion(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeVersion(%q) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}

	if _, err := NormalizeVersion("not-a-ver"); err == nil {
		t.Error("expected error for invalid version")
	}

	if got := WindowsPortForVersion("8.3"); got != 9083 {
		t.Errorf("WindowsPortForVersion(8.3) = %d, want 9083", got)
	}
	if got := WindowsPortForVersion("8.4"); got != 9084 {
		t.Errorf("WindowsPortForVersion(8.4) = %d, want 9084", got)
	}

	paths := xdg.Paths{RuntimeDir: "/run/user/1000/xabi"}
	if got := FastCGIUpstream(paths, "8.3", "linux"); got != "unix:/run/user/1000/xabi/php-8.3.sock" {
		t.Errorf("linux FastCGIUpstream(8.3) = %q", got)
	}
	if got := FastCGIUpstream(paths, "8.3", "windows"); got != "127.0.0.1:9083" {
		t.Errorf("windows FastCGIUpstream(8.3) = %q", got)
	}
	if got := FastCGIUpstream(paths, "system", "windows"); got != "127.0.0.1:9000" {
		t.Errorf("windows FastCGIUpstream(system) = %q", got)
	}
}

func TestDotPHPVersionNearestParent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	ver, err := WriteDotPHPVersion(root, "v8.3.10")
	if err != nil || ver != "8.3" {
		t.Fatalf("WriteDotPHPVersion = (%q, %v), want 8.3", ver, err)
	}

	got, found, err := ReadDotPHPVersion(sub)
	if err != nil || !found || got != "8.3" {
		t.Fatalf("ReadDotPHPVersion(%q) = (%q, %v, %v), want (8.3, true, nil)", sub, got, found, err)
	}
}

func TestDiscoverAndActivateShim(t *testing.T) {
	base := t.TempDir()
	paths := xdg.Paths{
		ConfigDir:  filepath.Join(base, "config", "xabi"),
		DataDir:    filepath.Join(base, "share", "xabi"),
		RuntimeDir: filepath.Join(base, "run", "xabi"),
	}

	var list []Installation
	if runtime.GOOS == "windows" {
		php83Dir := filepath.Join(paths.PHPDataDir(), "8.3")
		php84Dir := filepath.Join(base, "custom", "php-8.4.1-Win32")
		for _, dir := range []string{php83Dir, php84Dir} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, exe := range []string{"php.exe", "php-cgi.exe"} {
				if err := os.WriteFile(filepath.Join(dir, exe), []byte("stub"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		sys := &stubPHPSys{verByBin: map[string]string{
			filepath.Join(php83Dir, "php.exe"): "8.3",
			filepath.Join(php84Dir, "php.exe"): "8.4",
		}}
		list = DiscoverWith(context.Background(), DiscoverOptions{
			Paths:       paths,
			Sys:         sys,
			SearchRoots: []string{filepath.Join(base, "custom")},
		})
	} else {
		php83 := filepath.Join(paths.PHPDataDir(), "8.3", "bin", "php")
		fpm83 := filepath.Join(paths.PHPDataDir(), "8.3", "sbin", "php-fpm")
		sys84 := filepath.Join(base, "usrbin", "php8.4")
		for _, f := range []string{php83, fpm83, sys84} {
			if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		sys := &stubPHPSys{verByBin: map[string]string{
			php83: "8.3",
			sys84: "8.4",
		}}
		list = DiscoverWith(context.Background(), DiscoverOptions{
			Paths:   paths,
			Sys:     sys,
			PathEnv: filepath.Join(base, "usrbin"),
		})
	}

	if len(list) != 2 || list[0].Version != "8.4" || list[1].Version != "8.3" {
		t.Fatalf("unexpected discovered versions: %+v", list)
	}

	inst83, ok := FindVersion(list, "8.3")
	if !ok {
		t.Fatal("expected 8.3 in discovered list")
	}
	if err := ActivateShim(paths.BinDir(), inst83); err != nil {
		t.Fatalf("ActivateShim failed: %v", err)
	}

	if runtime.GOOS == "windows" {
		shimBytes, err := os.ReadFile(filepath.Join(paths.BinDir(), "php.cmd"))
		if err != nil {
			t.Fatalf("expected php.cmd shim: %v", err)
		}
		if !strings.Contains(string(shimBytes), inst83.CLI) || !strings.Contains(string(shimBytes), "%*") {
			t.Errorf("unexpected php.cmd content: %q", string(shimBytes))
		}
		if _, err := os.Stat(filepath.Join(paths.BinDir(), "php-cgi.cmd")); err != nil {
			t.Errorf("expected php-cgi.cmd shim: %v", err)
		}
	} else {
		target, err := os.Readlink(filepath.Join(paths.BinDir(), "php"))
		if err != nil || target != inst83.CLI {
			t.Errorf("php symlink = (%q, %v), want %q", target, err, inst83.CLI)
		}
	}
}
