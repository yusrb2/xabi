package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

// NewLogCommand returns the `xabi log` command definition.
func NewLogCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "log",
		Summary: "Stream raw service or site logs",
		Usage:   "xabi log [nginx|php|mariadb|<site>] [-n <lines>] [--no-follow] [--access]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runLog(ctx, args, s, deps)
		},
	}
}

func runLog(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("log", s)
	lines := fs.Int("n", 20, "Number of trailing lines to display")
	followFlag := fs.Bool("f", true, "Follow log output as new lines are appended")
	noFollow := fs.Bool("no-follow", false, "Print trailing lines and exit")
	access := fs.Bool("access", false, "Read nginx access log instead of error log")
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 1 {
		cli.Error(s.Err, "expected at most one log target")
		return cli.ExitUsage
	}
	if *lines < 0 {
		cli.Error(s.Err, "-n must be >= 0")
		return cli.ExitUsage
	}

	paths, cfg, _, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	target := "nginx"
	if fs.NArg() == 1 {
		target = strings.TrimSpace(fs.Arg(0))
	}

	logPath, err := resolveLogPath(target, *access, paths, cfg)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	follow := *followFlag && !*noFollow
	if err := tailFile(ctx, logPath, *lines, follow, 50*time.Millisecond, s.Out); err != nil && !errors.Is(err, context.Canceled) {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}
	return cli.ExitOK
}

func resolveLogPath(target string, access bool, paths xdg.Paths, cfg config.Config) (string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "nginx":
		if access {
			return paths.LogFile("nginx-access"), nil
		}
		return paths.LogFile(daemon.ServiceNginx), nil
	case "php", "php-fpm", "php-cgi":
		return paths.LogFile(daemon.ServicePHPFPM), nil
	case "mariadb", "mariadbd", "mysql", "mysqld", "db":
		return paths.LogFile(daemon.ServiceMariaDB), nil
	default:
		if strings.HasPrefix(strings.ToLower(target), "php-") {
			return paths.LogFile("php-fpm-" + strings.TrimPrefix(strings.ToLower(target), "php-")), nil
		}
		_, domain, err := vhost.NormalizeSiteName(target, cfg.TLD)
		if err != nil {
			return "", fmt.Errorf("unknown log target %q", target)
		}
		siteLog := paths.LogFile(domain)
		if fileExists(siteLog) {
			return siteLog, nil
		}
		reg, regErr := vhost.LoadRegistry(paths.SitesFile())
		if regErr == nil {
			if _, ok := reg.FindByDomain(domain); ok {
				if access {
					return paths.LogFile("nginx-access"), nil
				}
				return paths.LogFile(daemon.ServiceNginx), nil
			}
		}
		return "", fmt.Errorf("unknown log target or site %q", target)
	}
}

func tailFile(ctx context.Context, path string, n int, follow bool, poll time.Duration, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("log file %s does not exist (start the service with 'xabi up')", path)
		}
		return err
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}

	offset, err := writeLastLines(f, info.Size(), n, out)
	_ = f.Close()
	if err != nil {
		return err
	}
	if !follow {
		return nil
	}

	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			size := st.Size()
			if size < offset {
				// File was truncated or rotated.
				offset = 0
			}
			if size == offset {
				continue
			}
			rf, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := rf.Seek(offset, io.SeekStart); err == nil {
				written, _ := io.Copy(out, rf)
				offset += written
			}
			_ = rf.Close()
		}
	}
}

func writeLastLines(f *os.File, size int64, n int, out io.Writer) (int64, error) {
	if size == 0 || n == 0 {
		return size, nil
	}

	const chunkSize int64 = 4096
	var pos = size
	var newlines int
	var startOffset int64

	buf := make([]byte, chunkSize)
	for pos > 0 {
		readSize := chunkSize
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return size, err
		}
		rn, err := io.ReadFull(f, buf[:readSize])
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return size, err
		}

		for i := rn - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			// Ignore a trailing newline at the very end of the file.
			if pos+int64(i) == size-1 {
				continue
			}
			newlines++
			if newlines >= n {
				startOffset = pos + int64(i) + 1
				pos = 0
				break
			}
		}
	}

	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return size, err
	}
	if _, err := io.Copy(out, f); err != nil {
		return size, err
	}
	return size, nil
}
