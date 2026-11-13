// Package config parses the command line of the restic-browser binary.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Command is what the binary was asked to do.
type Command string

// The available commands.
const (
	CmdServe       Command = "serve"
	CmdSetPassword Command = "set-password"
	CmdVersion     Command = "version"
)

// Config holds the settings shared by all commands.
type Config struct {
	Command   Command
	Listen    string // HTTP listen address
	DataDir   string // holds the SQLite database, the encryption key and restic's cache
	ResticBin string // restic executable; resolved lazily, see ResolveRestic
	Debug     bool
	Secure    bool // the app is served over HTTPS (marks cookies Secure)
}

const usage = `Usage: restic-browser [command] [flags]

Commands:
  serve          Start the web interface (default)
  set-password   Set or change the login password
  version        Print version information

Flags:
`

// Parse reads the command line (without the program name).
func Parse(args []string, out io.Writer) (Config, error) {
	cfg := Config{Command: CmdServe}
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cfg.Command, args = Command(args[0]), args[1:]
	}
	switch cfg.Command {
	case CmdServe, CmdSetPassword, CmdVersion:
	default:
		return cfg, fmt.Errorf("unknown command %q", cfg.Command)
	}

	fs := flag.NewFlagSet("restic-browser", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprint(out, usage); fs.PrintDefaults() }
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "address to listen on, e.g. :8080 to serve the whole network")
	fs.StringVar(&cfg.DataDir, "data-dir", "data", "directory for the database, key file and restic cache")
	fs.StringVar(&cfg.ResticBin, "restic", "restic", "path of the restic executable")
	fs.BoolVar(&cfg.Debug, "debug", false, "log every request")
	fs.BoolVar(&cfg.Secure, "secure-cookies", false, "mark the session cookie Secure (use behind HTTPS)")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return cfg, nil
}

// ErrHelp is returned by Parse when -h was given.
var ErrHelp = flag.ErrHelp

// ResolveRestic finds the restic executable: the configured path if it exists,
// else restic on PATH, else next to this executable.
func ResolveRestic(configured string) (string, error) {
	if configured != "restic" { // explicitly set: use as given
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("restic executable %q not found: %w", configured, err)
		}
		return configured, nil
	}
	if p, err := exec.LookPath("restic"); err == nil {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		for _, name := range []string{"restic", "restic.exe"} {
			if p := filepath.Join(filepath.Dir(self), name); fileExists(p) {
				return p, nil
			}
		}
	}
	for _, name := range []string{"restic", "restic.exe"} { // working directory
		if fileExists(name) {
			return filepath.Abs(name)
		}
	}
	return "", errors.New("restic executable not found: install restic or pass --restic <path>")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
