package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// To update, use:
// curl -s https://api.github.com/licenses | jq '.[].key'
var githubLicenseToSPDXLicense = map[string]string{
	//"agpl-3.0"
	"apache-2.0":   "Apache-2.0",
	"artistic-2.0": "Artistic-2.0",
	"bsd-2-clause": "BSD-2-Clause",
	"bsd-3-clause": "BSD-3-Clause",
	"cc0-1.0":      "CC0-1.0",
	//"epl-1.0" (eclipse public license)
	"gpl-2.0":  "GPL-2.0-only",
	"gpl-3.0":  "GPL-3.0-only",
	"isc":      "ISC",
	"lgpl-2.1": "LGPL-2.1-only",
	"lgpl-3.0": "LGPL-3.0-only",
	"mit":      "MIT",
	"mpl-2.0":  "MPL-2.0",
	//"unlicense"
}

var githubRegexp = regexp.MustCompile(`github\.com/([^/]+/[^/]+)`)

func findGitHubOwnerRepo(gopkg string) (string, error) {
	if strings.HasPrefix(gopkg, "github.com/") {
		return strings.TrimPrefix(gopkg, "github.com/"), nil
	}
	resp, err := http.Get("https://" + gopkg + "?go-get=1")
	if err != nil {
		return "", fmt.Errorf("HTTP get: %w", err)
	}
	defer resp.Body.Close()
	z := html.NewTokenizer(resp.Body)
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return "", fmt.Errorf("%q is not on GitHub", gopkg)
		}
		token := z.Token()
		if token.Data != "meta" {
			continue
		}
		var meta struct {
			name, content string
		}
		for _, attr := range token.Attr {
			if attr.Key == "name" {
				meta.name = attr.Val
			}
			if attr.Key == "content" {
				meta.content = attr.Val
			}
		}

		match := func(name string, length int) string {
			if f := strings.Fields(meta.content); meta.name == name && len(f) == length {
				if f[0] != gopkg {
					return ""
				}
				if repoMatch := githubRegexp.FindStringSubmatch(f[2]); repoMatch != nil {
					return strings.TrimSuffix(repoMatch[1], ".git")
				}
			}
			return ""
		}
		if repo := match("go-import", 3); repo != "" {
			return repo, nil
		}
		if repo := match("go-source", 4); repo != "" {
			return repo, nil
		}
	}
}

func findGitHubRepo(gopkg string) (owner string, repo string, _ error) {
	ownerrepo, err := findGitHubOwnerRepo(gopkg)
	if err != nil {
		return "", "", fmt.Errorf("find GitHub owner repo: %w", err)
	}
	parts := strings.Split(ownerrepo, "/")
	if got, want := len(parts), 2; got != want {
		return "", "", fmt.Errorf("invalid GitHub repo: %q does not follow owner/repo", repo)
	}
	return parts[0], parts[1], nil
}

func getLicenseForGopkg(gopkg string) (string, error) {
	owner, repo, err := findGitHubRepo(gopkg)
	if err != nil {
		return "", fmt.Errorf("find GitHub repo: %w", err)
	}

	rl, _, err := gitHub.Repositories.License(context.TODO(), owner, repo)
	if err != nil {
		return "", fmt.Errorf("get license for Go package: %w", err)
	}

	if license, ok := githubLicenseToSPDXLicense[rl.GetLicense().GetKey()]; ok {
		return license, nil
	}

	return "TODO", nil
}

// getDescriptionForGopkg gets the package description from GitHub,
// intended for the synopsis or the short description in debian/control.
func getDescriptionForGopkg(gopkg string) (string, error) {
	owner, repo, err := findGitHubRepo(gopkg)
	if err != nil {
		return "", fmt.Errorf("find GitHub repo: %w", err)
	}

	rr, _, err := gitHub.Repositories.Get(context.TODO(), owner, repo)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(rr.GetDescription()), nil
}
