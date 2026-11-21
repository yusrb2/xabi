package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/commands"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], cli.DefaultStreams()))
}

func run(args []string, s cli.Streams) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return newRouter(version, xdg.Resolve, sysutil.OS{}).Dispatch(ctx, args, s)
}

func newRouter(ver string, resolveXDG func() (xdg.Paths, error), sys sysutil.System) *cli.Router {
	deps := commands.Deps{
		ResolveXDG: resolveXDG,
		Sys:        sys,
	}
	return cli.NewRouter(
		ver,
		commands.NewInitCommand(deps),
		commands.NewDoctorCommand(deps),
		commands.NewUpCommand(deps),
		commands.NewDownCommand(deps),
		commands.NewRestartCommand(deps),
		commands.NewStatusCommand(deps),
		commands.NewAddCommand(deps),
		commands.NewRmCommand(deps),
		commands.NewLsCommand(deps),
		commands.NewOpenCommand(deps),
	)
}
