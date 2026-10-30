package main

import (
	"github.com/BackFire/casdoor-cli/internal/cli"
	"os"
)

var version = "dev"

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version)) }
