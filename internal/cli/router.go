package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Exit codes following UNIX conventions.
const (
	ExitOK    = 0
	ExitErr   = 1
	ExitUsage = 2
)

// Handler executes a subcommand and returns a process exit code.
type Handler func(ctx context.Context, args []string, s Streams) int

// Command describes a single CLI subcommand.
type Command struct {
	Name    string
	Aliases []string
	Summary string
	Usage   string
	Run     Handler
}

// Router dispatches subcommands in O(1) time without reflection or runtime overhead.
type Router struct {
	version  string
	commands []Command
	lookup   map[string]*Command
}

// NewRouter constructs a Router from the registered commands.
func NewRouter(version string, cmds ...Command) *Router {
	if version == "" {
		version = "dev"
	}
	r := &Router{
		version:  version,
		commands: make([]Command, len(cmds)),
		lookup:   make(map[string]*Command, len(cmds)*2),
	}
	copy(r.commands, cmds)
	for i := range r.commands {
		cmd := &r.commands[i]
		r.lookup[cmd.Name] = cmd
		for _, alias := range cmd.Aliases {
			r.lookup[alias] = cmd
		}
	}
	return r
}

// Dispatch routes args to the matching command handler or built-in help/version.
func (r *Router) Dispatch(ctx context.Context, args []string, s Streams) int {
	if len(args) == 0 {
		r.printHelp(s.Out)
		return ExitOK
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "-h", "--help", "help":
		if len(rest) > 0 {
			return r.printCommandHelp(rest[0], s)
		}
		r.printHelp(s.Out)
		return ExitOK
	case "-V", "--version", "version":
		fmt.Fprintf(s.Out, "xabi %s\n", r.version)
		return ExitOK
	}

	if strings.HasPrefix(sub, "-") {
		Error(s.Err, "unknown flag %q", sub)
		fmt.Fprintln(s.Err, "Run 'xabi help' for usage.")
		return ExitUsage
	}

	cmd, ok := r.lookup[sub]
	if !ok {
		Error(s.Err, "unknown command %q", sub)
		fmt.Fprintln(s.Err, "Run 'xabi help' for usage.")
		return ExitUsage
	}

	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		r.writeCommandUsage(s.Out, cmd)
		return ExitOK
	}

	if cmd.Run == nil {
		Error(s.Err, "command %q is not implemented", cmd.Name)
		return ExitErr
	}

	return cmd.Run(ctx, rest, s)
}

// NewFlagSet returns a FlagSet configured to route errors to stderr without exiting.
func NewFlagSet(name string, s Streams) *flag.FlagSet {
	fs := flag.NewFlagSet("xabi "+name, flag.ContinueOnError)
	fs.SetOutput(s.Err)
	return fs
}

func (r *Router) printCommandHelp(name string, s Streams) int {
	cmd, ok := r.lookup[name]
	if !ok {
		Error(s.Err, "unknown command %q", name)
		return ExitUsage
	}
	r.writeCommandUsage(s.Out, cmd)
	return ExitOK
}

func (r *Router) writeCommandUsage(w io.Writer, cmd *Command) {
	if cmd.Summary != "" {
		fmt.Fprintf(w, "%s — %s\n\n", cmd.Name, cmd.Summary)
	}
	if cmd.Usage != "" {
		fmt.Fprintf(w, "Usage:\n  %s\n", cmd.Usage)
	} else {
		fmt.Fprintf(w, "Usage:\n  xabi %s\n", cmd.Name)
	}
	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(w, "\nAliases:\n  %s\n", strings.Join(cmd.Aliases, ", "))
	}
}

func (r *Router) printHelp(w io.Writer) {
	fmt.Fprintln(w, "xabi — minimalist user-space local web dev orchestrator")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  xabi <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")

	for _, cmd := range r.commands {
		label := cmd.Name
		if len(cmd.Aliases) > 0 {
			label += " (" + strings.Join(cmd.Aliases, ", ") + ")"
		}
		fmt.Fprintf(w, "  %-18s %s\n", label, cmd.Summary)
	}
	fmt.Fprintf(w, "  %-18s %s\n", "help", "Show help for xabi or a subcommand")
	fmt.Fprintf(w, "  %-18s %s\n", "version", "Print xabi version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -h, --help         Show help")
	fmt.Fprintln(w, "  -V, --version      Print version")
}
