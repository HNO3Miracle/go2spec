package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type specAssetFiles struct {
	licenseFiles []string
	readmeFile   string
}

func cleanSpecAssetPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	path = strings.TrimLeft(path, "/")
	if path == "" || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") || path == ".." {
		return ""
	}
	return path
}

func pkgsiteLicenseFilePaths(licenses []pkgsiteLicense) []string {
	topLevel := make([]pkgsiteLicense, 0, len(licenses))
	for _, license := range licenses {
		if !strings.Contains(strings.Trim(license.FilePath, "/"), "/") {
			topLevel = append(topLevel, license)
		}
	}
	if len(topLevel) > 0 {
		licenses = topLevel
	}

	seen := make(map[string]bool)
	paths := make([]string, 0, len(licenses))
	for _, license := range licenses {
		path := cleanSpecAssetPath(license.FilePath)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func getSpecAssetFilesForGopkg(gopkg string) (specAssetFiles, error) {
	info, err := getPkgsiteInfo(context.TODO(), gopkg)
	if err != nil {
		return specAssetFiles{}, err
	}

	licenses := info.Module.Licenses
	if len(licenses) == 0 {
		licenses = info.Package.Licenses
	}

	assets := specAssetFiles{
		licenseFiles: pkgsiteLicenseFilePaths(licenses),
	}
	if info.Module.Readme != nil {
		assets.readmeFile = cleanSpecAssetPath(info.Module.Readme.Filepath)
	}
	return assets, nil
}

func writeSpec(dir, gopkg, openRuyiSrc, openRuyiLib, openRuyiProgram, version string,
	pkgType packageType, u *upstream) error {

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
	assetFiles, err := getSpecAssetFilesForGopkg(gopkg)
	if err != nil {
		log.Printf("Could not determine license/doc file paths for %q: %v\n", gopkg, err)
	}
	upstreamName := filepath.Base(gopkg)
	if regexp.MustCompile(`^v\d+$`).MatchString(upstreamName) {
		upstreamName = filepath.Base(filepath.Dir(gopkg))
	}
	repoURL, err := getRepoURLForGopkg(gopkg)
	if err != nil {
		log.Printf("Could not determine repository URL for %q: %v\n", gopkg, err)
		repoURL = "TODO"
	}

	// Write the spec file content

	// SPDX header
	fmt.Fprintf(f, "# SPDX-FileCopyrightText: (C) 2026 Institute of Software, Chinese Academy of Sciences (ISCAS)\n")
	fmt.Fprintf(f, "# SPDX-FileCopyrightText: (C) 2026 openRuyi Project Contributors\n")
	fmt.Fprintf(f, "#\n")
	fmt.Fprintf(f, "# SPDX-License-Identifier: MulanPSL-2.0\n")
	fmt.Fprintf(f, "\n")

	// Macros
	fmt.Fprintf(f, "%%define _name           %s\n", upstreamName)
	fmt.Fprintf(f, "%%define go_import_path  %s\n", gopkg)

	if u.commitID != "" {
		fmt.Fprintf(f, "%%define commit_id %s\n", u.commitID)
	}
	fmt.Fprintf(f, "\n")

	// Header
	fmt.Fprintf(f, "Name:           %s\n", openRuyiSrc)

	// Program packages may need manual naming cleanup; keep the library-form
	// name visible as a second Name line until that workflow is redesigned.
	switch pkgType {
	case typeProgram:
		fmt.Fprintf(f, "Name:           %s\n", openRuyiLib)
	}

	fmt.Fprintf(f, "Version:        %s\n", version)
	fmt.Fprintf(f, "Release:        %%autorelease\n")
	fmt.Fprintf(f, "Summary:        %s\n", description)
	fmt.Fprintf(f, "License:        %s\n", license)
	fmt.Fprintf(f, "URL:            %s\n", repoURL)
	if u.sha256 != "" {
		fmt.Fprintf(f, "#!RemoteAsset:  sha256:%s\n", u.sha256)
	} else {
		fmt.Fprintf(f, "#!RemoteAsset\n")
	}
	sourceModulePath := u.modulePath
	if sourceModulePath == "" {
		sourceModulePath = gopkg
	}
	sourceURL, err := u.sourceURLForSpec(sourceModulePath)
	if err != nil {
		log.Printf("Could not determine remote source URL for %q: %v\n", gopkg, err)
		sourceURL = "TODO"
	}
	fmt.Fprintf(f, "Source0:        %s\n", sourceURL)

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
	rpmBuildDeps := convertDependenciesToRPM(u.repoDeps)
	sort.Strings(rpmBuildDeps)
	for _, dep := range rpmBuildDeps {
		fmt.Fprintf(f, "BuildRequires:  %s\n", dep)
	}
	rpmRuntimeDeps := convertDependenciesToRPM(u.repoRunDeps)
	sort.Strings(rpmRuntimeDeps)

	// For different package types, write different sections
	switch pkgType {
	case typeLibrary:
		writeRPMLibraryPackage(f, gopkg, openRuyiLib, longdescription, rpmRuntimeDeps)
	case typeProgram:
		log.Printf("Nothing to do for program package.\n")
	// TODO: what can this be used for? ExclusiveArch %%{go_arches}?
	//	writeRPMProgramPackage(f, gopkg, openRuyiProgram, longdescription)
	case typeLibraryProgram:
		writeRPMLibraryPackage(f, gopkg, openRuyiLib, longdescription, rpmRuntimeDeps)
		writeRPMProgramSubpackage(f, gopkg, openRuyiProgram, openRuyiSrc, description)
	case typeProgramLibrary:
		//writeRPMProgramPackage(f, gopkg, openRuyiProgram, longdescription)
		writeRPMLibrarySubpackage(f, gopkg, openRuyiLib, openRuyiSrc, longdescription, rpmRuntimeDeps)
	default:
		log.Fatalf("Invalid pkgType %d in writeRPMSpec(), aborting", pkgType)
	}

	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "%%description\n")
	fmt.Fprintf(f, "%s\n", longdescription)
	fmt.Fprintf(f, "\n")

	// %files
	writeRPMFilesSection(f, openRuyiSrc, openRuyiLib, openRuyiProgram, pkgType, assetFiles)

	// %changelog
	fmt.Fprintf(f, "%%changelog\n")
	fmt.Fprintf(f, "%%{?autochangelog}\n\n")

	return nil
}

// For library package
func writeRPMLibraryPackage(f *os.File, gopkg, openRuyiLib, longdesc string, deps []string) {
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "Provides:       go(%s) = %%{version}\n", gopkg)
	fmt.Fprintf(f, "\n")
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
	if len(deps) > 0 {
		for _, dep := range deps {
			fmt.Fprintf(f, "Requires:       %s\n", dep)
		}
	} else {
		fmt.Fprintf(f, "\n")
	}
	fmt.Fprintf(f, "%%description -n %s\n", openRuyiLib)
	fmt.Fprintf(f, "%s\n", longdesc)
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "This package provides the Go source files of %s for development.\n", gopkg)
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

