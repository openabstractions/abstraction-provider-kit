package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	exitNotResolved = 1
	exitUsage       = 2
	exitRefusedCall = 3
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
func main() {
	if err := modelsPackCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var exit *exitError
		code := 1
		if errors.As(err, &exit) {
			code = exit.code
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(code)
	}
}
func isHelp(arg string) bool { return arg == "--help" || arg == "-h" || arg == "help" }
func containsHelp(args []string) bool {
	for _, arg := range args {
		if isHelp(arg) {
			return true
		}
	}
	return false
}
func flagMistake(_ io.Writer, command, usage, problem string) error {
	line, _, _ := strings.Cut(usage, "\n")
	return &exitError{exitUsage, fmt.Errorf("%s: %s\n%s\nRun modelpack --help for the flags.", command, problem, line)}
}
func badFlag(_ *flag.FlagSet, diagnostics io.Writer, command, usage string, _ []string, err error) error {
	return flagMistake(diagnostics, command, usage, err.Error())
}
func parsePositional(flags *flag.FlagSet, args []string) ([]string, error) {
	positional := []string{}
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
}
