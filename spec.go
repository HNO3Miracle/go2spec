package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func writeSpec(dir, gopkg, openRuyiSrc, openRuyiLib, openRuyiProgram, version string,
	pkgType packageType, dependencies []string, u *upstream) error {

	f, err := os.Create(filepath.Join(dir, "", openRuyiSrc+".spec"))
	if err != nil {
		return err
	}
	defer f.Close()

	description, err := getDescriptionForGopkg(gopkg)
	if err != nil {
		log.Printf("Could not determine description for %q: %v\n", gopkg, err)
		description = "TODO: short description"
	}
	longdescription, err := getLongDescriptionForGopkg(gopkg)
	if err != nil {
		log.Printf("Could not determine long description for %q: %v\n", gopkg, err)
		longdescription = "TODO: long description"
	}
	longdescription = convertLongDescriptionForRPM(longdescription)
	license, err := getLicenseForGopkg(gopkg)
	if err != nil {
		log.Printf("Could not determine license for %q: %v\n", gopkg, err)
		license = "TODO"
	}
	upstreamName := filepath.Base(gopkg)
	if regexp.MustCompile(`^v\d+$`).MatchString(upstreamName) {
		upstreamName = filepath.Base(filepath.Dir(gopkg))
	}
	owner, repo, err := findGitHubRepo(gopkg)
	if err != nil {
		owner = "TODO"
		repo = "TODO"
	}

	// Write the spec file content

	// SPDX header
	fmt.Fprintf(f, "# SPDX-FileCopyrightText: (C) 2026 Institute of Software, Chinese Academy of Sciences (ISCAS)\n")
	fmt.Fprintf(f, "# SPDX-FileCopyrightText: (C) 2026 openRuyi Project Contributors\n")
	fmt.Fprintf(f, "# SPDX-FileContributor: \n")
	fmt.Fprintf(f, "#\n")
	fmt.Fprintf(f, "# SPDX-License-Identifier: MulanPSL-2.0\n")
	fmt.Fprintf(f, "\n")

	// Macros
	fmt.Fprintf(f, "%%define _name           %s\n", upstreamName)
	fmt.Fprintf(f, "%%define go_import_path  %s\n", gopkg)
	fmt.Fprintf(f, "\n")

	// Header
	fmt.Fprintf(f, "Name:           %s\n", openRuyiSrc)
	fmt.Fprintf(f, "Version:        %s\n", version)
	fmt.Fprintf(f, "Release:        %%autorelease\n")
	fmt.Fprintf(f, "Summary:        %s\n", description)
	fmt.Fprintf(f, "License:        %s\n", license)
	fmt.Fprintf(f, "URL:            https://github.com/%s/%s\n", owner, repo)
	fmt.Fprintf(f, "#!RemoteAsset\n")
	fmt.Fprintf(f, "Source0:        https://github.com/%s/%s/archive/v%%{version}.tar.gz#/%%{_name}-%%{version}.tar.gz\n", owner, repo)

	switch pkgType {
	case typeLibrary:
		fmt.Fprintf(f, "BuildArch:      noarch\n")
		fmt.Fprintf(f, "BuildSystem:    golangmodules\n")
		fmt.Fprintf(f, "\n")
	case typeProgram, typeLibraryProgram, typeProgramLibrary:
		fmt.Fprintf(f, "\n")
	}

	fmt.Fprintf(f, "BuildRequires:  go\n")
	fmt.Fprintf(f, "BuildRequires:  go-rpm-macros\n")
	// And other BuildRequires from dependencies
	rpmDeps := convertDependenciesToRPM(u.repoDeps)
	sort.Strings(rpmDeps)
	for _, dep := range rpmDeps {
		fmt.Fprintf(f, "BuildRequires:  %s\n", dep)
	}

	// For different package types, write different sections
	switch pkgType {
	case typeLibrary:
		writeRPMLibraryPackage(f, gopkg, openRuyiLib, longdescription, rpmDeps)
	case typeProgram:
		log.Printf("Nothing to do for program package.\n")
	// TODO: what can this be used for? ExclusiveArch %%{go_arches}?
	//	writeRPMProgramPackage(f, gopkg, openRuyiProgram, longdescription)
	case typeLibraryProgram:
		writeRPMLibraryPackage(f, gopkg, openRuyiLib, longdescription, rpmDeps)
		writeRPMProgramSubpackage(f, gopkg, openRuyiProgram, openRuyiSrc, description)
	case typeProgramLibrary:
		//writeRPMProgramPackage(f, gopkg, openRuyiProgram, longdescription)
		writeRPMLibrarySubpackage(f, gopkg, openRuyiLib, openRuyiSrc, longdescription, rpmDeps)
	default:
		log.Fatalf("Invalid pkgType %d in writeRPMSpec(), aborting", pkgType)
	}

	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "%%description\n")
	fmt.Fprintf(f, "%s\n", longdescription)
	fmt.Fprintf(f, "\n")

	// %files
	writeRPMFilesSection(f, openRuyiSrc, openRuyiLib, openRuyiProgram, pkgType)

	// %changelog
	fmt.Fprintf(f, "%%changelog\n")
	fmt.Fprintf(f, "%%{?autochangelog}\n\n")

	return nil
}