func writeLicenseAndDocFiles(f *os.File, assetFiles specAssetFiles, includeDoc bool) {
	if includeDoc && assetFiles.readmeFile != "" {
		fmt.Fprintf(f, "%%doc %s\n", assetFiles.readmeFile)
	}
	licenseFiles := append([]string(nil), assetFiles.licenseFiles...)
	sort.Strings(licenseFiles)
	for _, licenseFile := range licenseFiles {
		fmt.Fprintf(f, "%%license %s\n", licenseFile)
	}
}

func writeRPMFilesSection(f *os.File, openRuyiSrc, openRuyiLib, openRuyiProgram string, pkgType packageType, assetFiles specAssetFiles) {
	switch pkgType {
	case typeLibrary:
		fmt.Fprintf(f, "%%files\n")
		writeLicenseAndDocFiles(f, assetFiles, true)
		fmt.Fprintf(f, "%%{go_sys_gopath}/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")

	case typeProgram:
		fmt.Fprintf(f, "%%files\n")
		writeLicenseAndDocFiles(f, assetFiles, true)
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")

	case typeLibraryProgram:
		// 库包文件（主包）
		fmt.Fprintf(f, "%%files\n")
		writeLicenseAndDocFiles(f, assetFiles, true)
		fmt.Fprintf(f, "%%{go_sys_gopath}/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")
		// 程序子包文件
		fmt.Fprintf(f, "%%files -n %s\n", openRuyiProgram)
		writeLicenseAndDocFiles(f, assetFiles, false)
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")

	case typeProgramLibrary:
		// 程序主包文件
		fmt.Fprintf(f, "%%files\n")
		writeLicenseAndDocFiles(f, assetFiles, true)
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")
		// 库子包文件
		fmt.Fprintf(f, "%%files -n %s\n", openRuyiLib)
		writeLicenseAndDocFiles(f, assetFiles, false)
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
