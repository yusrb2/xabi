package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/yusrb2/xabi/internal/cli"
)

// NewCompletionCommand returns the `xabi completion` command definition.
func NewCompletionCommand() cli.Command {
	return cli.Command{
		Name:    "completion",
		Summary: "Generate shell completion script (bash, zsh, fish, powershell)",
		Usage:   "xabi completion <bash|zsh|fish|powershell>",
		Run: func(_ context.Context, args []string, s cli.Streams) int {
			return runCompletion(args, s)
		},
	}
}

func runCompletion(args []string, s cli.Streams) int {
	fs := cli.NewFlagSet("completion", s)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		cli.Error(s.Err, "usage: xabi completion <bash|zsh|fish|powershell>")
		return cli.ExitUsage
	}

	script, err := renderCompletionScript(fs.Arg(0))
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitUsage
	}
	fmt.Fprint(s.Out, script)
	return cli.ExitOK
}

func renderCompletionScript(shell string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "bash":
		return bashCompletionScript, nil
	case "zsh":
		return zshCompletionScript, nil
	case "fish":
		return fishCompletionScript, nil
	case "powershell", "pwsh":
		return powershellCompletionScript, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (expected bash, zsh, fish, or powershell)", shell)
	}
}

const bashCompletionScript = `# bash completion for xabi
_xabi_completions() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    local cmds="init doctor up down restart status st add rm ls open tls php conf log share db completion help version"
    local services="nginx php-fpm mariadb"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${cmds}" -- "${cur}") )
        return 0
    fi

    case "${COMP_WORDS[1]}" in
        up|down|restart|status|st)
            COMPREPLY=( $(compgen -W "${services}" -- "${cur}") )
            ;;
        rm|open|tls|share)
            local sites
            sites="$(xabi ls 2>/dev/null | awk 'NR>1 {print $1}')"
            COMPREPLY=( $(compgen -W "${sites}" -- "${cur}") )
            ;;
        conf)
            local sites
            sites="$(xabi ls 2>/dev/null | awk 'NR>1 {print $1}')"
            COMPREPLY=( $(compgen -W "config nginx php sites ${sites}" -- "${cur}") )
            ;;
        log)
            local sites
            sites="$(xabi ls 2>/dev/null | awk 'NR>1 {print $1}')"
            COMPREPLY=( $(compgen -W "${services} ${sites}" -- "${cur}") )
            ;;
        php)
            COMPREPLY=( $(compgen -W "use pin" -- "${cur}") )
            ;;
        db)
            COMPREPLY=( $(compgen -W "shell ls create drop" -- "${cur}") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish powershell" -- "${cur}") )
            ;;
        help)
            COMPREPLY=( $(compgen -W "${cmds}" -- "${cur}") )
            ;;
    esac
}
complete -F _xabi_completions xabi
`

const zshCompletionScript = `#compdef xabi

_xabi() {
    local -a commands services sites
    commands=(
        'init:Initialize user-space directories and stack'
        'doctor:Verify binaries, ports, XDG layout, and hosts'
        'up:Start managed services'
        'down:Stop managed services'
        'restart:Restart or reload managed services'
        'status:Show real-time daemon status'
        'st:Alias for status'
        'add:Register a local site'
        'rm:Remove a registered site'
        'ls:List registered sites'
        'open:Open site in browser'
        'tls:Enable or disable local HTTPS'
        'php:List, switch, or pin PHP version'
        'conf:Open configuration in editor'
        'log:Stream service or site logs'
        'share:Expose site via public tunnel'
        'db:Manage databases or open SQL shell'
        'completion:Generate shell completion script'
        'help:Show help'
        'version:Print version'
    )
    services=(nginx php-fpm mariadb)

    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi

    sites=(${(f)"$(xabi ls 2>/dev/null | awk 'NR>1 {print $1}')"})
    case "${words[2]}" in
        up|down|restart|status|st)
            _describe 'service' services
            ;;
        rm|open|tls|share)
            _describe 'site' sites
            ;;
        conf)
            local -a targets
            targets=(config nginx php sites $sites)
            _describe 'target' targets
            ;;
        log)
            local -a targets
            targets=($services $sites)
            _describe 'target' targets
            ;;
        php)
            compadd -- use pin
            ;;
        db)
            compadd -- shell ls create drop
            ;;
        completion)
            compadd -- bash zsh fish powershell
            ;;
    esac
}

compdef _xabi xabi
`

const fishCompletionScript = `# fish completion for xabi
set -l xabi_cmds init doctor up down restart status st add rm ls open tls php conf log share db completion help version

function __xabi_sites
    xabi ls 2>/dev/null | awk 'NR>1 {print $1}'
end

complete -c xabi -f
complete -c xabi -n "not __fish_seen_subcommand_from $xabi_cmds" -a "$xabi_cmds"
complete -c xabi -n "__fish_seen_subcommand_from up down restart status st" -a "nginx php-fpm mariadb"
complete -c xabi -n "__fish_seen_subcommand_from rm open tls share" -a "(__xabi_sites)"
complete -c xabi -n "__fish_seen_subcommand_from conf" -a "config nginx php sites (__xabi_sites)"
complete -c xabi -n "__fish_seen_subcommand_from log" -a "nginx php-fpm mariadb (__xabi_sites)"
complete -c xabi -n "__fish_seen_subcommand_from php" -a "use pin"
complete -c xabi -n "__fish_seen_subcommand_from db" -a "shell ls create drop"
complete -c xabi -n "__fish_seen_subcommand_from completion" -a "bash zsh fish powershell"
`

const powershellCompletionScript = `# PowerShell completion for xabi
Register-ArgumentCompleter -Native -CommandName @('xabi', 'xabi.exe') -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)

    $cmds = @('init', 'doctor', 'up', 'down', 'restart', 'status', 'st', 'add', 'rm', 'ls', 'open', 'tls', 'php', 'conf', 'log', 'share', 'db', 'completion', 'help', 'version')
    $services = @('nginx', 'php-fpm', 'mariadb')

    $tokens = @($commandAst.CommandElements | ForEach-Object { $_.ToString() })
    $sub = if ($tokens.Count -ge 2) { $tokens[1] } else { '' }

    $getSites = {
        $lines = xabi ls 2>$null | Select-Object -Skip 1
        foreach ($l in $lines) {
            $first = ($l -split '\s+')[0]
            if ($first) { $first }
        }
    }

    $candidates = if ($tokens.Count -le 2 -and $wordToComplete -eq $sub) {
        $cmds
    } else {
        switch ($sub) {
            { $_ -in 'up', 'down', 'restart', 'status', 'st' } { $services; break }
            { $_ -in 'rm', 'open', 'tls', 'share' } { & $getSites; break }
            'conf' { @('config', 'nginx', 'php', 'sites') + (& $getSites); break }
            'log'  { $services + (& $getSites); break }
            'php'  { @('use', 'pin'); break }
            'db'   { @('shell', 'ls', 'create', 'drop'); break }
            'completion' { @('bash', 'zsh', 'fish', 'powershell'); break }
            'help' { $cmds; break }
            default { @() }
        }
    }

    $candidates | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`
