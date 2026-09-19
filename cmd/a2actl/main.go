package main

import (
	"os"

	"github.com/agent-a2a-bridge/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}
