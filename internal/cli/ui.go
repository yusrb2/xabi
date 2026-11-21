// Package cli provides a minimal, zero-overhead subcommand router and UNIX-style output helpers.
package cli

import (
	"fmt"
	"io"
	"os"
)

// Streams bundles the standard I/O streams used by commands.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// DefaultStreams returns Streams connected to the OS standard descriptors.
func DefaultStreams() Streams {
	return Streams{
		In:  os.Stdin,
		Out: os.Stdout,
		Err: os.Stderr,
	}
}

// Info writes a concise Arch-style status line prefixed with ":: ".
func Info(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, ":: "+format+"\n", args...)
}

// Step writes a section header line prefixed with "==> ".
func Step(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "==> "+format+"\n", args...)
}

// Warn writes a warning message to the given stream (typically stderr).
func Warn(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "xabi: warning: "+format+"\n", args...)
}

// Error writes an error message to the given stream (typically stderr).
func Error(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "xabi: error: "+format+"\n", args...)
}
