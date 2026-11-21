package commands

import (
	"bufio"
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/xdg"
)

// NewDBCommand returns the `xabi db` command definition.
func NewDBCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "db",
		Summary: "Manage local databases or open an interactive SQL shell",
		Usage:   "xabi db [shell [name] | ls | create <name> | drop <name> [--force]]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runDB(ctx, args, s, deps)
		},
	}
}

func runDB(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("db", s)
	force := fs.Bool("force", false, "Skip confirmation when dropping a database")
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	clientBin, ok := sys.LookBinary("mariadb", "mysql")
	if !ok {
		cli.Error(s.Err, "mariadb/mysql client not found in PATH")
		return cli.ExitErr
	}

	baseArgs := dbConnectionArgs(paths, cfg, runtime.GOOS)
	pos := fs.Args()

	if len(pos) == 0 {
		if err := sys.RunAttached(ctx, clientBin, baseArgs, s.In, s.Out, s.Err); err != nil {
			cli.Error(s.Err, "database shell failed: %v", err)
			return cli.ExitErr
		}
		return cli.ExitOK
	}

	sub := strings.ToLower(pos[0])
	switch sub {
	case "shell":
		if len(pos) > 2 {
			cli.Error(s.Err, "usage: xabi db shell [name]")
			return cli.ExitUsage
		}
		shellArgs := append([]string(nil), baseArgs...)
		if len(pos) == 2 {
			if !isValidDBIdentifier(pos[1]) {
				cli.Error(s.Err, "invalid database name %q", pos[1])
				return cli.ExitErr
			}
			shellArgs = append(shellArgs, pos[1])
		}
		if err := sys.RunAttached(ctx, clientBin, shellArgs, s.In, s.Out, s.Err); err != nil {
			cli.Error(s.Err, "database shell failed: %v", err)
			return cli.ExitErr
		}
		return cli.ExitOK

	case "ls":
		if len(pos) != 1 {
			cli.Error(s.Err, "usage: xabi db ls")
			return cli.ExitUsage
		}
		queryArgs := append(append([]string(nil), baseArgs...), "-e", "SHOW DATABASES;")
		out, err := sys.Run(ctx, clientBin, queryArgs...)
		if err != nil {
			cli.Error(s.Err, "%s", formatDBError(out, err))
			return cli.ExitErr
		}
		fmt.Fprint(s.Out, string(out))
		return cli.ExitOK

	case "create":
		if len(pos) != 2 {
			cli.Error(s.Err, "usage: xabi db create <name>")
			return cli.ExitUsage
		}
		dbName := pos[1]
		if !isValidDBIdentifier(dbName) {
			cli.Error(s.Err, "invalid database name %q (use letters, digits, underscore)", dbName)
			return cli.ExitErr
		}
		sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", dbName)
		queryArgs := append(append([]string(nil), baseArgs...), "-e", sql)
		if out, err := sys.Run(ctx, clientBin, queryArgs...); err != nil {
			cli.Error(s.Err, "%s", formatDBError(out, err))
			return cli.ExitErr
		}
		cli.Info(s.Out, "created database %s", dbName)
		return cli.ExitOK

	case "drop":
		if len(pos) != 2 {
			cli.Error(s.Err, "usage: xabi db drop <name> [--force]")
			return cli.ExitUsage
		}
		dbName := pos[1]
		if !isValidDBIdentifier(dbName) {
			cli.Error(s.Err, "invalid database name %q (use letters, digits, underscore)", dbName)
			return cli.ExitErr
		}
		if !*force {
			fmt.Fprintf(s.Out, "drop database %q? [y/N]: ", dbName)
			reader := bufio.NewReader(s.In)
			answer, _ := reader.ReadString('\n')
			answer = strings.ToLower(strings.TrimSpace(answer))
			if answer != "y" && answer != "yes" {
				cli.Info(s.Out, "aborted")
				return cli.ExitOK
			}
		}
		sql := fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;", dbName)
		queryArgs := append(append([]string(nil), baseArgs...), "-e", sql)
		if out, err := sys.Run(ctx, clientBin, queryArgs...); err != nil {
			cli.Error(s.Err, "%s", formatDBError(out, err))
			return cli.ExitErr
		}
		cli.Info(s.Out, "dropped database %s", dbName)
		return cli.ExitOK

	default:
		cli.Error(s.Err, "unknown db subcommand %q (expected shell, ls, create, drop)", sub)
		return cli.ExitUsage
	}
}

func dbConnectionArgs(paths xdg.Paths, cfg config.Config, goos string) []string {
	if goos == "windows" {
		return []string{
			"--host=127.0.0.1",
			"--port=" + strconv.Itoa(cfg.Ports.DB),
			"--user=root",
		}
	}
	return []string{
		"--socket=" + paths.SocketFile("mysql"),
		"--user=root",
	}
}

func isValidDBIdentifier(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		isAlpha := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
		isDigit := ch >= '0' && ch <= '9'
		if isAlpha || isDigit || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func formatDBError(out []byte, err error) string {
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return fmt.Sprintf("%s: %v", msg, err)
	}
	return err.Error()
}
