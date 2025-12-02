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
	fmt.Fprintf(f, "Source0:        https://github.com/%s/%s/archive/v%%{version}.tar.gz#/%%{_name}-%%{version}.tar.gz\n", owner, repo)

	fmt.Fprintf(f, "BuildRequires:  go\n")
	// And other BuildRequires from dependencies
	rpmDeps := convertDependenciesToRPM(dependencies)
	sort.Strings(rpmDeps)
	for _, dep := range rpmDeps {
		fmt.Fprintf(f, "BuildRequires:  %s\n", dep)
	}
	fmt.Fprintf(f, "\n")

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

	// %prep
	fmt.Fprintf(f, "%%prep\n")
	fmt.Fprintf(f, "%%autosetup -n %%{_name}-%%{version}\n")
	if len(u.vendorDirs) > 0 {
		fmt.Fprintf(f, "# Remove bundled dependencies\n")
		for _, vdir := range u.vendorDirs {
			fmt.Fprintf(f, "rm -rf %s\n", vdir)
		}
	}
	if u.hasGodeps {
		fmt.Fprintf(f, "rm -rf Godeps/_workspace\n")
	}
	fmt.Fprintf(f, "\n")

	// %build
	fmt.Fprintf(f, "%%build\n")
	switch pkgType {
	case typeLibrary:
		fmt.Fprintf(f, "# Library package - no build needed\n")
	case typeProgram, typeLibraryProgram, typeProgramLibrary:
		fmt.Fprintf(f, "export GOPATH=%%{_builddir}/go\n")
		fmt.Fprintf(f, "export GOFLAGS=\"-buildmode=pie -trimpath -mod=readonly -modcacherw\"\n")
		fmt.Fprintf(f, "export CGO_CFLAGS=\"${CFLAGS:-%%optflags}\"\n")
		fmt.Fprintf(f, "export CGO_CXXFLAGS=\"${CXXFLAGS:-%%optflags}\"\n")
		fmt.Fprintf(f, "export CGO_LDFLAGS=\"${LDFLAGS:-%%__global_ldflags}\"\n")
		fmt.Fprintf(f, "go build -v -o %%{_name} .\n")
	}
	fmt.Fprintf(f, "\n")

	// %install
	fmt.Fprintf(f, "%%install\n")
	switch pkgType {
	case typeLibrary:
		fmt.Fprintf(f, "# Install source code for library package\n")
		fmt.Fprintf(f, "install -d %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}\n")
		fmt.Fprintf(f, "cp -pav *. go go. mod go.sum %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}/ || :\n")
		fmt.Fprintf(f, "find .  -mindepth 1 -type d -not -path './.*' -exec install -d %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}/{} \\;\n")
		fmt.Fprintf(f, "find . -type f -name '*.go' -exec install -m 0644 {} %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}/{} \\;\n")
	case typeProgram:
		fmt.Fprintf(f, "install -D -m 0755 %%{_name} %%{buildroot}%%{_bindir}/%%{_name}\n")
	case typeLibraryProgram, typeProgramLibrary:
		fmt.Fprintf(f, "# Install binary\n")
		fmt.Fprintf(f, "install -D -m 0755 %%{_name} %%{buildroot}%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")
		fmt.Fprintf(f, "# Install source code for library package\n")
		fmt.Fprintf(f, "install -d %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}\n")
		fmt.Fprintf(f, "cp -pav *.go go.mod go.sum %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}/ || :\n")
		fmt.Fprintf(f, "find . -mindepth 1 -type d -not -path './.*' -exec install -d %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}/{} \\;\n")
		fmt.Fprintf(f, "find . -type f -name '*.go' -exec install -m 0644 {} %%{buildroot}%%{_datadir}/gocode/src/%%{go_import_path}/{} \\;\n")
	}
	fmt.Fprintf(f, "\n")

	// %check
	fmt.Fprintf(f, "%%check\n")
	fmt.Fprintf(f, "export GOPATH=%%{_builddir}/go\n")
	fmt.Fprintf(f, "go test -v ./...\n")
	fmt.Fprintf(f, "\n")

	// %files
	writeRPMFilesSection(f, openRuyiSrc, openRuyiLib, openRuyiProgram, pkgType)

	// %changelog
	fmt.Fprintf(f, "%%changelog\n")
	fmt.Fprintf(f, "%%{?autochangelog}\n")

	return nil
}

