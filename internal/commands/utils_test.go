package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/php"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

func TestConfTargetsPathAndEditor(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(paths.ConfigFile(), config.Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.NginxDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.NginxConf(), []byte("# nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.SitesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	siteConf := vhost.SiteConfPath(paths, "app.test")
	if err := os.WriteFile(siteConf, []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	phpDir := filepath.Join(paths.PHPDataDir(), "8.3")
	if err := os.MkdirAll(phpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	phpIni := filepath.Join(phpDir, "php.ini")
	if err := os.WriteFile(phpIni, []byte("[PHP]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.PHPDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	fpmConf := filepath.Join(paths.PHPDir(), "php-fpm.conf")
	if err := os.WriteFile(fpmConf, []byte("[global]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sys := &fakeSystem{}
	deps := Deps{
		ResolveXDG: resolve,
		Sys:        sys,
		DiscoverPHP: func(_ context.Context, _ xdg.Paths, _ sysutil.System) []php.Installation {
			return []php.Installation{{Version: "8.3", CLI: filepath.Join(phpDir, "php"), FPM: filepath.Join(phpDir, "php-cgi")}}
		},
	}

	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	// --path for config, nginx, site, php
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--path"}, paths.ConfigFile()},
		{[]string{"nginx", "--path"}, paths.NginxConf()},
		{[]string{"app", "--path"}, siteConf},
	} {
		out.Reset()
		if code := NewConfCommand(deps).Run(context.Background(), tc.args, streams); code != cli.ExitOK {
			t.Fatalf("conf %v failed: %d (%s)", tc.args, code, errBuf.String())
		}
		if got := strings.TrimSpace(out.String()); got != tc.want {
			t.Errorf("conf %v = %q, want %q", tc.args, got, tc.want)
		}
	}

	out.Reset()
	if code := NewConfCommand(deps).Run(context.Background(), []string{"php", "--path"}, streams); code != cli.ExitOK {
		t.Fatalf("conf php --path failed: %d (%s)", code, errBuf.String())
	}
	wantPHPConf := fpmConf
	if runtime.GOOS == "windows" {
		wantPHPConf = phpIni
	}
	if got := strings.TrimSpace(out.String()); got != wantPHPConf {
		t.Errorf("conf php --path = %q, want %q", got, wantPHPConf)
	}

	// Launches $EDITOR with interspersed flags
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "code --wait")
	if code := NewConfCommand(deps).Run(context.Background(), []string{"nginx"}, streams); code != cli.ExitOK {
		t.Fatalf("conf nginx editor launch failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.attachedCalls) != 1 || sys.attachedCalls[0].name != "code" {
		t.Fatalf("expected editor 'code', got %+v", sys.attachedCalls)
	}
	gotArgs := sys.attachedCalls[0].args
	if len(gotArgs) != 2 || gotArgs[0] != "--wait" || gotArgs[1] != paths.NginxConf() {
		t.Errorf("unexpected editor args: %v", gotArgs)
	}
}

func TestLogTailNoFollowAndLiveStream(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}

	nginxErrLog := paths.LogFile(daemon.ServiceNginx)
	if err := os.WriteFile(nginxErrLog, []byte("line1\nline2\nline3\nline4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nginxAccLog := paths.LogFile("nginx-access")
	if err := os.WriteFile(nginxAccLog, []byte("GET / 200\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := Deps{ResolveXDG: resolve, Sys: &fakeSystem{}}
	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	// -n 2 --no-follow
	if code := NewLogCommand(deps).Run(context.Background(), []string{"nginx", "-n", "2", "--no-follow"}, streams); code != cli.ExitOK {
		t.Fatalf("log --no-follow failed: %d (%s)", code, errBuf.String())
	}
	if out.String() != "line3\nline4\n" {
		t.Errorf("unexpected tail output: %q", out.String())
	}

	// --access --no-follow
	out.Reset()
	if code := NewLogCommand(deps).Run(context.Background(), []string{"nginx", "--access", "--no-follow"}, streams); code != cli.ExitOK {
		t.Fatalf("log --access failed: %d (%s)", code, errBuf.String())
	}
	if out.String() != "GET / 200\n" {
		t.Errorf("unexpected access log output: %q", out.String())
	}

	// Live follow streaming via tailFile
	var streamBuf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- tailFile(ctx, nginxErrLog, 1, true, 10*time.Millisecond, &streamBuf)
	}()

	time.Sleep(30 * time.Millisecond)
	f, err := os.OpenFile(nginxErrLog, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_, _ = f.WriteString("line5-streamed\n")
	_ = f.Close()

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(streamBuf.String(), "line5-streamed") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("tailFile returned error: %v", err)
	}
	if !strings.Contains(streamBuf.String(), "line4\nline5-streamed\n") {
		t.Errorf("expected initial + streamed lines, got %q", streamBuf.String())
	}
}

func TestShareCloudflaredAndNgrokFallback(t *testing.T) {
	paths, resolve := testXDGResolver(t)
	projectDir := t.TempDir()
	reg := vhost.Registry{
		Sites: []vhost.Site{
			{Name: "app", Domain: "app.test", Kind: vhost.KindPHP, Root: projectDir},
			{Name: "sec", Domain: "sec.test", Kind: vhost.KindPHP, Root: projectDir, TLS: true},
		},
	}
	if err := vhost.SaveRegistry(paths.SitesFile(), reg); err != nil {
		t.Fatal(err)
	}

	// 1. Prefers cloudflared when both present
	sys := &fakeSystem{
		bins: map[string]string{
			"cloudflared": "/usr/bin/cloudflared",
			"ngrok":       "/usr/bin/ngrok",
		},
	}
	deps := Deps{ResolveXDG: resolve, Sys: sys}
	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}

	if code := NewShareCommand(deps).Run(context.Background(), []string{"app"}, streams); code != cli.ExitOK {
		t.Fatalf("share app failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.attachedCalls) != 1 || sys.attachedCalls[0].name != "/usr/bin/cloudflared" {
		t.Fatalf("expected cloudflared priority, got %+v", sys.attachedCalls)
	}
	wantCF := "tunnel --url http://127.0.0.1:80 --http-host-header app.test"
	if got := strings.Join(sys.attachedCalls[0].args, " "); got != wantCF {
		t.Errorf("cloudflared args = %q, want %q", got, wantCF)
	}

	// 2. Fallback to ngrok when cloudflared missing
	sysNgrok := &fakeSystem{bins: map[string]string{"ngrok": "/usr/bin/ngrok"}}
	if code := NewShareCommand(Deps{ResolveXDG: resolve, Sys: sysNgrok}).Run(context.Background(), []string{"app"}, streams); code != cli.ExitOK {
		t.Fatalf("share ngrok fallback failed: %d (%s)", code, errBuf.String())
	}
	wantNgrok := "http --host-header=app.test 80"
	if got := strings.Join(sysNgrok.attachedCalls[0].args, " "); got != wantNgrok {
		t.Errorf("ngrok args = %q, want %q", got, wantNgrok)
	}

	// 3. TLS site passes --no-tls-verify and https origin to cloudflared
	sys.attachedCalls = nil
	if code := NewShareCommand(deps).Run(context.Background(), []string{"sec"}, streams); code != cli.ExitOK {
		t.Fatalf("share TLS site failed: %d (%s)", code, errBuf.String())
	}
	wantTLS := "tunnel --url https://127.0.0.1:443 --http-host-header sec.test --no-tls-verify"
	if got := strings.Join(sys.attachedCalls[0].args, " "); got != wantTLS {
		t.Errorf("TLS cloudflared args = %q, want %q", got, wantTLS)
	}
}

func TestDBCommandsAndConnArgs(t *testing.T) {
	paths := xdg.Paths{RuntimeDir: "/run/user/1000/xabi"}
	cfg := config.Default()

	linArgs := strings.Join(dbConnectionArgs(paths, cfg, "linux"), " ")
	wantLin := "--socket=" + paths.SocketFile("mysql") + " --user=root"
	if linArgs != wantLin {
		t.Errorf("linux db args = %q, want %q", linArgs, wantLin)
	}
	winArgs := strings.Join(dbConnectionArgs(paths, cfg, "windows"), " ")
	if winArgs != "--host=127.0.0.1 --port=3306 --user=root" {
		t.Errorf("windows db args = %q", winArgs)
	}

	_, resolve := testXDGResolver(t)
	sys := &fakeSystem{
		bins: map[string]string{"mariadb": "/usr/bin/mariadb"},
	}
	deps := Deps{ResolveXDG: resolve, Sys: sys}

	var out, errBuf bytes.Buffer

	// Interactive shell
	if code := NewDBCommand(deps).Run(context.Background(), nil, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	}); code != cli.ExitOK {
		t.Fatalf("db shell failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.attachedCalls) != 1 || sys.attachedCalls[0].name != "/usr/bin/mariadb" {
		t.Fatalf("expected interactive mariadb call, got %+v", sys.attachedCalls)
	}

	// create <db>
	out.Reset()
	if code := NewDBCommand(deps).Run(context.Background(), []string{"create", "shop_db"}, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	}); code != cli.ExitOK {
		t.Fatalf("db create failed: %d (%s)", code, errBuf.String())
	}
	last := sys.calls[len(sys.calls)-1]
	joined := strings.Join(last.args, " ")
	if !strings.Contains(joined, "CREATE DATABASE IF NOT EXISTS `shop_db`") {
		t.Errorf("unexpected create SQL args: %v", last.args)
	}

	// drop <db> aborts without confirmation 'y'
	out.Reset()
	sys.calls = nil
	if code := NewDBCommand(deps).Run(context.Background(), []string{"drop", "shop_db"}, cli.Streams{
		In: strings.NewReader("n\n"), Out: &out, Err: &errBuf,
	}); code != cli.ExitOK {
		t.Fatalf("db drop abort failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.calls) != 0 || !strings.Contains(out.String(), ":: aborted") {
		t.Errorf("expected aborted drop without SQL exec, calls=%v out=%q", sys.calls, out.String())
	}

	// drop <db> --force executes SQL
	out.Reset()
	if code := NewDBCommand(deps).Run(context.Background(), []string{"drop", "shop_db", "--force"}, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	}); code != cli.ExitOK {
		t.Fatalf("db drop --force failed: %d (%s)", code, errBuf.String())
	}
	if len(sys.calls) != 1 || !strings.Contains(strings.Join(sys.calls[0].args, " "), "DROP DATABASE IF EXISTS `shop_db`;") {
		t.Errorf("expected DROP DATABASE call, got %+v", sys.calls)
	}

	// Reject SQL injection identifier
	errBuf.Reset()
	if code := NewDBCommand(deps).Run(context.Background(), []string{"create", "bad;DROP"}, cli.Streams{
		In: strings.NewReader(""), Out: &out, Err: &errBuf,
	}); code != cli.ExitErr {
		t.Fatalf("expected ExitErr on invalid identifier, got %d", code)
	}
}

func TestCompletionAllShells(t *testing.T) {
	var out, errBuf bytes.Buffer
	streams := cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errBuf}
	cmd := NewCompletionCommand()

	for _, tc := range []struct {
		shell string
		want  string
	}{
		{"bash", "_xabi_completions"},
		{"zsh", "#compdef xabi"},
		{"fish", "complete -c xabi"},
		{"powershell", "Register-ArgumentCompleter"},
		{"pwsh", "Register-ArgumentCompleter"},
	} {
		out.Reset()
		errBuf.Reset()
		if code := cmd.Run(context.Background(), []string{tc.shell}, streams); code != cli.ExitOK {
			t.Fatalf("completion %s failed: %d (%s)", tc.shell, code, errBuf.String())
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("completion %s missing %q", tc.shell, tc.want)
		}
	}

	if code := cmd.Run(context.Background(), []string{"csh"}, streams); code != cli.ExitUsage {
		t.Fatalf("expected ExitUsage for unsupported shell, got %d", code)
	}
}
