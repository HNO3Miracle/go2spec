package main

import (
	"os"

	"github.com/google/go-github/v60/github"
	"github.com/sandrolain/httpcache"
)

var (
	gitHub *github.Client
)

func printHelp() {
	helpText := `go2spec - A tool to package Go modules into RPM spec files.

Usage:
  go2spec [command]

Available Commands:
  pack        Package the Go modules into an RPM spec file
  help        Show this help message

Use "go2spec [command] --help" for more information about a command.

If there are no commands provided, the tool will default to executing the 'pack' command.
`
	println(helpText)
}

func main() {
	transport := github.BasicAuthTransport{
		Username:  os.Getenv("GITHUB_USERNAME"),
		Password:  os.Getenv("GITHUB_PASSWORD"),
		OTP:       os.Getenv("GITHUB_OTP"),
		Transport: httpcache.NewMemoryCacheTransport(),
	}
	gitHub = github.NewClient(transport.Client())

	args := os.Args[1:]

	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "help":
		printHelp()
	case "pack":
		// Placeholder for the pack command implementation
		println("Executing 'pack' command...")
		// Actual packing logic would go here
		mainPack(args[1:], nil)
	default:
		// Default to 'pack' command if no command is provided
		println("No command provided. Defaulting to 'pack' command...")
		// Actual packing logic would go here
		mainPack(args, usage)
	}
}
