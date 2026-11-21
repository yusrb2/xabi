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
	"github.com/yusrb2/xabi/internal/hosts"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

func TestSitesAddLsOpenRmWorkflow(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	hostsFile := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	laravelDir := filepath.Join(t.TempDir(), "Shop App")
	if err := os.MkdirAll(filepath.Join(laravelDir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laravelDir, "public", "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}

	fc := &fakeController{
		statusList: []daemon.ServiceStatus{
			{Name: daemon.ServiceNginx, State: daemon.StateRunning, PID: 777},
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
	}

	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	// 1. Add Laravel project via --dir without explicit name -> shop-app.test pointing to /public
	if code := NewAddCommand(deps).Run(context.Background(), []string{"--dir", laravelDir}, streams); code != cli.ExitOK {
		t.Fatalf("add laravel failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: linked http://shop-app.test -> ") || !strings.Contains(out.String(), "/public") {
		t.Errorf("unexpected add output: %q", out.String())
	}
	if !fc.reloadFlag || len(fc.restartCalled) != 1 || fc.restartCalled[0] != daemon.ServiceNginx {
		t.Errorf("expected running nginx to reload after add, got reload=%v services=%v", fc.reloadFlag, fc.restartCalled)
	}
	confBytes, err := os.ReadFile(vhost.SiteConfPath(paths, "shop-app.test"))
	if err != nil {
		t.Fatalf("expected vhost conf to exist: %v", err)
	}
	if !strings.Contains(string(confBytes), "/public") {
		t.Errorf("expected /public root in vhost conf:\n%s", string(confBytes))
	}

	// 2. Add reverse proxy site with interspersed flags (`xabi add api --proxy 3000`)
	out.Reset()
	errBuf.Reset()
	if code := NewAddCommand(deps).Run(context.Background(), []string{"api", "--proxy", "3000"}, streams); code != cli.ExitOK {
		t.Fatalf("add proxy failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: linked http://api.test -> http://127.0.0.1:3000") {
		t.Errorf("unexpected proxy add output: %q", out.String())
	}

	// 3. List registered sites (`xabi ls`)
	out.Reset()
	if code := NewLsCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitOK {
		t.Fatalf("ls failed: %d (%s)", code, errBuf.String())
	}
	for _, want := range []string{"DOMAIN", "api.test", "proxy -> http://127.0.0.1:3000", "shop-app.test", "php ->"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("ls output missing %q:\n%s", want, out.String())
		}
	}

	// 4. Open explicit site and open via current working directory detection
	out.Reset()
	if code := NewOpenCommand(deps).Run(context.Background(), []string{"api"}, streams); code != cli.ExitOK {
		t.Fatalf("open api failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.openedURLs) != 1 || sys.openedURLs[0] != "http://api.test" {
		t.Errorf("openedURLs = %v, want [http://api.test]", sys.openedURLs)
	}

	t.Chdir(laravelDir)
	out.Reset()
	if code := NewOpenCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitOK {
		t.Fatalf("open cwd failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.openedURLs) != 2 || sys.openedURLs[1] != "http://shop-app.test" {
		t.Errorf("openedURLs = %v, want second URL http://shop-app.test", sys.openedURLs)
	}

	// 5. Remove site (`xabi rm shop-app`) including cert cleanup & hosts cleanup
	if err := os.MkdirAll(paths.CertsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(paths.CertsDir(), "shop-app.test.pem")
	if err := os.WriteFile(certFile, []byte("cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := NewRmCommand(deps).Run(context.Background(), []string{"shop-app"}, streams); code != cli.ExitOK {
		t.Fatalf("rm failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: unlinked shop-app.test") {
		t.Errorf("unexpected rm output: %q", out.String())
	}
	if _, err := os.Stat(certFile); !os.IsNotExist(err) {
		t.Error("expected site cert file to be removed on rm")
	}

	domains, err := hosts.NewManager(hostsFile).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 1 || domains[0] != "api.test" {
		t.Errorf("hosts entries = %v, want [api.test]", domains)
	}
}
