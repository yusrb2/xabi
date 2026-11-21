package commands

import (
	"context"
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/sysutil"
)

// NewUpCommand returns the `xabi up` command definition.
func NewUpCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "up",
		Summary: "Start managed user-space services",
		Usage:   "xabi up [service...]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runLifecycle(ctx, "up", args, s, deps, false)
		},
	}
}

// NewDownCommand returns the `xabi down` command definition.
func NewDownCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "down",
		Summary: "Stop managed user-space services",
		Usage:   "xabi down [service...]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runLifecycle(ctx, "down", args, s, deps, false)
		},
	}
}

// NewRestartCommand returns the `xabi restart` command definition.
func NewRestartCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "restart",
		Summary: "Restart or gracefully reload managed services",
		Usage:   "xabi restart [--reload] [service...]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			fs := cli.NewFlagSet("restart", s)
			reload := fs.Bool("reload", false, "Send graceful reload signal (SIGHUP/SIGUSR2) when running")
			if err := fs.Parse(args); err != nil {
				return cli.ExitUsage
			}
			return runLifecycle(ctx, "restart", fs.Args(), s, deps, *reload)
		},
	}
}

// NewStatusCommand returns the `xabi status` (alias: `st`) command definition.
func NewStatusCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "status",
		Aliases: []string{"st"},
		Summary: "Show real-time status, PID, endpoint, and memory usage",
		Usage:   "xabi status [service...]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runStatus(ctx, args, s, deps)
		},
	}
}

func runLifecycle(ctx context.Context, action string, args []string, s cli.Streams, deps Deps, reload bool) int {
	if action != "restart" {
		fs := cli.NewFlagSet(action, s)
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		args = fs.Args()
	}

	mgr, services, err := prepareController(args, deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	var results []daemon.ActionResult
	switch action {
	case "up":
		results, err = mgr.Up(ctx, services)
	case "down":
		results, err = mgr.Down(ctx, services)
	case "restart":
		results, err = mgr.Restart(ctx, services, reload)
	}
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	for _, r := range results {
		if r.PID > 0 && r.Note != "stopped" && r.Note != "already stopped" {
			cli.Info(s.Out, "%s %s (pid %d)", r.Note, r.Name, r.PID)
		} else {
			cli.Info(s.Out, "%s %s", r.Note, r.Name)
		}
	}
	return cli.ExitOK
}

func runStatus(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("status", s)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	mgr, services, err := prepareController(fs.Args(), deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	statuses, err := mgr.Status(ctx, services)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	tw := tabwriter.NewWriter(s.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tSTATE\tPID\tENDPOINT\tMEMORY")
	for _, st := range statuses {
		pidCol := "-"
		if st.PID > 0 {
			pidCol = strconv.Itoa(st.PID)
		}
		memCol := "-"
		if st.State == daemon.StateRunning {
			memCol = daemon.FormatBytes(st.MemoryRSS)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", st.Name, st.State, pidCol, st.Endpoint, memCol)
	}
	_ = tw.Flush()
	return cli.ExitOK
}

func prepareController(requested []string, deps Deps) (daemon.Controller, []string, error) {
	paths, err := deps.ResolveXDG()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve XDG paths: %w", err)
	}
	cfg, _, err := config.LoadOrDefault(paths.ConfigFile())
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", paths.ConfigFile(), err)
	}
	services, err := daemon.ResolveServices(cfg, requested)
	if err != nil {
		return nil, nil, err
	}

	sys := deps.Sys
	if sys == nil {
		sys = sysutil.OS{}
	}
	if deps.NewDaemon != nil {
		return deps.NewDaemon(paths, cfg, sys), services, nil
	}
	return daemon.NewManager(paths, cfg, sys), services, nil
}
