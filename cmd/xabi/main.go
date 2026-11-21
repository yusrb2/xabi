package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/xdg"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], cli.DefaultStreams()))
}

func run(args []string, s cli.Streams) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return newRouter(version, xdg.Resolve).Dispatch(ctx, args, s)
}

func newRouter(ver string, _ func() (xdg.Paths, error)) *cli.Router {
	return cli.NewRouter(ver)
}
