package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

type mkcertFakeSys struct {
	fakeSystem
	mkcertArgs []string
}

func (m *mkcertFakeSys) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if strings.Contains(strings.ToLower(filepath.Base(name)), "mkcert") {
		m.mkcertArgs = append([]string(nil), args...)
		// Parse -cert-file and -key-file to emit real files for verification.
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-cert-file" || args[i] == "-key-file" {
				if err := os.WriteFile(args[i+1], []byte("-----BEGIN TEST-----\n"), 0o644); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}
	return m.fakeSystem.Run(ctx, name, args...)
}

func TestTLSMissingMKCertFailsWithInstallHint(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	siteDir := t.TempDir()
	reg := vhost.Registry{
		Sites: []vhost.Site{{
			Name:   "app",
			Domain: "app.test",
			Kind:   vhost.KindStatic,
			Root:   siteDir,
		}},
	}
	if err := vhost.SaveRegistry(paths.SitesFile(), reg); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	deps := Deps{
		ResolveXDG: resolve,
		Sys:        &fakeSystem{bins: map[string]string{}},
	}
	code := NewTLSCommand(deps).Run(context.Background(), []string{"app"}, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	})
	if code != cli.ExitErr {
		t.Fatalf("expected ExitErr when mkcert missing, got %d", code)
	}
	if !strings.Contains(errBuf.String(), "not found in PATH") {
		t.Errorf("expected install hint in stderr, got %q", errBuf.String())
	}
}

func TestTLSEnableAndDisableWorkflow(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(t.TempDir(), "portal")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}

	fc := &fakeController{
		statusList: []daemon.ServiceStatus{
			{Name: daemon.ServiceNginx, State: daemon.StateRunning, PID: 888},
		},
	}
	sys := &mkcertFakeSys{
		fakeSystem: fakeSystem{
			bins:  map[string]string{"mkcert": "/usr/bin/mkcert"},
			hosts: sysutil.HostsStatus{Path: hostsFile, Exists: true, Readable: true, Writable: true},
		},
	}
	deps := Deps{
		ResolveXDG: resolve,
		Sys:        sys,
		NewDaemon: func(_ xdg.Paths, _ config.Config, _ sysutil.System) daemon.Controller {
			return fc
		},
	}

	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	if code := NewAddCommand(deps).Run(context.Background(), []string{"--dir", projectDir}, streams); code != cli.ExitOK {
		t.Fatalf("add failed: %d (%s)", code, errBuf.String())
	}

	// Enable TLS via auto-detection from cwd
	t.Chdir(projectDir)
	out.Reset()
	errBuf.Reset()
	fc.restartCalled = nil
	fc.reloadFlag = false
	if code := NewTLSCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitOK {
		t.Fatalf("tls enable failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: enabled TLS for https://portal.test") {
		t.Errorf("unexpected tls output: %q", out.String())
	}

	certPath := filepath.Join(paths.CertsDir(), "portal.test.pem")
	keyPath := filepath.Join(paths.CertsDir(), "portal.test-key.pem")
	wantArgs := []string{"-cert-file", certPath, "-key-file", keyPath, "portal.test", "*.portal.test"}
	if strings.Join(sys.mkcertArgs, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("mkcert args = %v, want %v", sys.mkcertArgs, wantArgs)
	}

	confBytes, err := os.ReadFile(vhost.SiteConfPath(paths, "portal.test"))
	if err != nil {
		t.Fatal(err)
	}
	confStr := string(confBytes)
	for _, want := range []string{
		"return 301 https://$host$request_uri;",
		"listen 443 ssl;",
		`ssl_certificate     "` + filepath.ToSlash(certPath) + `";`,
		`ssl_certificate_key "` + filepath.ToSlash(keyPath) + `";`,
	} {
		if !strings.Contains(confStr, want) {
			t.Errorf("HTTPS vhost missing %q:\n%s", want, confStr)
		}
	}

	reg, err := vhost.LoadRegistry(paths.SitesFile())
	if err != nil {
		t.Fatal(err)
	}
	site, ok := reg.FindByDomain("portal.test")
	if !ok || !site.TLS || site.CertFile != certPath || site.KeyFile != keyPath {
		t.Errorf("unexpected registry entry after tls enable: %+v", site)
	}
	if !fc.reloadFlag || len(fc.restartCalled) != 1 || fc.restartCalled[0] != daemon.ServiceNginx {
		t.Errorf("expected nginx reload after tls enable, got reload=%v services=%v", fc.reloadFlag, fc.restartCalled)
	}

	// Disable TLS with --off
	out.Reset()
	errBuf.Reset()
	if code := NewTLSCommand(deps).Run(context.Background(), []string{"portal", "--off"}, streams); code != cli.ExitOK {
		t.Fatalf("tls --off failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: disabled TLS for http://portal.test") {
		t.Errorf("unexpected tls --off output: %q", out.String())
	}
	if _, err := os.Stat(certPath); !os.IsNotExist(err) {
		t.Error("expected cert file to be removed on --off")
	}
	confBytes, _ = os.ReadFile(vhost.SiteConfPath(paths, "portal.test"))
	if strings.Contains(string(confBytes), "listen 443 ssl;") || strings.Contains(string(confBytes), "return 301") {
		t.Errorf("expected HTTP-only vhost after --off:\n%s", string(confBytes))
	}
	reg, _ = vhost.LoadRegistry(paths.SitesFile())
	site, _ = reg.FindByDomain("portal.test")
	if site.TLS || site.CertFile != "" || site.KeyFile != "" {
		t.Errorf("expected TLS cleared in sites.toml, got %+v", site)
	}
}
