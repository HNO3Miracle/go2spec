package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildSettings(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "1234567890abcdef"},
		{Key: "vcs.time", Value: "2026-06-27T15:33:29Z"},
	}

	if got, want := versionFromBuildSettings(settings), "2026-06-27 1234567"; got != want {
		t.Fatalf("versionFromBuildSettings() = %q, want %q", got, want)
	}
}

func TestVersionFromBuildSettingsUnknown(t *testing.T) {
	if got := versionFromBuildSettings(nil); got != unknownVersion {
		t.Fatalf("versionFromBuildSettings(nil) = %q, want %q", got, unknownVersion)
	}
}
