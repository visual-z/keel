// Command keel re-derives task-card claims from git and real exit codes.
package main

import (
	"os"

	"github.com/visual-z/keel/internal/keel"
)

func main() { os.Exit(keel.Main(os.Args[1:], os.Stdout, os.Stderr)) }
