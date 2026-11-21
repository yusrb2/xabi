package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/sysutil"
)

func TestDoctorPassesWhenSystemReady(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	initSys := &fakeSystem{
		bins: map[string]string{
			"nginx":       "/usr/bin/nginx",
			"php-fpm":     "/usr/bin/php-fpm",
			"mariadbd":    "/usr/bin/mariadbd",
			"mkcert":      "/usr/bin/mkcert",
			"cloudflared": "/usr/bin/cloudflared",
		},
		hosts: sysutil.HostsStatus{Path: "/etc/hosts", Exists: true, Readable: true, Writable: true},
		capSt: sysutil.CapStatus{Supported: true, Set: true},
	}

	// Provision XDG layout first via init.
	initCmd := NewInitCommand(Deps{ResolveXDG: resolve, Sys: initSys})
	var sink bytes.Buffer
	if code := initCmd.Run(context.Background(), nil, cli.Streams{
		In: strings.NewReader(""), Out: &sink, Err: &sink,
	}); code != cli.ExitOK {
		t.Fatalf("init failed: %d (%s)", code, sink.String())
	}

	var out, errBuf bytes.Buffer
	docCmd := NewDoctorCommand(Deps{ResolveXDG: resolve, Sys: initSys})
	code := docCmd.Run(context.Background(), nil, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	})
	if code != cli.ExitOK {
		t.Fatalf("doctor exit code = %d, out=%q err=%q", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), paths.ConfigDir) || !strings.Contains(out.String(), ":: system ready") {
		t.Errorf("unexpected doctor output: %q", out.String())
	}
}

func TestDoctorFailsOnPortConflictAndMissingRequiredBinary(t *testing.T) {
	_, resolve := testXDGResolver(t)
	sys := &fakeSystem{
		bins: map[string]string{},
		portsInUse: map[int]string{
			80:   "listening on 127.0.0.1:80",
			3306: "listening on 127.0.0.1:3306",
		},
		hosts: sysutil.HostsStatus{Path: "/etc/hosts", Exists: true, Readable: true, Writable: false},
	}

	var out, errBuf bytes.Buffer
	docCmd := NewDoctorCommand(Deps{ResolveXDG: resolve, Sys: sys})
	code := docCmd.Run(context.Background(), nil, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	})
	if code != cli.ExitErr {
		t.Fatalf("expected ExitErr on failures, got %d", code)
	}
	if !strings.Contains(out.String(), "✖ port 80 (http)") {
		t.Errorf("expected port 80 conflict failure in output: %q", out.String())
	}
	if !strings.Contains(out.String(), "! hosts file") {
		t.Errorf("expected read-only hosts warning in output: %q", out.String())
	}
	if !strings.Contains(errBuf.String(), "critical issue(s)") {
		t.Errorf("expected summary error on stderr, got %q", errBuf.String())
	}
}
