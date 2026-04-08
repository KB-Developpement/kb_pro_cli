package main

import (
	"os"

	"github.com/KB-Developpement/kb_pro_cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