// For library package
func writeRPMLibraryPackage(f *os.File, gopkg, openRuyiLib, longdesc string, deps []string) {
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "Provides:       go(%s) = %%{version}\n", gopkg)
	// 库包的运行时依赖
	if len(deps) > 0 {
		for _, dep := range deps {
			fmt.Fprintf(f, "Requires:       %s\n", dep)
		}
		fmt.Fprintf(f, "\n")
	}
}

// For library subpackage
func writeRPMLibrarySubpackage(f *os.File, gopkg, openRuyiLib, openRuyiSrc, longdesc string, deps []string) {
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "%%package -n %s\n", openRuyiLib)
	fmt.Fprintf(f, "Summary:        Development files of %s\n", filepath.Base(gopkg))
	fmt.Fprintf(f, "Provides:       go(%s) = %%{version}\n", gopkg)
	fmt.Fprintf(f, "BuildArch:      noarch\n")
	for _, dep := range deps {
		fmt.Fprintf(f, "Requires:       %s\n", dep)
	}
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "%%description -n %s\n", openRuyiLib)
	fmt.Fprintf(f, "%s\n", longdesc)
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "This package provides the Go source files of %s for development.\n")
}

// For program subpackage
func writeRPMProgramSubpackage(f *os.File, gopkg, openRuyiProgram, openRuyiSrc, description string) {
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "%%package -n %s\n", openRuyiProgram)
	fmt.Fprintf(f, "Summary:        Executable of %s\n", filepath.Base(gopkg))
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "%%description -n %s\n", openRuyiProgram)
	fmt.Fprintf(f, "%s\n", description)
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "This package contains the %s executable.\n", filepath.Base(gopkg))
}

func writeRPMFilesSection(f *os.File, openRuyiSrc, openRuyiLib, openRuyiProgram string, pkgType packageType) {
	switch pkgType {
	case typeLibrary:
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE*\n")
		fmt.Fprintf(f, "%%doc README*\n")
		fmt.Fprintf(f, "%%{go_sys_gopath}/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")

	case typeProgram:
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE*\n")
		fmt.Fprintf(f, "%%doc README*\n")
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")

	case typeLibraryProgram:
		// 库包文件（主包）
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE*\n")
		fmt.Fprintf(f, "%%doc README*\n")
		fmt.Fprintf(f, "%%{go_sys_gopath}/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")
		// 程序子包文件
		fmt.Fprintf(f, "%%files -n %s\n", openRuyiProgram)
		fmt.Fprintf(f, "%%license LICENSE*\n")
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")

	case typeProgramLibrary:
		// 程序主包文件
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE*\n")
		fmt.Fprintf(f, "%%doc README*\n")
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")
		// 库子包文件
		fmt.Fprintf(f, "%%files -n %s\n", openRuyiLib)
		fmt.Fprintf(f, "%%license LICENSE*\n")
		fmt.Fprintf(f, "%%{go_sys_gopath}/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")
	}
}

func convertLongDescriptionForRPM(openRuyiSrc string) string {
	// 移除 Debian 特有的格式
	lines := strings.Split(openRuyiSrc, "\n")
	var result []string
	for _, line := range lines {
		// 移除行首空格
		line = strings.TrimPrefix(line, " ")
		// 将 "." 单独行转换为空行
		if line == "." {
			line = ""
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func convertDependenciesToRPM(goPkgs []string) []string {
	// 使用 map 来去重和提取顶级包路径
	topLevelPkgs := make(map[string]bool)

	for _, goPkg := range goPkgs {
		// 提取顶级包：github.com/user/repo/subpkg -> github.com/user/repo
		parts := strings.Split(goPkg, "/")
		var topLevel string

		if len(parts) >= 3 {
			// 对于大多数情况（github.com/user/repo/...），只取前三部分
			topLevel = strings.Join(parts[:3], "/")

			// 例外情况：如果第四部分以 'v' 开头（版本号），则保留第四部分
			// 例如：github.com/minio/madmin-go/v3 -> github.com/minio/madmin-go/v3
			if len(parts) >= 4 && strings.HasPrefix(parts[3], "v") {
				topLevel = strings.Join(parts[:4], "/")
			}
		} else if len(parts) > 0 {
			// 如果路径部分少于 3 个，保留整个路径
			topLevel = goPkg
		}

		topLevelPkgs[topLevel] = true
	}

	// 转换为 RPM 格式并排序
	var rpmDeps []string
	for pkg := range topLevelPkgs {
		rpmDeps = append(rpmDeps, fmt.Sprintf("go(%s)", pkg))
	}
	sort.Strings(rpmDeps)

	return rpmDeps
}
