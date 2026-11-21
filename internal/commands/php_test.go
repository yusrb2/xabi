package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/php"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

func TestPHPListSwitchPinAndDotPHPVersion(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	php83CLI := filepath.Join(paths.PHPDataDir(), "8.3", "php")
	php83FPM := filepath.Join(paths.PHPDataDir(), "8.3", "php-fpm")
	php84CLI := filepath.Join(paths.PHPDataDir(), "8.4", "php")
	php84FPM := filepath.Join(paths.PHPDataDir(), "8.4", "php-fpm")
	if runtime.GOOS == "windows" {
		php83CLI += ".exe"
		php83FPM = filepath.Join(paths.PHPDataDir(), "8.3", "php-cgi.exe")
		php84CLI += ".exe"
		php84FPM = filepath.Join(paths.PHPDataDir(), "8.4", "php-cgi.exe")
	}
	for _, f := range []string{php83CLI, php83FPM, php84CLI, php84FPM} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fc := &fakeController{
		statusList: []daemon.ServiceStatus{
			{Name: daemon.ServicePHPFPM, State: daemon.StateRunning, PID: 501},
			{Name: daemon.ServiceNginx, State: daemon.StateRunning, PID: 502},
		},
	}
	sys := &fakeSystem{
		hosts: sysutil.HostsStatus{Path: hostsFile, Exists: true, Readable: true, Writable: true},
	}

	deps := Deps{
		ResolveXDG: resolve,
		Sys:        sys,
		NewDaemon: func(_ xdg.Paths, _ config.Config, _ sysutil.System) daemon.Controller {
			return fc
		},
		DiscoverPHP: func(_ context.Context, _ xdg.Paths, _ sysutil.System) []php.Installation {
			return []php.Installation{
				{Version: "8.3", CLI: php83CLI, FPM: php83FPM, Source: "xabi"},
				{Version: "8.4", CLI: php84CLI, FPM: php84FPM, Source: "xabi"},
			}
		},
	}

	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	// 1. Switch global PHP to 8.4 -> updates config.toml, creates shim/symlink, refreshes running services
	if code := NewPHPCommand(deps).Run(context.Background(), []string{"8.4"}, streams); code != cli.ExitOK {
		t.Fatalf("xabi php 8.4 failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: active PHP set to 8.4") {
		t.Errorf("unexpected switch output: %q", out.String())
	}
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil || cfg.PHPDefault != "8.4" {
		t.Errorf("expected config.php_default = 8.4, got %+v (err=%v)", cfg, err)
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join(paths.BinDir(), "php.cmd")); err != nil {
			t.Errorf("expected php.cmd shim: %v", err)
		}
	} else {
		if target, err := os.Readlink(filepath.Join(paths.BinDir(), "php")); err != nil || target != php84CLI {
			t.Errorf("expected php symlink -> %s, got (%q, %v)", php84CLI, target, err)
		}
	}

	// 2. Register a project in subdir inheriting parent .php-version
	projectRoot := filepath.Join(t.TempDir(), "webapp")
	subDir := filepath.Join(projectRoot, "service")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := php.WriteDotPHPVersion(projectRoot, "8.3"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := NewAddCommand(deps).Run(context.Background(), []string{"--dir", subDir, "webapp"}, streams); code != cli.ExitOK {
		t.Fatalf("add with parent .php-version failed: %d (%s)", code, errBuf.String())
	}

	reg, err := vhost.LoadRegistry(paths.SitesFile())
	if err != nil {
		t.Fatal(err)
	}
	site, ok := reg.FindByDomain("webapp.test")
	if !ok || site.PHP != "8.3" {
		t.Fatalf("expected webapp.test PHP=8.3 from parent .php-version, got %+v", site)
	}

	// 3. `xabi php pin 8.4` inside registered project updates .php-version, sites.toml, and nginx .conf
	t.Chdir(subDir)
	out.Reset()
	errBuf.Reset()
	if code := NewPHPCommand(deps).Run(context.Background(), []string{"pin", "8.4"}, streams); code != cli.ExitOK {
		t.Fatalf("xabi php pin 8.4 failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: pinned PHP 8.4 for webapp.test (.php-version)") {
		t.Errorf("unexpected pin output: %q", out.String())
	}

	dotVer, found, err := php.ReadDotPHPVersion(subDir)
	if err != nil || !found || dotVer != "8.4" {
		t.Errorf("expected .php-version = 8.4 in %s, got (%q, %v, %v)", subDir, dotVer, found, err)
	}
	confBytes, _ := os.ReadFile(vhost.SiteConfPath(paths, "webapp.test"))
	if runtime.GOOS == "windows" {
		if !strings.Contains(string(confBytes), "fastcgi_pass 127.0.0.1:9084;") {
			t.Errorf("expected fastcgi_pass 127.0.0.1:9084 in pinned vhost:\n%s", string(confBytes))
		}
	} else {
		if !strings.Contains(string(confBytes), "php-8.4.sock;") {
			t.Errorf("expected php-8.4.sock in pinned vhost:\n%s", string(confBytes))
		}
	}

	// 4. `xabi php` lists active global (*) and current project .php-version
	out.Reset()
	if code := NewPHPCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitOK {
		t.Fatalf("xabi php list failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "* 8.4") || !strings.Contains(out.String(), ":: project .php-version: 8.4") {
		t.Errorf("unexpected php list output:\n%s", out.String())
	}
}
