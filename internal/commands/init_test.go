package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

type fakeCall struct {
	name string
	args []string
}

type fakeSystem struct {
	bins        map[string]string
	portsInUse  map[int]string
	hosts       sysutil.HostsStatus
	capSt       sysutil.CapStatus
	setCapErr   error
	runErrByBin map[string]error
	calls       []fakeCall
	setCapCalls []string
	openedURLs  []string
}

func (f *fakeSystem) LookBinary(candidates ...string) (string, bool) {
	for _, c := range candidates {
		if p, ok := f.bins[c]; ok {
			return p, true
		}
	}
	return "", false
}

func (f *fakeSystem) EvalSymlinks(path string) (string, error) {
	return path, nil
}

func (f *fakeSystem) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: args})
	if err := f.runErrByBin[name]; err != nil {
		return []byte("command failed"), err
	}
	return nil, nil
}

func (f *fakeSystem) CheckPort(port int, _ time.Duration) sysutil.PortStatus {
	if detail, ok := f.portsInUse[port]; ok {
		return sysutil.PortStatus{Port: port, InUse: true, Detail: detail}
	}
	return sysutil.PortStatus{Port: port, InUse: false}
}

func (f *fakeSystem) CheckHosts(_ string) sysutil.HostsStatus {
	return f.hosts
}

func (f *fakeSystem) HostsPath() string {
	if f.hosts.Path != "" {
		return f.hosts.Path
	}
	return "/etc/hosts"
}

func (f *fakeSystem) CheckNetBindCap(_ string) sysutil.CapStatus {
	return f.capSt
}

func (f *fakeSystem) SetNetBindCap(_ context.Context, binPath string) error {
	f.setCapCalls = append(f.setCapCalls, binPath)
	return f.setCapErr
}

func (f *fakeSystem) IsRoot() bool {
	return false
}

func (f *fakeSystem) OpenURL(_ context.Context, url string) error {
	f.openedURLs = append(f.openedURLs, url)
	return nil
}

func testXDGResolver(t *testing.T) (xdg.Paths, func() (xdg.Paths, error)) {
	t.Helper()
	base := t.TempDir()
	p := xdg.Paths{
		ConfigDir:  filepath.Join(base, "config", "xabi"),
		DataDir:    filepath.Join(base, "share", "xabi"),
		RuntimeDir: filepath.Join(base, "run", "xabi"),
	}
	return p, func() (xdg.Paths, error) {
		return p, nil
	}
}

func TestInitDryRunDoesNotMutateFilesystem(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	sys := &fakeSystem{
		bins: map[string]string{
			"nginx":              "/usr/bin/nginx",
			"php-fpm":            "/usr/bin/php-fpm",
			"mariadb-install-db": "/usr/bin/mariadb-install-db",
			"mkcert":             "/usr/bin/mkcert",
		},
		capSt: sysutil.CapStatus{Supported: true, Set: false},
	}

	var out, errBuf bytes.Buffer
	cmd := NewInitCommand(Deps{ResolveXDG: resolve, Sys: sys})
	code := cmd.Run(context.Background(), []string{"--dry-run"}, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	})
	if code != cli.ExitOK {
		t.Fatalf("dry-run exit code = %d, stderr = %q", code, errBuf.String())
	}

	if _, err := os.Stat(paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run should not create ConfigDir %s", paths.ConfigDir)
	}
	if len(sys.calls) != 0 || len(sys.setCapCalls) != 0 {
		t.Errorf("dry-run should not execute external commands, got calls=%v setcap=%v", sys.calls, sys.setCapCalls)
	}
}

func TestInitCreatesLayoutPreservesManualConfigAndHandlesSetcapPerm(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	sys := &fakeSystem{
		bins: map[string]string{
			"nginx":              "/usr/bin/nginx",
			"php-fpm":            "/usr/bin/php-fpm",
			"mariadb-install-db": "/usr/bin/mariadb-install-db",
			"mkcert":             "/usr/bin/mkcert",
		},
		capSt:     sysutil.CapStatus{Supported: true, Set: false},
		setCapErr: os.ErrPermission,
	}

	var out, errBuf bytes.Buffer
	cmd := NewInitCommand(Deps{ResolveXDG: resolve, Sys: sys})
	code := cmd.Run(context.Background(), nil, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	})
	if code != cli.ExitOK {
		t.Fatalf("init exit code = %d, stderr = %q", code, errBuf.String())
	}

	for _, dir := range []string{
		paths.SitesDir(),
		paths.CertsDir(),
		paths.PHPDir(),
		paths.DBDir(),
		filepath.Join(paths.NginxTempDir(), "fastcgi"),
	} {
		if !sysutil.DirWritable(dir) {
			t.Errorf("expected directory %s to exist and be writable", dir)
		}
	}

	if _, err := os.Stat(paths.ConfigFile()); err != nil {
		t.Fatalf("expected config.toml to exist: %v", err)
	}
	if _, err := os.Stat(paths.NginxConf()); err != nil {
		t.Fatalf("expected nginx.conf to exist: %v", err)
	}
	if !strings.Contains(errBuf.String(), "sudo setcap 'cap_net_bind_service=+ep' /usr/bin/nginx") {
		t.Errorf("expected setcap hint on permission error, got stderr=%q", errBuf.String())
	}
	if len(sys.calls) != 2 {
		t.Errorf("expected mariadb-install-db and mkcert calls, got %+v", sys.calls)
	}

	// Modify config.toml manually and ensure second init without --force preserves it.
	custom := config.Default()
	custom.TLD = "local"
	if err := config.Save(paths.ConfigFile(), custom); err != nil {
		t.Fatalf("Save custom config failed: %v", err)
	}

	out.Reset()
	errBuf.Reset()
	if code := cmd.Run(context.Background(), nil, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	}); code != cli.ExitOK {
		t.Fatalf("second init failed: %d (%s)", code, errBuf.String())
	}

	loaded, err := config.Load(paths.ConfigFile())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.TLD != "local" {
		t.Errorf("expected manual TLD 'local' to be preserved, got %q", loaded.TLD)
	}
}
