package main

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var specSourcePathPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/@-]+$`)

func writeAggregateSpec(dir string, plan aggregatePlan) error {
	data, err := renderAggregateSpec(plan)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, plan.Request.Name+".spec"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func renderAggregateSpec(plan aggregatePlan) ([]byte, error) {
	var b bytes.Buffer
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	p("# SPDX-FileCopyrightText: (C) 2026 Institute of Software, Chinese Academy of Sciences (ISCAS)\n")
	p("# SPDX-FileCopyrightText: (C) 2026 openRuyi Project Contributors\n#\n")
	p("# SPDX-License-Identifier: MulanPSL-2.0\n\n")
	p("%%global go2spec_combine 1\n")
	p("%%global go_import_path %s\n", plan.Request.Root)
	p("\n%%global go_submodules %%{expand:\n")
	for _, source := range plan.Sources {
		p("%s %s\n", source.Module.Path, source.RPMVersion)
	}
	p("}\n\n")
	p("Name:           %s\n", plan.Request.Name)
	p("Version:        %s\n", plan.Request.Version)
	p("Release:        %%autorelease\n")
	p("Summary:        Go library sources for %s\n", plan.Request.Root)
	p("License:        %s\n", plan.License)
	p("URL:            %s\n", plan.Request.URL)
	for i, source := range plan.Sources {
		sourceURL, err := url.Parse(source.SourceURL)
		if !specSourcePathPattern.MatchString(source.FileName) || filepath.Base(source.FileName) != source.FileName ||
			!isValidSHA256(source.SHA256) || err != nil || sourceURL.Scheme != "https" || sourceURL.Host == "" || sourceURL.User != nil ||
			sourceURL.Fragment != "/"+source.FileName {
			return nil, fmt.Errorf("%s: invalid Source URL, filename or SHA256", source.Module.Path)
		}
		p("# Module: %s@%s\n", source.Module.Path, source.Module.Version)
		p("#!RemoteAsset:  sha256:%s\n", source.SHA256)
		p("Source%d:        %s\n", i, source.SourceURL)
	}
	p("\nBuildArch:      noarch\nBuildSystem:    golangmodules\n\n")
	p("BuildRequires:  go >= %s\n", plan.GoVersion)
	p("BuildRequires:  go-rpm-macros >= 0.3\n")
	p("BuildRequires:  coreutils\nBuildRequires:  unzip\n")
	for _, dep := range plan.BuildDeps {
		p("BuildRequires:  go(%s)\n", dep)
	}
	for _, dep := range plan.RunDeps {
		p("Requires:       go(%s)\n", dep)
	}
	p("\n%%description\nGo library sources for the following independently versioned modules:\n")
	for _, source := range plan.Sources {
		p("- %s %s\n", source.Module.Path, source.Module.Version)
	}
	p("\n%%prep\n%%setup -q -c -T\n")
	for i, source := range plan.Sources {
		root := fmt.Sprintf("module-%d/%s@%s", i, source.Module.Path, source.Module.Version)
		if !specSourcePathPattern.MatchString(root) {
			return nil, fmt.Errorf("%s: unsafe ZIP root", source.Module.Path)
		}
		p("\nunzip -q \"%%{SOURCE%d}\" -d module-%d\n", i, i)
		p("mkdir -p \"gopath/src/%s\"\n", source.Module.Path)
		p("cp -a \"%s/.\" \"gopath/src/%s/\"\n", root, source.Module.Path)
		for _, license := range source.LicenseFiles {
			if !specSourcePathPattern.MatchString(license) || filepath.IsAbs(license) ||
				filepath.ToSlash(filepath.Clean(license)) != license || strings.HasPrefix(license, "../") {
				return nil, fmt.Errorf("%s: unsupported license path %q", source.Module.Path, license)
			}
			dest := fmt.Sprintf("licenses/module-%d/%s", i, license)
			p("install -D -p -m 0644 \"%s/%s\" \"%s\"\n", root, license, dest)
		}
	}
	p("\n%%build\n\n")
	p("%%install\n")
	p("export GO111MODULE=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT=\n")
	p("export GOPROXY=off GOSUMDB=off\n")
	p("mkdir -p \"%%{buildroot}%%{go_sys_gopath}/%%{go_import_path}\"\n")
	p("cp -a \"gopath/src/%%{go_import_path}/.\" \"%%{buildroot}%%{go_sys_gopath}/%%{go_import_path}/\"\n")
	p("%%go_generate_provides -i %%{go_import_path} -v %%{version}\n")
	p("\n%%check\n")
	p("export GO111MODULE=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT=\n")
	p("export GOPROXY=off GOSUMDB=off\n")
	p("export GOPATH=\"$PWD/gopath:%%{gopath}\"\n")
	p("export GOCACHE=\"$PWD/.gocache\"\n")
	p("%%{__go} test %%{go_test_flags_default} \"%%{go_import_path}/...\"\n")
	p("\n%%files\n%%license licenses/\n%%{go_sys_gopath}/%%{go_import_path}\n")
	p("\n%%changelog\n%%autochangelog\n")
	return b.Bytes(), nil
}
