package main

import (
	"context"
	"fmt"
	"html"
	"path"
	"regexp"
	"strings"
)

func getRepoURLForGopkg(gopkg string) (string, error) {
	info, err := getPkgsiteInfo(context.TODO(), gopkg)
	if err != nil {
		return "", err
	}
	repoURL := strings.TrimSpace(info.Module.RepoURL)
	if repoURL == "" {
		return "", fmt.Errorf("pkgsite module %q has no repository URL", info.Module.Path)
	}
	return gitCloneURLFromRepoURL(repoURL), nil
}

var (
	htmlTagRegexp      = regexp.MustCompile(`<[^>]*>`)
	markdownImageRegex = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
	markdownLinkRegex  = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	packagePrefixRegex = regexp.MustCompile(`^Package\s+\S+\s+`)
)

// cleanSummaryCandidate turns a godoc synopsis or README line into an
// RPM-style Summary by stripping markup, keeping the first sentence, dropping
// the leading "Package foo" convention, and capitalizing the result.
func cleanSummaryCandidate(summary string) string {
	summary = html.UnescapeString(strings.TrimSpace(summary))
	summary = markdownImageRegex.ReplaceAllString(summary, "")
	summary = markdownLinkRegex.ReplaceAllString(summary, "$1")
	summary = htmlTagRegexp.ReplaceAllString(summary, " ")
	summary = strings.ReplaceAll(summary, "`", "")
	summary = strings.Join(strings.Fields(summary), " ")
	summary = strings.Trim(summary, " \t\n\r#*-_")
	if end := strings.Index(summary, ". "); end >= 0 {
		summary = summary[:end]
	}
	summary = packagePrefixRegex.ReplaceAllString(summary, "")
	if summary != "" && summary[0] >= 'a' && summary[0] <= 'z' {
		summary = string(summary[0]-('a'-'A')) + summary[1:]
	}
	if summary == "" ||
		strings.HasPrefix(summary, "[!") ||
		strings.HasPrefix(summary, "![") ||
		strings.HasPrefix(summary, "<!--") ||
		strings.ContainsAny(summary, "<>") {
		return ""
	}
	return strings.TrimSuffix(summary, ".")
}

// summaryFromReadme returns the first prose line of a README, skipping code
// fences, headings, HTML comments, badges, admonitions, and images.
func summaryFromReadme(markdown string) string {
	inFence := false
	lines := strings.Split(markdown, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next != "" && strings.Trim(next, "=-") == "" {
				continue
			}
		}
		if inFence ||
			line == "" ||
			strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "[!") ||
			strings.HasPrefix(line, "![") ||
			strings.HasPrefix(line, "<!--") ||
			strings.HasPrefix(line, "<p align=") ||
			strings.HasPrefix(line, "---") ||
			strings.Trim(line, "=-") == "" {
			continue
		}
		if summary := cleanSummaryCandidate(line); summary != "" {
			return summary
		}
	}
	return ""
}

func unusableSummary(summary, gopkg string, info *pkgsiteInfo) bool {
	if summary == "" {
		return true
	}
	base := path.Base(gopkg)
	moduleBase := path.Base(info.Module.Path)
	return strings.EqualFold(summary, base) ||
		strings.EqualFold(summary, moduleBase) ||
		strings.EqualFold(summary, info.Package.Name)
}

func getLicenseForGopkg(gopkg string) (string, error) {
	info, err := getPkgsiteInfo(context.TODO(), gopkg)
	if err != nil {
		return "", err
	}

	licenses := info.Module.Licenses
	if len(licenses) == 0 {
		licenses = info.Package.Licenses
	}

	return pkgsiteLicenseExpression(licenses), nil
}

// getDescriptionForGopkg gets the package synopsis from pkg.go.dev,
// intended for the summary in the RPM spec.
func getDescriptionForGopkg(gopkg string) (string, error) {
	info, err := getPkgsiteInfo(context.TODO(), gopkg)
	if err != nil {
		return "", err
	}

	description := cleanSummaryCandidate(info.Package.Synopsis)
	if unusableSummary(description, gopkg, info) {
		description = ""
	}
	if description == "" && info.Module.Readme != nil {
		description = summaryFromReadme(info.Module.Readme.Contents)
		if unusableSummary(description, gopkg, info) {
			description = ""
		}
	}
	if description == "" {
		return "", fmt.Errorf("no usable synopsis for %q", gopkg)
	}
	return description, nil
}
