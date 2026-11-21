package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
)

// NewShareCommand returns the `xabi share` command definition.
func NewShareCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "share",
		Summary: "Expose a local site via cloudflared or ngrok tunnel",
		Usage:   "xabi share [name] [--provider <cloudflared|ngrok>]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runShare(ctx, args, s, deps)
		},
	}
}

func runShare(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("share", s)
	providerFlag := fs.String("provider", "", "Tunnel provider (cloudflared or ngrok)")
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 1 {
		cli.Error(s.Err, "expected at most one site name")
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	reg, err := vhost.LoadRegistry(paths.SitesFile())
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}

	site, err := resolveRegisteredSite(fs.Args(), reg, cfg.TLD)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	bin, tunnelArgs, err := buildShareCommand(sys, site, cfg, *providerFlag)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	cli.Info(s.Out, "sharing %s via %s (Ctrl+C to stop)", site.Domain, bin)
	if err := sys.RunAttached(ctx, bin, tunnelArgs, s.In, s.Out, s.Err); err != nil && !errors.Is(err, context.Canceled) {
		if ctx.Err() != nil {
			return cli.ExitOK
		}
		cli.Error(s.Err, "tunnel exited with error: %v", err)
		return cli.ExitErr
	}
	return cli.ExitOK
}

func buildShareCommand(sys sysutil.System, site vhost.Site, cfg config.Config, provider string) (string, []string, error) {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p != "" && p != "cloudflared" && p != "ngrok" {
		return "", nil, fmt.Errorf("unsupported tunnel provider %q (expected cloudflared or ngrok)", provider)
	}

	tryCloudflared := func() (string, []string, bool) {
		bin, ok := sys.LookBinary("cloudflared")
		if !ok {
			return "", nil, false
		}
		if site.TLS {
			origin := fmt.Sprintf("https://127.0.0.1:%d", cfg.Ports.HTTPS)
			return bin, []string{"tunnel", "--url", origin, "--http-host-header", site.Domain, "--no-tls-verify"}, true
		}
		origin := fmt.Sprintf("http://127.0.0.1:%d", cfg.Ports.HTTP)
		return bin, []string{"tunnel", "--url", origin, "--http-host-header", site.Domain}, true
	}

	tryNgrok := func() (string, []string, bool) {
		bin, ok := sys.LookBinary("ngrok")
		if !ok {
			return "", nil, false
		}
		if site.TLS {
			origin := fmt.Sprintf("https://127.0.0.1:%d", cfg.Ports.HTTPS)
			return bin, []string{"http", "--host-header=" + site.Domain, origin}, true
		}
		return bin, []string{"http", "--host-header=" + site.Domain, strconv.Itoa(cfg.Ports.HTTP)}, true
	}

	switch p {
	case "cloudflared":
		if bin, args, ok := tryCloudflared(); ok {
			return bin, args, nil
		}
		return "", nil, fmt.Errorf("cloudflared not found in PATH")
	case "ngrok":
		if bin, args, ok := tryNgrok(); ok {
			return bin, args, nil
		}
		return "", nil, fmt.Errorf("ngrok not found in PATH")
	default:
		if bin, args, ok := tryCloudflared(); ok {
			return bin, args, nil
		}
		if bin, args, ok := tryNgrok(); ok {
			return bin, args, nil
		}
		return "", nil, fmt.Errorf("neither cloudflared nor ngrok found in PATH")
	}
}
