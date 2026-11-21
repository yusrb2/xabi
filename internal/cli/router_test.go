package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testStreams() (Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	return Streams{
		In:  strings.NewReader(""),
		Out: &out,
		Err: &errBuf,
	}, &out, &errBuf
}

func TestRouterBuiltinHelpAndVersion(t *testing.T) {
	r := NewRouter("1.2.3", Command{
		Name:    "status",
		Aliases: []string{"st"},
		Summary: "Show service status",
		Usage:   "xabi status [service]",
		Run: func(ctx context.Context, args []string, s Streams) int {
			return ExitOK
		},
	})

	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "no args", args: nil, wantCode: ExitOK, wantOut: "Usage:"},
		{name: "help", args: []string{"help"}, wantCode: ExitOK, wantOut: "status (st)"},
		{name: "short help", args: []string{"-h"}, wantCode: ExitOK, wantOut: "xabi <command>"},
		{name: "command help", args: []string{"help", "st"}, wantCode: ExitOK, wantOut: "xabi status [service]"},
		{name: "version", args: []string{"--version"}, wantCode: ExitOK, wantOut: "xabi 1.2.3"},
		{name: "short version", args: []string{"-V"}, wantCode: ExitOK, wantOut: "xabi 1.2.3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, out, errBuf := testStreams()
			code := r.Dispatch(context.Background(), tc.args, s)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr=%q)", code, tc.wantCode, errBuf.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("stdout = %q, want substring %q", out.String(), tc.wantOut)
			}
		})
	}
}

func TestRouterDispatchAliasAndErrors(t *testing.T) {
	var gotArgs []string
	r := NewRouter("dev", Command{
		Name:    "status",
		Aliases: []string{"st"},
		Summary: "Show status",
		Run: func(ctx context.Context, args []string, s Streams) int {
			gotArgs = args
			Info(s.Out, "ok")
			return ExitOK
		},
	})

	s, out, errBuf := testStreams()
	if code := r.Dispatch(context.Background(), []string{"st", "nginx"}, s); code != ExitOK {
		t.Fatalf("code = %d, want 0", code)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "nginx" {
		t.Errorf("gotArgs = %v, want [nginx]", gotArgs)
	}
	if got := out.String(); got != ":: ok\n" {
		t.Errorf("stdout = %q, want %q", got, ":: ok\n")
	}
	if errBuf.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errBuf.String())
	}

	// Unknown command
	s, _, errBuf = testStreams()
	if code := r.Dispatch(context.Background(), []string{"bogus"}, s); code != ExitUsage {
		t.Fatalf("unknown cmd code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errBuf.String(), `unknown command "bogus"`) {
		t.Errorf("stderr = %q, want unknown command message", errBuf.String())
	}

	// Unknown flag
	s, _, errBuf = testStreams()
	if code := r.Dispatch(context.Background(), []string{"--nope"}, s); code != ExitUsage {
		t.Fatalf("unknown flag code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errBuf.String(), `unknown flag "--nope"`) {
		t.Errorf("stderr = %q, want unknown flag message", errBuf.String())
	}
}
