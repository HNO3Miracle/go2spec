package main

import (
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// semverRegexp checks if a string is a valid Go semver,
	// from https://semver.org/#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
	// with leading "v" added.
	semverRegexp = regexp.MustCompile(`^v(?P<major>0|[1-9]\d*)\.(?P<minor>0|[1-9]\d*)\.(?P<patch>0|[1-9]\d*)(?:-(?P<prerelease>(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+(?P<buildmetadata>[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

	// uversionPrereleaseRegexp checks for upstream pre-release
	// so that '-' can be replaced with '~' in pkgVersionFromGit.
	uversionPrereleaseRegexp = regexp.MustCompile(`(\d)[_\.\-\+]?(RC|rc|pre|dev|beta|alpha)[.]?(\d*)$`)

	packagingDateNow = time.Now
)

func packagingDateString() string {
	return packagingDateNow().Format("20060102")
}

func shortCommitHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

func preferredRevisionCandidates(preferredRev string) []string {
	preferredRev = strings.TrimSpace(preferredRev)
	if preferredRev == "" {
		return nil
	}
	candidates := []string{preferredRev}
	if !strings.HasPrefix(preferredRev, "v") && semverRegexp.MatchString("v"+preferredRev) {
		candidates = append(candidates, "v"+preferredRev)
	}
	return candidates
}

// pkgVersionFromGit determines the actual version to be packaged
// from the git repository status and user preference.
// Besides returning the upstream version, the "upstream" struct fields
// u.version, u.commitID, u.commitIsh, u.hasRelease and u.isRelease are also set.
// `preferredRev` should be empty if there are no user preferences.
// TODO: also support other VCS
func pkgVersionFromGit(gitdir string, u *upstream, preferredRev string, forcePrerelease bool) (string, error) {
	var latestTag string
	var commitsAhead int

	var cmd *exec.Cmd // the temporary shell commands we execute

	// If the user specifies a valid tag as the preferred revision, that tag should be used without additional heuristics.
	for _, candidate := range preferredRevisionCandidates(preferredRev) {
		cmd = exec.Command("git", "tag", "--list", candidate)
		cmd.Dir = gitdir
		if out, err := cmd.Output(); err == nil && slices.Contains(strings.Fields(string(out)), candidate) {
			latestTag = candidate
			break
		}
	}

	// Find @latest version tag (whether annotated or not) when the user
	// (1) does not specify a version tag, or
	// (2) specifies an invalid version tag.
	if len(latestTag) == 0 {
		cmd = exec.Command("git", "describe", "--abbrev=0", "--tags", "--exclude", "*/v*")
		cmd.Dir = gitdir
		if out, err := cmd.Output(); err == nil {
			latestTag = strings.TrimSpace(string(out))
		}
	}

	if len(latestTag) > 0 {
		u.hasRelease = true
		u.tag = latestTag
		log.Printf("Found latest tag %q", latestTag)

		if !semverRegexp.MatchString(latestTag) {
			log.Printf("WARNING: Latest tag %q is not a valid SemVer version\n", latestTag)
			// TODO: Enforce strict sementic versioning with leading "v"?
		}

		// Count number of commits since @latest version
		cmd = exec.Command("git", "rev-list", "--count", latestTag+"..HEAD")
		cmd.Dir = gitdir
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git rev-list: %w", err)
		}
		commitsAhead, err = strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return "", fmt.Errorf("parse commits ahead: %w", err)
		}

		if commitsAhead == 0 {
			// Equivalent to "git describe --exact-match --tags"
			log.Printf("Latest tag %q matches master", latestTag)
		} else {
			log.Printf("INFO: master is ahead of %q by %v commits", latestTag, commitsAhead)
		}

		u.commitIsh = latestTag

		// Mangle latestTag into upstream_version
		// TODO: Move to function and write unit test?
		u.version = strings.TrimLeftFunc(
			uversionPrereleaseRegexp.ReplaceAllString(latestTag, "$1~$2$3"),
			func(r rune) bool {
				return !unicode.IsNumber(r)
			},
		)

		if forcePrerelease {
			log.Printf("INFO: Force packaging master (prerelease) as requested by user")
			// Fallthrough to package @master (prerelease)
		} else {
			u.isRelease = true
			return u.version, nil
		}
	}

	// Packaging @master (prerelease)

	// Fetch full commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitdir
	fullCommitHashBytes, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	fullCommitHash := strings.TrimSpace(string(fullCommitHashBytes))
	lastCommitHash := shortCommitHash(fullCommitHash)
	u.commitID = fullCommitHash
	u.commitIsh = lastCommitHash

	// Snapshot versions are based on the packaging date, not the commit date.
	u.version = fmt.Sprintf("0+git%s.%s", packagingDateString(), lastCommitHash)
	return u.version, nil
}
