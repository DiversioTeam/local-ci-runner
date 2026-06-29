package main

import (
	_ "embed"
	"fmt"
	"io"
	"strings"
)

// manualText is the built-in long-form reference for binary-only users.
//
// Keep MANUAL.md product-facing. Internal maintenance notes belong here, not in the
// user-visible manual.
//
// Section layout to keep stable when the tool grows:
// - 0: how to use the manual
// - 1-3: purpose and command map
// - 4-7: run model and command reference
// - 8-11: JSON, active-run semantics, debugging, safety
//
//go:embed MANUAL.md
var manualText string

func (c *cli) manualCommand(args []string) error {
	if err := validateManualArgs(args); err != nil {
		return err
	}
	_, err := io.WriteString(c.stdout, manualText)
	return err
}

func validateManualArgs(args []string) error {
	switch len(args) {
	case 0:
		return nil
	case 1:
		if isHelpArg(args[0]) {
			return nil
		}
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("unknown flag %q", args[0])
		}
		return fmt.Errorf("manual does not accept positional arguments")
	default:
		return fmt.Errorf("manual accepts no arguments")
	}
}
