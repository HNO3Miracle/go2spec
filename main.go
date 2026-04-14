package main

import (
	"net/http"
	"os"

	"github.com/google/go-github/v60/github"
	"github.com/sandrolain/httpcache"
)

var (
	gitHub *github.Client
)

// TokenTransport implements http.RoundTripper for Bearer token authentication
type TokenTransport struct {
	Token     string
	Transport http.RoundTripper
}

func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Authorization", "Bearer "+t.Token)
	return t.Transport.RoundTrip(req)
}

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
	token := os.Getenv("GITHUB_TOKEN")

	var client *http.Client
	if token != "" {
		// Use token authentication for better rate limits
		client = &http.Client{
			Transport: &TokenTransport{
				Token:     token,
				Transport: httpcache.NewMemoryCacheTransport(),
			},
		}
	} else {
		// Fallback to basic auth if token is not provided
		transport := github.BasicAuthTransport{
			Username:  os.Getenv("GITHUB_USERNAME"),
			Password:  os.Getenv("GITHUB_PASSWORD"),
			OTP:       os.Getenv("GITHUB_OTP"),
			Transport: httpcache.NewMemoryCacheTransport(),
		}
		client = transport.Client()
	}
	gitHub = github.NewClient(client)

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
		mainPack(args, printHelp)
	}
}
