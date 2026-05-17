// Package cliflags exposes flag builders shared between subcommands (cmd/scan
// and cmd/cursor). Centralizing them ensures the env var name, default, and
// help text stay in sync — and that a future fourth subcommand picks them up
// for free.
package cliflags

import "github.com/urfave/cli/v3"

// EnvCheckpointPath is the env var consulted by every --checkpoint flag.
const EnvCheckpointPath = "FEEDSCAN_CHECKPOINT"

// EnvFormat is the env var consulted by every --format flag.
const EnvFormat = "FEEDSCAN_FORMAT"

// CheckpointFlag returns the --checkpoint flag bound to dest. When required
// is true the flag has no default; otherwise it defaults to defaultValue.
func CheckpointFlag(dest *string, required bool, defaultValue string) cli.Flag {
	f := &cli.StringFlag{
		Name:        "checkpoint",
		Usage:       "checkpoint file path",
		Sources:     cli.EnvVars(EnvCheckpointPath),
		Destination: dest,
		Required:    required,
	}
	if !required {
		f.Value = defaultValue
	}
	return f
}

// FormatFlag returns the --format flag bound to dest with the given default.
func FormatFlag(dest *string, defaultValue string) cli.Flag {
	return &cli.StringFlag{
		Name:        "format",
		Usage:       "output format: json|table",
		Value:       defaultValue,
		Sources:     cli.EnvVars(EnvFormat),
		Destination: dest,
	}
}
