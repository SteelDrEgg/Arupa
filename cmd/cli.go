package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const defaultConfigPath = "config.toml"

type cliOptions struct {
	ConfigPath  string
	Username    string
	Password    string
	ShowHelp    bool
	ShowVersion bool
}

func parseCLI(args []string, output io.Writer) (cliOptions, error) {
	opts := cliOptions{
		ConfigPath: defaultConfigPath,
	}
	fs := flagSetForHelp(output, &opts)
	fs.Usage = func() {
		writeUsage(output, fs)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			opts.ShowHelp = true
			return opts, nil
		}
		return opts, err
	}

	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.ShowHelp {
		return opts, nil
	}
	if opts.ShowVersion {
		return opts, nil
	}
	if opts.Username == "" && opts.Password != "" {
		return opts, fmt.Errorf("-password requires -user")
	}
	if opts.Username != "" && opts.Password == "" {
		return opts, fmt.Errorf("-user requires -password")
	}
	return opts, nil
}

func flagSetForHelp(output io.Writer, opts *cliOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("minimalpanel", flag.ContinueOnError)
	fs.SetOutput(output)
	registerFlags(fs, opts)
	return fs
}

func registerFlags(fs *flag.FlagSet, opts *cliOptions) {
	fs.StringVar(&opts.ConfigPath, "config", opts.ConfigPath, "path to config file")
	fs.StringVar(&opts.Username, "user", "", "create or update this user in config")
	fs.StringVar(&opts.Password, "password", "", "password for -user")
	fs.BoolVar(&opts.ShowHelp, "help", false, "show help")
	fs.BoolVar(&opts.ShowHelp, "h", false, "show help")
	fs.BoolVar(&opts.ShowVersion, "version", false, "show version")
}

func writeUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  minimalpanel [flags]")
	fmt.Fprintln(w, "  minimalpanel -user <name> -password <password> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fs.PrintDefaults()
}
