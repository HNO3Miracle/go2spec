package main

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// rpmModuleVersion converts a canonical Go module version to an RPM Version.
// The caller must retain the original version separately, including any
// +incompatible suffix. No repository lookup, latest-version selection, or
// fallback to a parent version is performed.
//
// packagingDate must be a real YYYYMMDD date on every call; only pseudo-version
// output uses it. The upstream pseudo-version timestamp is validated, not used
// as the packaging date. Revisions must be lowercase hexadecimal with at least
// seven digits; repository existence and Go's VCS-specific canonical shortening
// cannot be verified by this string-only helper.
//
// Prereleases use RPM's ~ separator. Internal hyphens in prerelease identifiers
// are rejected rather than silently changing their meaning with underscores.
// This mapping is not an order-preserving or injective encoding of all Go
// versions: packaging dates can reorder snapshots, and timestamps, build
// metadata, and revision characters beyond the first seven are discarded.
func rpmModuleVersion(version, packagingDate string) (string, error) {
	if !semver.IsValid(version) || module.CanonicalVersion(version) != version {
		return "", fmt.Errorf("noncanonical Go module version %q", version)
	}
	date, err := time.Parse("20060102", packagingDate)
	if err != nil || len(packagingDate) != 8 || date.Year() < 1 || date.Format("20060102") != packagingDate {
		return "", fmt.Errorf("invalid packaging date %q: want a real YYYYMMDD date", packagingDate)
	}

	pre := semver.Prerelease(version)
	core := strings.TrimSuffix(strings.TrimPrefix(semver.Canonical(version), "v"), pre)
	pre = strings.TrimPrefix(pre, "-")
	if !module.IsPseudoVersion(version) {
		// This also refuses malformed pseudo-looking versions instead of
		// reinterpreting their timestamp/revision separator as a prerelease.
		if strings.Contains(pre, "-") {
			return "", fmt.Errorf("unsupported hyphenated prerelease in %q", version)
		}
		if pre != "" {
			return core + "~" + pre, nil
		}
		return core, nil
	}

	// Validate the pseudo structure before discarding +incompatible. In
	// particular, a no-base pseudo-version cannot have that suffix, and a
	// release-based pseudo-version cannot decrement a zero patch number.
	// The returned parent is deliberately NOT used as the RPM version.
	if _, err := module.PseudoVersionBase(version); err != nil {
		return "", fmt.Errorf("invalid pseudo-version %q: %w", version, err)
	}
	stamp, err := module.PseudoVersionTime(version)
	if err != nil {
		return "", fmt.Errorf("invalid pseudo-version timestamp in %q: %w", version, err)
	}
	if stamp.Year() < 1 {
		return "", fmt.Errorf("invalid pseudo-version timestamp year in %q", version)
	}
	rev, err := module.PseudoVersionRev(version)
	if err != nil {
		return "", fmt.Errorf("invalid pseudo-version revision in %q: %w", version, err)
	}
	if len(rev) < 7 {
		return "", fmt.Errorf("pseudo-version revision in %q needs at least seven hexadecimal digits", version)
	}
	for _, c := range rev {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("pseudo-version revision in %q must be lowercase hexadecimal", version)
		}
	}

	snapshot := "git" + packagingDate + "." + rev[:7]
	prefix, ok := strings.CutSuffix(pre, stamp.Format(module.PseudoVersionTimestampFormat)+"-"+rev)
	if !ok {
		return "", fmt.Errorf("invalid pseudo-version suffix in %q", version)
	}
	switch prefix {
	case "": // vX.0.0-time-rev: no base tag.
		if core == "0.0.0" {
			return "0+" + snapshot, nil
		}
		// Keep a nonzero major instead of falling back to a 0+git version.
		return core + "~" + snapshot, nil
	case "0.": // vX.Y.Z-0.time-rev: prerelease of the stated X.Y.Z.
		return core + "~" + snapshot, nil
	default: // vX.Y.Z-pre.0.time-rev: snapshot after the stated prerelease.
		basePre, ok := strings.CutSuffix(prefix, ".0.")
		if !ok || basePre == "" {
			return "", fmt.Errorf("invalid pseudo-version prerelease in %q", version)
		}
		if strings.Contains(basePre, "-") {
			return "", fmt.Errorf("unsupported hyphenated prerelease in %q", version)
		}
		return core + "~" + basePre + "." + snapshot, nil
	}
}
