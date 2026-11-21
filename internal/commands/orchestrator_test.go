package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

type fakeController struct {
	upCalled      []string
	downCalled    []string
	restartCalled []string
	reloadFlag    bool
	statusList    []daemon.ServiceStatus
	err           error
}

func (f *fakeController) Up(_ context.Context, services []string) ([]daemon.ActionResult, error) {
	f.upCalled = services
	if f.err != nil {
		return nil, f.err
	}
	res := make([]daemon.ActionResult, 0, len(services))
	for i, s := range services {
		res = append(res, daemon.ActionResult{Name: s, PID: 2000 + i, Changed: true, Note: "started"})
	}
	return res, nil
}

func (f *fakeController) Down(_ context.Context, services []string) ([]daemon.ActionResult, error) {
	f.downCalled = services
	if f.err != nil {
		return nil, f.err
	}
	res := make([]daemon.ActionResult, 0, len(services))
	for i := len(services) - 1; i >= 0; i-- {
		res = append(res, daemon.ActionResult{Name: services[i], PID: 2000 + i, Changed: true, Note: "stopped"})
	}
	return res, nil
}

func (f *fakeController) Restart(_ context.Context, services []string, reloadOnly bool) ([]daemon.ActionResult, error) {
	f.restartCalled = services
	f.reloadFlag = reloadOnly
	if f.err != nil {
		return nil, f.err
	}
	note := "restarted"
	if reloadOnly {
		note = "reloaded (SIGHUP)"
	}
	res := make([]daemon.ActionResult, 0, len(services))
	for i, s := range services {
		res = append(res, daemon.ActionResult{Name: s, PID: 3000 + i, Changed: true, Note: note})
	}
	return res, nil
}

func (f *fakeController) Status(_ context.Context, _ []string) ([]daemon.ServiceStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.statusList, nil
}

func TestOrchestratorUpDownRestartStatus(t *testing.T) {
	_, resolve := testXDGResolver(t)
	fc := &fakeController{
		statusList: []daemon.ServiceStatus{
			{Name: daemon.ServiceNginx, State: daemon.StateRunning, PID: 1201, Endpoint: "127.0.0.1:80", MemoryRSS: 6 * 1024 * 1024},
			{Name: daemon.ServicePHPFPM, State: daemon.StateRunning, PID: 1202, Endpoint: "unix:/tmp/php-8.3.sock", MemoryRSS: 18 * 1024 * 1024},
			{Name: daemon.ServiceMariaDB, State: daemon.StateStopped, Endpoint: "127.0.0.1:3306"},
		},
	}

	deps := Deps{
		ResolveXDG: resolve,
		Sys:        &fakeSystem{},
		NewDaemon: func(_ xdg.Paths, _ config.Config, _ sysutil.System) daemon.Controller {
			return fc
		},
	}

	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	// xabi up php (alias normalized to php-fpm)
	if code := NewUpCommand(deps).Run(context.Background(), []string{"php"}, streams); code != cli.ExitOK {
		t.Fatalf("up failed: %d (%s)", code, errBuf.String())
	}
	if len(fc.upCalled) != 1 || fc.upCalled[0] != daemon.ServicePHPFPM {
		t.Errorf("upCalled = %v, want [php-fpm]", fc.upCalled)
	}
	if !strings.Contains(out.String(), ":: started php-fpm (pid 2000)") {
		t.Errorf("unexpected up output: %q", out.String())
	}

	// xabi restart --reload nginx
	out.Reset()
	if code := NewRestartCommand(deps).Run(context.Background(), []string{"--reload", "nginx"}, streams); code != cli.ExitOK {
		t.Fatalf("restart --reload failed: %d (%s)", code, errBuf.String())
	}
	if !fc.reloadFlag || len(fc.restartCalled) != 1 || fc.restartCalled[0] != daemon.ServiceNginx {
		t.Errorf("unexpected restart args: reload=%v services=%v", fc.reloadFlag, fc.restartCalled)
	}

	// xabi status
	out.Reset()
	if code := NewStatusCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitOK {
		t.Fatalf("status failed: %d (%s)", code, errBuf.String())
	}
	for _, want := range []string{"SERVICE", "nginx", "RUNNING", "1201", "6.0 MiB", "mariadb", "STOPPED"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, out.String())
		}
	}

	// xabi down
	out.Reset()
	if code := NewDownCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitOK {
		t.Fatalf("down failed: %d (%s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ":: stopped mariadb") {
		t.Errorf("unexpected down output: %q", out.String())
	}

	// Unknown service & daemon error
	errBuf.Reset()
	if code := NewUpCommand(deps).Run(context.Background(), []string{"redis"}, streams); code != cli.ExitErr {
		t.Fatalf("expected ExitErr for unknown service, got %d", code)
	}
	fc.err = errors.New("boom")
	if code := NewUpCommand(deps).Run(context.Background(), nil, streams); code != cli.ExitErr {
		t.Fatalf("expected ExitErr on controller error, got %d", code)
	}
}
