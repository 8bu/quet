package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/8bu/quet/internal/config"
)

// starterFile is one file `quet init` writes.
type starterFile struct {
	path    string
	content string
}

// runInit writes the starter configuration and flags files, keeping existing
// files unless --force is given.
func runInit(cmd command, stdout, stderr io.Writer) int {
	files, err := starterFiles(cmd.global)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	for _, f := range files {
		err := config.WriteStarter(f.path, f.content, cmd.force)
		switch {
		case err == nil:
			fmt.Fprintf(stdout, "wrote %s\n", f.path)
		case errors.Is(err, config.ErrExists):
			fmt.Fprintf(stderr, "quet: kept %s (already exists; --force to overwrite)\n", f.path)
		default:
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
	}
	return 0
}

// starterFiles lists the files `quet init` writes: ./quet.yaml and ./flags.yaml,
// or config.yaml and flags.yaml in the per-user Quet directory with --global.
func starterFiles(global bool) ([]starterFile, error) {
	if !global {
		return []starterFile{
			{path: "quet.yaml", content: config.StarterConfig},
			{path: "flags.yaml", content: config.StarterFlags},
		}, nil
	}
	dir := config.QuetDir()
	if dir == "" {
		return nil, errors.New("cannot locate the user config directory (set $XDG_CONFIG_HOME or $HOME)")
	}
	return []starterFile{
		{path: filepath.Join(dir, "config.yaml"), content: config.StarterConfig},
		{path: filepath.Join(dir, "flags.yaml"), content: config.StarterFlags},
	}, nil
}
