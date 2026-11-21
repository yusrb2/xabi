package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

type checkStatus uint8

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

// NewDoctorCommand returns the `xabi doctor` command definition.
func NewDoctorCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "doctor",
		Summary: "Verify binaries, ports, XDG layout, and /etc/hosts permissions",
		Usage:   "xabi doctor",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runDoctor(ctx, args, s, deps)
		},
	}
}

func runDoctor(_ context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("doctor", s)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		cli.Error(s.Err, "unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}

	sys := deps.Sys
	if sys == nil {
		sys = sysutil.OS{}
	}

	failures := 0
	record := func(st checkStatus, label, detail string) {
		switch st {
		case statusOK:
			fmt.Fprintf(s.Out, "  ✔ %-16s %s\n", label, detail)
		case statusWarn:
			fmt.Fprintf(s.Out, "  ! %-16s %s\n", label, detail)
		case statusFail:
			failures++
			fmt.Fprintf(s.Out, "  ✖ %-16s %s\n", label, detail)
		}
	}

	paths, err := deps.ResolveXDG()
	if err != nil {
		cli.Error(s.Err, "resolve XDG paths: %v", err)
		return cli.ExitErr
	}

	cfg, cfgExists, cfgErr := config.LoadOrDefault(paths.ConfigFile())

	cli.Step(s.Out, "XDG environment")
	checkXDGLayout(paths, cfgExists, cfgErr, record)

	cli.Step(s.Out, "System binaries")
	checkBinaries(sys, cfg, record)

	cli.Step(s.Out, "Port availability")
	checkPorts(sys, cfg, record)

	cli.Step(s.Out, "Hosts & capabilities")
	checkHostsAndCaps(sys, cfg, record)

	if failures > 0 {
		cli.Error(s.Err, "doctor found %d critical issue(s)", failures)
		return cli.ExitErr
	}
	cli.Info(s.Out, "system ready")
	return cli.ExitOK
}

func checkXDGLayout(paths xdg.Paths, cfgExists bool, cfgErr error, record func(checkStatus, string, string)) {
	checkDir := func(name, dir string) {
		if sysutil.DirWritable(dir) {
			record(statusOK, name, dir)
		} else {
			record(statusFail, name, fmt.Sprintf("%s (missing or not writable; run 'xabi init')", dir))
		}
	}
	checkDir("config dir", paths.ConfigDir)
	checkDir("data dir", paths.DataDir)
	checkDir("runtime dir", paths.RuntimeDir)

	switch {
	case cfgErr != nil:
		record(statusFail, "config.toml", cfgErr.Error())
	case !cfgExists:
		record(statusWarn, "config.toml", fmt.Sprintf("%s not found (using defaults; run 'xabi init')", paths.ConfigFile()))
	default:
		record(statusOK, "config.toml", paths.ConfigFile())
	}

	if fileExists(paths.NginxConf()) {
		record(statusOK, "nginx.conf", paths.NginxConf())
	} else {
		record(statusWarn, "nginx.conf", fmt.Sprintf("%s missing (run 'xabi init')", paths.NginxConf()))
	}
}

func checkBinaries(sys sysutil.System, cfg config.Config, record func(checkStatus, string, string)) {
	type binCheck struct {
		label      string
		candidates []string
		required   bool
		note       string
	}

	webBins := []string{"nginx"}
	if cfg.WebServer == "caddy" {
		webBins = []string{"caddy"}
	}

	checks := []binCheck{
		{label: cfg.WebServer, candidates: webBins, required: true, note: "required web server"},
		{label: "php-fpm", candidates: phpFPMBinCandidates, required: false, note: "needed for PHP sites"},
		{label: "mariadb/mysql", candidates: dbDaemonCandidates, required: false, note: "needed for local DB"},
		{label: "mkcert", candidates: []string{"mkcert"}, required: false, note: "needed for 'xabi tls'"},
		{label: "cloudflared", candidates: []string{"cloudflared"}, required: false, note: "needed for 'xabi share'"},
	}

	for _, c := range checks {
		if p, ok := sys.LookBinary(c.candidates...); ok {
			record(statusOK, c.label, p)
			continue
		}
		if c.required {
			record(statusFail, c.label, fmt.Sprintf("not found in PATH (%s)", c.note))
		} else {
			record(statusWarn, c.label, fmt.Sprintf("not found in PATH (%s)", c.note))
		}
	}
}

func checkPorts(sys sysutil.System, cfg config.Config, record func(checkStatus, string, string)) {
	targets := []struct {
		label string
		port  int
	}{
		{label: "http", port: cfg.Ports.HTTP},
		{label: "https", port: cfg.Ports.HTTPS},
		{label: "db", port: cfg.Ports.DB},
	}

	results := make([]sysutil.PortStatus, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(idx, port int) {
			defer wg.Done()
			results[idx] = sys.CheckPort(port, 40*time.Millisecond)
		}(i, t.port)
	}
	wg.Wait()

	for i, t := range targets {
		st := results[i]
		label := fmt.Sprintf("port %d (%s)", st.Port, t.label)
		if st.InUse {
			record(statusFail, label, fmt.Sprintf("in use (%s)", st.Detail))
		} else {
			record(statusOK, label, "free")
		}
	}
}

func checkHostsAndCaps(sys sysutil.System, cfg config.Config, record func(checkStatus, string, string)) {
	hosts := sys.CheckHosts(sys.HostsPath())
	switch {
	case !hosts.Exists:
		record(statusFail, "hosts file", fmt.Sprintf("%s not found", hosts.Path))
	case !hosts.Readable:
		record(statusFail, "hosts file", fmt.Sprintf("%s not readable (%v)", hosts.Path, hosts.Err))
	case !hosts.Writable:
		record(statusWarn, "hosts file", fmt.Sprintf("%s is read-only for current user (grant write permission/ACL for 'xabi add')", hosts.Path))
	default:
		record(statusOK, "hosts file", fmt.Sprintf("%s (read/write)", hosts.Path))
	}

	if cfg.Ports.HTTP >= 1024 && cfg.Ports.HTTPS >= 1024 {
		return
	}

	binPath, ok := sys.LookBinary(cfg.WebServer)
	if !ok {
		return
	}
	if resolved, err := sys.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}
	capSt := sys.CheckNetBindCap(binPath)
	if !capSt.Supported {
		return
	}
	if capSt.Err != nil {
		record(statusWarn, "net_bind_cap", fmt.Sprintf("unable to check %s (%v)", binPath, capSt.Err))
		return
	}
	if capSt.Set {
		record(statusOK, "net_bind_cap", fmt.Sprintf("%s has cap_net_bind_service", binPath))
	} else {
		record(statusWarn, "net_bind_cap", fmt.Sprintf("%s lacks cap_net_bind_service (run: sudo setcap 'cap_net_bind_service=+ep' %s)", binPath, binPath))
	}
}
