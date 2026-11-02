package main

import (
	"strings"
	"testing"
)

func TestRPMModuleVersion(t *testing.T) {
	const date = "20261019"
	tests := []struct {
		name, version, want string
	}{
		{"release", "v1.2.3", "1.2.3"},
		{"zero_release", "v0.0.0", "0.0.0"},
		{"large_numbers", "v12345678901234567890.2.3", "12345678901234567890.2.3"},
		{"release_incompatible", "v2.3.4+incompatible", "2.3.4"},
		{"prerelease", "v1.2.3-alpha", "1.2.3~alpha"},
		{"prerelease_identifiers", "v1.2.3-alpha.1.rc.2", "1.2.3~alpha.1.rc.2"},
		{"numeric_prerelease", "v1.2.3-0", "1.2.3~0"},
		{"prerelease_case", "v1.2.3-RC.1", "1.2.3~RC.1"},
		{"prerelease_incompatible", "v2.3.4-rc.1+incompatible", "2.3.4~rc.1"},
		{"pseudo_without_base", "v0.0.0-20240229123456-abcdef123456", "0+git20261019.abcdef1"},
		{"pseudo_nonzero_major_without_base", "v2.0.0-20240229123456-abcdef123456", "2.0.0~git20261019.abcdef1"},
		{"pseudo_after_release", "v1.2.4-0.20240229123456-abcdef123456", "1.2.4~git20261019.abcdef1"},
		{"pseudo_after_release_incompatible", "v2.3.5-0.20240229123456-abcdef123456+incompatible", "2.3.5~git20261019.abcdef1"},
		{"pseudo_after_prerelease", "v1.2.3-rc.0.20240229123456-abcdef123456", "1.2.3~rc.git20261019.abcdef1"},
		{"pseudo_after_dotted_prerelease", "v1.2.3-rc.2.0.20240229123456-abcdef123456", "1.2.3~rc.2.git20261019.abcdef1"},
		{"pseudo_after_prerelease_incompatible", "v2.3.4-rc.1.0.20240229123456-abcdef123456+incompatible", "2.3.4~rc.1.git20261019.abcdef1"},
		{"pseudo_after_numeric_prerelease", "v1.2.3-0.0.20240229123456-abcdef123456", "1.2.3~0.git20261019.abcdef1"},
		{"pseudo_large_patch", "v1.2.12345678901234567890-0.20240229123456-abcdef123456", "1.2.12345678901234567890~git20261019.abcdef1"},
		{"seven_digit_hex_revision", "v0.0.0-20240229123456-123abcd", "0+git20261019.123abcd"},
		{"long_hex_revision", "v0.0.0-20240229123456-0123456789abcdef0123456789abcdef01234567", "0+git20261019.0123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rpmModuleVersion(tt.version, date)
			if err != nil || got != tt.want {
				t.Fatalf("rpmModuleVersion(%q, %q) = %q, %v; want %q, nil", tt.version, date, got, err, tt.want)
			}
			if strings.ContainsAny(got, "- \t\n") {
				t.Fatalf("unexpected RPM Version separator/whitespace in %q", got)
			}
		})
	}
}

func TestRPMModuleVersionRejectsVersions(t *testing.T) {
	tests := []struct {
		name, version string
	}{
		{"empty", ""},
		{"latest", "latest"},
		{"branch", "main"},
		{"revision_only", "abcdef123456"},
		{"module_query", "example.com/mod@v1.2.3"},
		{"missing_v", "1.2.3"},
		{"uppercase_v", "V1.2.3"},
		{"short_major", "v1"},
		{"short_minor", "v1.2"},
		{"short_incompatible", "v2+incompatible"},
		{"leading_space", " v1.2.3"},
		{"trailing_newline", "v1.2.3\n"},
		{"leading_zero_major", "v01.2.3"},
		{"leading_zero_minor", "v1.02.3"},
		{"leading_zero_patch", "v1.2.03"},
		{"leading_zero_prerelease", "v1.2.3-rc.01"},
		{"empty_prerelease", "v1.2.3-"},
		{"empty_identifier", "v1.2.3-rc..1"},
		{"underscore", "v1.2.3-rc_1"},
		{"unicode", "v1.2.3-β"},
		{"metadata", "v1.2.3+build.5"},
		{"metadata_case", "v2.3.4+Incompatible"},
		{"extra_metadata", "v2.3.4+incompatible.extra"},
		{"hyphenated_identifier", "v1.2.3-alpha-beta"},
		{"hyphen_only_identifier", "v1.2.3--"},
		{"hyphenated_pseudo_base", "v1.2.3-alpha-beta.0.20240229123456-abcdef123456"},
		{"no_base_incompatible", "v0.0.0-20240229123456-abcdef123456+incompatible"},
		{"nonzero_major_no_base_incompatible", "v2.0.0-20240229123456-abcdef123456+incompatible"},
		{"zero_patch_pseudo_base", "v1.2.0-0.20240229123456-abcdef123456"},
		{"zero_version_pseudo_base", "v0.0.0-0.20240229123456-abcdef123456"},
		{"wrong_no_base_core", "v0.1.0-20240229123456-abcdef123456"},
		{"missing_zero_marker", "v1.2.3-rc.20240229123456-abcdef123456"},
		{"wrong_zero_marker", "v1.2.3-1.20240229123456-abcdef123456"},
		{"short_timestamp", "v0.0.0-2024022912345-abcdef123456"},
		{"long_timestamp", "v0.0.0-202402291234567-abcdef123456"},
		{"fractional_timestamp", "v0.0.0-20240229123456.1-abcdef123456"},
		{"nondigit_timestamp", "v0.0.0-20240229T23456-abcdef123456"},
		{"empty_revision", "v0.0.0-20240229123456-"},
		{"short_revision", "v0.0.0-20240229123456-abcdef"},
		{"nonhex_revision", "v0.0.0-20240229123456-abcdefg12345"},
		{"uppercase_revision", "v0.0.0-20240229123456-ABCDEF123456"},
		{"pseudo_metadata", "v1.2.4-0.20240229123456-abcdef123456+build.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rpmModuleVersion(tt.version, "20261019")
			if err == nil || got != "" {
				t.Fatalf("rpmModuleVersion(%q) = %q, %v; want empty version and error, not a fallback", tt.version, got, err)
			}
		})
	}
}