// For library package
func writeRPMLibraryPackage(f *os.File, gopkg, openRuyiLib, longdesc string, deps []string) {
	fmt.Fprintf(f, "BuildArch:      noarch\n")
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
		fmt.Fprintf(f, "%%license LICENSE* COPYING*\n")
		fmt.Fprintf(f, "%%doc README* CHANGELOG* HISTORY*\n")
		fmt.Fprintf(f, "%%{_datadir}/gocode/src/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")

	case typeProgram:
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE* COPYING*\n")
		fmt.Fprintf(f, "%%doc README* CHANGELOG* HISTORY*\n")
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")

	case typeLibraryProgram:
		// 库包文件（主包）
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE* COPYING*\n")
		fmt.Fprintf(f, "%%doc README* CHANGELOG* HISTORY*\n")
		fmt.Fprintf(f, "%%{_datadir}/gocode/src/%%{go_import_path}\n")
		fmt.Fprintf(f, "\n")
		// 程序子包文件
		fmt.Fprintf(f, "%%files -n %s\n", openRuyiProgram)
		fmt.Fprintf(f, "%%license LICENSE* COPYING*\n")
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")

	case typeProgramLibrary:
		// 程序主包文件
		fmt.Fprintf(f, "%%files\n")
		fmt.Fprintf(f, "%%license LICENSE* COPYING*\n")
		fmt.Fprintf(f, "%%doc README* CHANGELOG* HISTORY*\n")
		fmt.Fprintf(f, "%%{_bindir}/%%{_name}\n")
		fmt.Fprintf(f, "\n")
		// 库子包文件
		fmt.Fprintf(f, "%%files -n %s\n", openRuyiLib)
		fmt.Fprintf(f, "%%license LICENSE* COPYING*\n")
		fmt.Fprintf(f, "%%{_datadir}/gocode/src/%%{go_import_path}\n")
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

// Sync with shortHostName() from pack.go
func getKnownHostsReverse() map[string]string {
	return map[string]string{
		"bazil":         "bazil.org",
		"bitbucket":     "bitbucket.org",
		"blitiri":       "blitiri. com. ar",
		"googlecloud":   "cloud.google.com",
		"googlecode":    "code.google.com",
		"codeberg":      "codeberg.org",
		"filippo":       "filippo.io",
		"fortio":        "fortio.org",
		"fyne":          "fyne.io",
		"sourcehut":     "git.sr.ht",
		"github":        "github.com",
		"gitlab":        "gitlab.com",
		"bugst":         "go.bug.st",
		"cypherpunks":   "go.cypherpunks.ru",
		"mongodb":       "go.mongodb.org",
		"opentelemetry": "go.opentelemetry.io",
		"step":          "go.step.sm",
		"uber":          "go.uber.org",
		"go4":           "go4.org",
		"gocloud":       "gocloud.dev",
		"golang":        "golang.org",
		"google":        "google.golang.org",
		"gopkg":         "gopkg.in",
		"honnef":        "honnef. co",
		"howett":        "howett.net",
		"k8s":           "k8s.io",
		"modernc":       "modernc.org",
		"pault":         "pault.ag",
		"rsc":           "rsc.io",
		"debian":        "salsa.debian.org",
		"k8s-sigs":      "sigs.k8s.io",
		"sslmate":       "software.sslmate.com",
		"zgoat":         "zgo.at",
	}
}

func convertDependenciesToRPM(debDeps []string) []string {
	knownHostsReverse := getKnownHostsReverse()
	var rpmDeps []string

	for _, dep := range debDeps {
		// 转换 golang-xxx-dev 为 golang(xxx)
		if strings.HasPrefix(dep, "golang-") && strings.HasSuffix(dep, "-devel") {
			// golang-github-foo-bar-dev -> golang(github.com/foo/bar)
			trimmed := strings.TrimPrefix(dep, "golang-")
			trimmed = strings.TrimSuffix(trimmed, "-devel")

			// 尝试还原 Go 包路径
			parts := strings.Split(trimmed, "-")
			if len(parts) >= 2 {
				shortHost := parts[0]

				// 使用 knownHosts 反向映射查找完整主机名
				var fullHost string
				if fqdn, ok := knownHostsReverse[shortHost]; ok {
					fullHost = fqdn
				} else {
					// 未知主机，保持原样（可能是自定义域名）
					fullHost = shortHost
					log.Printf("WARNING: Unknown host shortname %q in dependency %q\n", shortHost, dep)
				}

				importPath := fullHost + "/" + strings.Join(parts[1:], "/")
				rpmDeps = append(rpmDeps, fmt.Sprintf("golang(%s)", importPath))
			} else {
				rpmDeps = append(rpmDeps, fmt.Sprintf("golang(%s)", trimmed))
			}
		} else {
			rpmDeps = append(rpmDeps, dep)
		}
	}
	return rpmDeps
}
