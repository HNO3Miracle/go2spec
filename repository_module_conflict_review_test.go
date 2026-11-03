package main

import (
	"testing"

	"golang.org/x/mod/module"
)

func TestModuleRequirementConflictIsReportedWithoutMatchingDependency(t *testing.T) {
	modules := []module.Version{
		{Path: "example.test/unused", Version: "v1.0.0"},
		{Path: "example.test/unused", Version: "v1.1.0"},
	}
	conflicts := attachRepositoryModuleRequirements(nil, modules)
	if len(conflicts) != 1 || conflicts[0] != "conflicting --module-require versions for example.test/unused: v1.0.0, v1.1.0; no version was selected" {
		t.Fatalf("unmatched module conflict not reported deterministically: %v", conflicts)
	}
}