func TestRPMModuleVersionPackagingDates(t *testing.T) {
	valid := []string{"20261019", "20240229", "20000229", "00010101", "99991231"}
	invalid := []string{
		"", "2026-10-19", "2026101", "202610190", "20261019\n", " 20261019",
		"20260229", "19000229", "21000229", "20260431", "20261301", "20260001",
		"20260100", "20260132", "00000101", "２０２６１０１９", "2026a019",
	}
	// The date contract applies even when the output is a release/prerelease.
	versions := []string{"v1.2.3", "v1.2.3-rc.1", "v0.0.0-20240229123456-abcdef123456"}
	for _, version := range versions {
		for _, date := range valid {
			t.Run(version+"/valid/"+date, func(t *testing.T) {
				if _, err := rpmModuleVersion(version, date); err != nil {
					t.Fatalf("valid date %q: %v", date, err)
				}
			})
		}
		for _, date := range invalid {
			t.Run(version+"/invalid/"+date, func(t *testing.T) {
				if got, err := rpmModuleVersion(version, date); err == nil || got != "" {
					t.Fatalf("invalid date %q: got %q, %v", date, got, err)
				}
			})
		}
	}
}

func TestRPMModuleVersionPseudoTimestamps(t *testing.T) {
	valid := []string{"20240229123456", "20000229000000", "00010101000000", "99991231235959"}
	invalid := []string{
		"20260229123456", "19000229123456", "21000229123456", "20260431123456",
		"20241301123456", "20240001123456", "20240100123456", "20240132123456",
		"20240101240000", "20240101006000", "20240101000060", "00000101000000",
	}
	for _, prefix := range []string{"v0.0.0-", "v1.2.4-0.", "v1.2.3-rc.1.0."} {
		for _, stamp := range valid {
			t.Run(prefix+"/valid/"+stamp, func(t *testing.T) {
				version := prefix + stamp + "-abcdef123456"
				if got, err := rpmModuleVersion(version, "20261019"); err != nil || !strings.Contains(got, "git20261019.abcdef1") {
					t.Fatalf("valid timestamp in %q: got %q, %v", version, got, err)
				}
			})
		}
		for _, stamp := range invalid {
			t.Run(prefix+"/invalid/"+stamp, func(t *testing.T) {
				version := prefix + stamp + "-abcdef123456"
				if got, err := rpmModuleVersion(version, "20261019"); err == nil || got != "" {
					t.Fatalf("invalid timestamp in %q: got %q, %v", version, got, err)
				}
			})
		}
	}
}

func TestRPMModuleVersionUsesPackagingDate(t *testing.T) {
	for _, version := range []string{
		"v0.0.0-20240229123456-abcdef123456",
		"v1.2.4-0.20240229123456-abcdef123456",
		"v1.2.3-rc.1.0.20240229123456-abcdef123456",
	} {
		t.Run(version, func(t *testing.T) {
			first, err := rpmModuleVersion(version, "20261019")
			if err != nil {
				t.Fatal(err)
			}
			second, err := rpmModuleVersion(version, "20261020")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Replace(first, "git20261019.", "git20261020.", 1) != second || first == second {
				t.Fatalf("packaging date was not used: %q then %q", first, second)
			}
		})
	}
}
