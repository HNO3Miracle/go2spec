package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/net/publicsuffix"
	"golang.org/x/tools/go/vcs"
)

type packageType int

const (
	typeGuess packageType = iota
	typeLibrary
	typeProgram
	typeLibraryProgram
	typeProgramLibrary
)

// upstream describes the upstream repo we are about to package.
type upstream struct {
	rr          *vcs.RepoRoot
	tarPath     string   // path to the downloaded or generated orig tarball tempfile
	compression string   // compression method, either "gz" or "xz"
	version     string   // upstream version number, e.g. 0.0~git20180204.1d24609
	tag         string   // Latest upstream tag, if any
	commitIsh   string   // commit-ish corresponding to upstream version to be packaged
	remote      string   // git remote, set to short hostname if upstream git history is included
	firstMain   string   // import path of the first main package within repo, if any
	vendorDirs  []string // all vendor sub directories, relative to the repo directory
	repoDeps    []string // the repository paths of all dependencies (e.g. github.com/zyedidia/glob)
	hasGodeps   bool     // whether the Godeps/_workspace directory exists
	hasRelease  bool     // whether any release tags exist, for debian/watch
	isRelease   bool     // whether what we end up packaging is a tagged release
}

var errUnsupportedHoster = errors.New("unsupported hoster")

func passthroughEnv() []string {
	var relevantVariables = []string{
		"HOME",
		"PATH",
		"HTTP_PROXY", "http_proxy",
		"HTTPS_PROXY", "https_proxy",
		"ALL_PROXY", "all_proxy",
		"NO_PROXY", "no_proxy",
		"GIT_PROXY_COMMAND",
		"GIT_HTTP_PROXY_AUTHMETHOD",
	}
	var result []string
	for _, variable := range relevantVariables {
		if value, ok := os.LookupEnv(variable); ok {
			result = append(result, fmt.Sprintf("%s=%s", variable, value))
		}
	}
	return result
}

// findVendorDirs walks the directory tree rooted at dir and returns
// all vendor/ directories found, relative to dir.
func findVendorDirs(dir string) ([]string, error) {
	var vendorDirs []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.IsDir() {
			return nil // nothing to do for anything but directories
		}
		if info.Name() == ".git" ||
			info.Name() == ".hg" ||
			info.Name() == ".bzr" {
			return filepath.SkipDir
		}
		if info.Name() == "vendor" {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return fmt.Errorf("filepath.Rel: %w", err)
			}
			vendorDirs = append(vendorDirs, rel)
		}
		return nil
	})
	return vendorDirs, err
}

func downloadFile(filename, url string) error {
	dst, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer dst.Close()

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("response: %s", resp.Status)
	}

	_, err = io.Copy(dst, resp.Body)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

// get downloads the specified Go package into the provided GOPATH,
// checking out the specified revision if non-empty.
func (u *upstream) get(gopath, repo, rev string) error {
	done := make(chan struct{})
	defer close(done)
	go progressSize("go get", filepath.Join(gopath, "src"), done)

	rr, err := vcs.RepoRootForImportPath(repo, false)
	if err != nil {
		return fmt.Errorf("get repo root: %w", err)
	}
	u.rr = rr
	dir := filepath.Join(gopath, "src", rr.Root)
	if rev != "" {
		// Run "git clone {repo} {dir}" and "git checkout {tag}"
		return rr.VCS.CreateAtRev(dir, rr.Repo, rev)
	}
	// Run "git clone {repo} {dir}" (or the equivalent command for hg, svn, bzr)
	return rr.VCS.Create(dir, rr.Repo)
}

func (u *upstream) tarballUrl() (string, error) {
	repo := strings.TrimSuffix(u.rr.Repo, ".git")
	repoU, err := url.Parse(repo)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}

	switch repoU.Host {
	case "github.com":
		return fmt.Sprintf("%s/archive/%s.tar.%s",
			repo, u.tag, u.compression), nil
	case "gitlab.com", "salsa.debian.org":
		parts := strings.Split(repoU.Path, "/")
		if len(parts) < 3 {
			return "", fmt.Errorf("incomplete repo URL: %s", u.rr.Repo)
		}
		project := parts[2]
		return fmt.Sprintf("%s/-/archive/%s/%s-%s.tar.%s",
			repo, u.tag, project, u.tag, u.compression), nil
	case "git.sr.ht":
		return fmt.Sprintf("%s/archive/%s.tar.%s",
			repo, u.tag, u.compression), nil
	case "codeberg.org":
		return fmt.Sprintf("%s/archive/%s.tar.%s",
			repo, u.tag, u.compression), nil
	default:
		return "", errUnsupportedHoster
	}
}

func (u *upstream) tarballFromHoster() error {
	tarURL, err := u.tarballUrl()
	if err != nil {
		return err
	}

	done := make(chan struct{})
	go progressSize("Download", u.tarPath, done)

	log.Printf("Downloading %s", tarURL)
	err = downloadFile(u.tarPath, tarURL)

	close(done)

	return err
}

func (u *upstream) tar(gopath, repo string) error {
	f, err := os.CreateTemp("", "pack-tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	u.tarPath = f.Name()
	f.Close()

	if u.isRelease {
		if u.hasGodeps {
			log.Printf("Godeps/_workspace exists, not downloading tarball from hoster.")
		} else {
			u.compression = "gz"
			if err := u.tarballFromHoster(); err == nil {
				return nil
			} else if err == errUnsupportedHoster {
				log.Printf("INFO: Hoster does not provide release tarball\n")
			} else {
				return fmt.Errorf("tarball from hoster: %w", err)
			}
		}
	}

	u.compression = "xz"
	base := filepath.Base(repo)
	log.Printf("Generating temp tarball as %q\n", u.tarPath)
	dir := filepath.Dir(repo)
	cmd := exec.Command(
		"tar",
		"cJf",
		u.tarPath,
		"--exclude=.git",
		"--exclude=Godeps/_workspace",
		base)
	cmd.Dir = filepath.Join(gopath, "src", dir)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// findMains finds main packages within the repo (useful to auto-detect the
// package type).
func (u *upstream) findMains(gopath, repo string) error {
	cmd := exec.Command("go", "list", "-e", "-f", "{{.ImportPath}} {{.Name}}", repo+"/...")
	cmd.Dir = filepath.Join(gopath, "src", repo)
	cmd.Env = passthroughEnv()
	cmd.Stderr = os.Stderr
	log.Println("findMains: Running", cmd, "in", cmd.Dir)
	out, err := cmd.Output()
	if err != nil {
		log.Println("WARNING: In findMains:", fmt.Errorf("%q: %w", cmd.Args, err))
		log.Printf("Retrying without appending \"/...\" to repo")
		cmd = exec.Command("go", "list", "-e", "-f", "{{.ImportPath}} {{.Name}}", repo)
		cmd.Dir = filepath.Join(gopath, "src", repo)
		cmd.Env = passthroughEnv()
		cmd.Stderr = os.Stderr
		log.Println("findMains: Running", cmd, "in", cmd.Dir)
		out, err = cmd.Output()
		if err != nil {
			log.Println("WARNING: In findMains:", fmt.Errorf("%q: %w", cmd.Args, err))
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(line, "/vendor/") ||
			strings.Contains(line, "/Godeps/") ||
			strings.Contains(line, "/samples/") ||
			strings.Contains(line, "/examples/") ||
			strings.Contains(line, "/example/") {
			continue
		}
		if strings.HasSuffix(line, " main") {
			u.firstMain = strings.TrimSuffix(line, " main")
			break
		}
	}
	return nil
}

func (u *upstream) findDependencies(gopath, repo string) error {
	log.Printf("Determining dependencies\n")

	cmd := exec.Command("go", "list", "-e", "-f", "{{join .Imports \"\\n\"}}\n{{join .TestImports \"\\n\"}}\n{{join .XTestImports \"\\n\"}}", repo+"/...")
	cmd.Dir = filepath.Join(gopath, "src", repo)
	cmd.Env = passthroughEnv()
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		log.Println("WARNING: In findDependencies:", fmt.Errorf("%q: %w", cmd.Args, err))
		// See https://bugs.debian.org/992610
		log.Printf("Retrying without appending \"/...\" to repo")
		cmd = exec.Command("go", "list", "-e", "-f", "{{join .Imports \"\\n\"}}\n{{join .TestImports \"\\n\"}}\n{{join .XTestImports \"\\n\"}}", repo)
		cmd.Dir = filepath.Join(gopath, "src", repo)
		cmd.Env = passthroughEnv()
		cmd.Stderr = os.Stderr
		out, err = cmd.Output()
		if err != nil {
			log.Println("WARNING: In findDependencies:", fmt.Errorf("%q: %w", cmd.Args, err))
		}
	}

	godependencies := make(map[string]bool)
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p == "" {
			continue // skip separators between import types
		}
		// Strip packages that are included in the repository we are packaging.
		if strings.HasPrefix(p, repo+"/") || p == repo {
			continue
		}
		if p == "C" {
			// TODO: maybe parse the comments to figure out C deps from pkg-config files?
		} else {
			godependencies[p] = true
		}
	}

	if len(godependencies) == 0 {
		return nil
	}

	// Remove all packages which are in the standard lib.
	cmd = exec.Command("go", "list", "std")
	cmd.Stderr = os.Stderr
	cmd.Env = passthroughEnv()

	out, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("go list std: (args: %v): %w", cmd.Args, err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		delete(godependencies, line)
	}

	// Resolve all packages to the root of their repository.
	//roots := make(map[string]bool)
	//for dep := range godependencies {
	//	rr, err := vcs.RepoRootForImportPath(dep, false)
	//	if err != nil {
	//		log.Printf("Could not determine repo path for import path %q: %v\n", dep, err)
	//		continue
	//	}
	//	roots[rr.Root] = true
	//}

	//u.repoDeps = make([]string, 0, len(godependencies))
	//for root := range roots {
	//	u.repoDeps = append(u.repoDeps, root)
	//}

	// Sort the dependencies for reproducibility
	u.repoDeps = make([]string, 0, len(godependencies))

	return nil
}

// makeUpstreamSourceTarball downloads the specified Go package from the Internet,
// checks out the specified revision, determines the version number, removes
// vendored dependencies, and creates a tarball of the upstream source code.
// It returns an upstream struct describing the downloaded package.
func makeUpstreamSourceTarball(repo, revision string, forcePrerelease bool) (*upstream, error) {
	gopath, err := os.MkdirTemp("", "pack-tmp")
	if err != nil {
		return nil, fmt.Errorf("create tmp dir: %w", err)
	}
	defer os.RemoveAll(gopath)
	repoDir := filepath.Join(gopath, "src", repo)

	var u upstream

	log.Printf("Downloading %q\n", repo+"/...")
	if err := u.get(gopath, repo, revision); err != nil {
		return nil, fmt.Errorf("go get: %w", err)
	}

	// Verify early this repository uses git (we call pkgVersionFromGit later):
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		return nil, fmt.Errorf("not a git repository; This program currently only supports git")
	}

	u.vendorDirs, err = findVendorDirs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("find vendor dirs: %w", err)
	}
	if len(u.vendorDirs) > 0 {
		log.Printf("Deleting upstream vendor/ directories")
		for _, dir := range u.vendorDirs {
			if err := os.RemoveAll(filepath.Join(repoDir, dir)); err != nil {
				return nil, fmt.Errorf("remove all: %w", err)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(repoDir, "Godeps", "_workspace")); !os.IsNotExist(err) {
		log.Println("Godeps/_workspace detected")
		u.hasGodeps = true
	}

	log.Printf("Determining upstream version number\n")

	u.version, err = pkgVersionFromGit(repoDir, &u, revision, forcePrerelease)
	if err != nil {
		return nil, fmt.Errorf("get package version from Git: %w", err)
	}

	log.Printf("Package version is %q\n", u.version)

	if err := u.findMains(gopath, repo); err != nil {
		return nil, fmt.Errorf("find mains: %w", err)
	}

	if err := u.findDependencies(gopath, repo); err != nil {
		return nil, fmt.Errorf("find dependencies: %w", err)
	}

	if err := u.tar(gopath, repo); err != nil {
		return nil, fmt.Errorf("tar: %w", err)
	}

	return &u, nil
}

func createDirectory(openRuyiSrc string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}
	dir := filepath.Join(wd, openRuyiSrc)
	if err := os.Mkdir(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	return dir, nil
}

// Package names (both source and binary, see Package, Section 5.6.7) must
// consist only of lower case letters (a-z), digits (0-9), plus (+) and minus
// (-) signs, and periods (.). They must be at least two characters long and
// must start with an alphanumeric character.
func normalizePackageName(str string) string {
	lowerDigitPlusMinusDot := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z' || '0' <= r && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == '.' || r == '+' || r == '-':
			return r
		case r == '_':
			return '-'
		}
		return -1
	}

	safe := strings.Trim(strings.Map(lowerDigitPlusMinusDot, str), "-")
	if len(safe) < 2 {
		return "TODO"
	}

	return safe
}

// shortHostName maps a Go package import path to a canonical short hostname
func shortHostName(gopkg string, allowUnknownHoster bool) (host string, err error) {
	knownHosts := map[string]string{
		// keep the list in alphabetical order
		"bazil.org":            "bazil",
		"bitbucket.org":        "bitbucket",
		"blitiri.com.ar":       "blitiri",
		"cloud.google.com":     "googlecloud",
		"code.google.com":      "googlecode",
		"codeberg.org":         "codeberg",
		"filippo.io":           "filippo",
		"fortio.org":           "fortio",
		"fyne.io":              "fyne",
		"git.sr.ht":            "sourcehut",
		"github.com":           "github",
		"gitlab.com":           "gitlab",
		"go.bug.st":            "bugst",
		"go.cypherpunks.ru":    "cypherpunks",
		"go.mongodb.org":       "mongodb",
		"go.opentelemetry.io":  "opentelemetry",
		"go.step.sm":           "step",
		"go.uber.org":          "uber",
		"go4.org":              "go4",
		"gocloud.dev":          "gocloud",
		"golang.org":           "golang",
		"google.golang.org":    "google",
		"gopkg.in":             "gopkg",
		"honnef.co":            "honnef",
		"howett.net":           "howett",
		"k8s.io":               "k8s",
		"modernc.org":          "modernc",
		"pault.ag":             "pault",
		"rsc.io":               "rsc",
		"salsa.debian.org":     "debian",
		"sigs.k8s.io":          "k8s-sigs",
		"software.sslmate.com": "sslmate",
		"zgo.at":               "zgoat",
	}
	parts := strings.Split(gopkg, "/")
	fqdn := parts[0]
	if host, ok := knownHosts[fqdn]; ok {
		return host, nil
	}
	if !allowUnknownHoster {
		return "", fmt.Errorf("unknown hoster %q", fqdn)
	}
	suffix, _ := publicsuffix.PublicSuffix(fqdn)
	host = fqdn[:len(fqdn)-len(suffix)-len(".")]
	log.Printf("WARNING: Using %q as canonical hostname for %q. If that is not okay, please file a bug against %s.\n", host, fqdn, os.Args[0])
	return host, nil
}

// nameFromGopkg maps a Go package import path to a openRuyi package name.
// e.g. "golang.org/x/text" → "go-golang-x-text".
// This follows https://fedoraproject.org/wiki/PackagingDrafts/Go#Package_Names
func nameFromGopkg(gopkg string, t packageType, customProgPkgName string, allowUnknownHoster bool) string {
	parts := strings.Split(gopkg, "/")

	if t == typeProgram || t == typeProgramLibrary {
		if customProgPkgName != "" {
			return normalizePackageName(customProgPkgName)
		}
		return normalizePackageName(parts[len(parts)-1])
	}

	host, err := shortHostName(gopkg, allowUnknownHoster)
	if err != nil {
		log.Fatalf("Cannot derive package name: %v. See -help output for -allow_unknown_hoster\n", err)
	}
	parts[0] = host

	return normalizePackageName("go-" + strings.Join(parts, "-"))
}

func copyFile(src, dest string) error {
	input, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer input.Close()

	output, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return output.Close()
}

// mainPack is the entry point for the "pack" command.
func mainPack(args []string, usage func()) {
	flagSet := flag.NewFlagSet("pack", flag.ExitOnError)
	if usage != nil {
		flagSet.Usage = usage
	} else {
		flagSet.Usage = func() {
			fmt.Fprintf(os.Stderr, "Usage: %s [pack] [FLAG]... <go-package-importpath>\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "Example: %s pack golang.org/x/oauth2\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "\"%s pack\" downloads the specified Go package from the Internet,\nand creates new files and directories in the current working directory.\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "Flags:\n")
			flagSet.PrintDefaults()
		}
	}

	var gitRevision string
	flagSet.StringVar(&gitRevision,
		"git_revision",
		"",
		"git revision (see gitrevisions(7)) of the specified Go package\n"+
			"to check out, defaulting to the default behavior of git clone.\n"+
			"Useful in case you do not want to package e.g. current HEAD.")

	var forcePrerelease bool
	flagSet.BoolVar(&forcePrerelease,
		"force_prerelease",
		false,
		"Package @master or @tip instead of the latest tagged version")

	var pkgTypeString string
	flagSet.StringVar(&pkgTypeString,
		"type",
		"",
		"Set package type, one of:\n"+
			` * "library" (aliases: "lib", "l", "dev")`+"\n"+
			` * "program" (aliases: "prog", "p")`+"\n"+
			` * "library+program" (aliases: "lib+prog", "l+p", "both")`+"\n"+
			` * "program+library" (aliases: "prog+lib", "p+l", "combined")`)

	var customProgPkgName string
	flagSet.StringVar(&customProgPkgName,
		"program_package_name",
		"",
		"Override the program package name, and the source package name too\n"+
			"when appropriate, e.g. to name github.com/cli/cli as \"gh\"")

	var allowUnknownHoster bool
	flagSet.BoolVar(&allowUnknownHoster,
		"allow_unknown_hoster",
		false,
		"The pkg-go naming conventions use a canonical identifier for\n"+
			"the hostname (see https://go-team.pages.debian.net/packaging.html),\n"+
			"and the mapping is hardcoded into dh-make-golang.\n"+
			"In case you want to package a Go package living on an unknown hoster,\n"+
			"you may set this flag to true and double-check that the resulting\n"+
			"package name is sane. Contact pkg-go if unsure.")

	// Actual pack starts here

	// Parse flags
	err := flagSet.Parse(args)
	if err != nil {
		log.Fatalf("parse args: %v", err)
	}

	// Check for required positional argument
	if flagSet.NArg() < 1 {
		flagSet.Usage()
		os.Exit(1)
	}

	gitRevision = strings.TrimSpace(gitRevision)
	gopkg := flagSet.Arg(0)

	// Verify that the provided argument is a valid Go package import path
	rr, err := vcs.RepoRootForImportPath(gopkg, false)
	if err != nil {
		log.Fatalf("Verifying arguments: %v — did you specify a Go package import path?", err)
	}
	if gopkg != rr.Root {
		log.Printf("Continuing with repository root %q instead of specified import path %q", rr.Root, gopkg)
		gopkg = rr.Root
	}

	// Set default source and binary package names.
	openRuyiSrc := nameFromGopkg(gopkg, typeLibrary, customProgPkgName, allowUnknownHoster)
	openRuyiLib := openRuyiSrc
	openRuyiProgram := nameFromGopkg(gopkg, typeProgram, customProgPkgName, allowUnknownHoster)

	var pkgType packageType

	switch strings.TrimSpace(pkgTypeString) {
	case "", "guess":
		pkgType = typeGuess
	case "library", "lib", "l", "dev":
		pkgType = typeLibrary
	case "program", "prog", "p":
		pkgType = typeProgram
	case "library+program", "lib+prog", "l+p", "both":
		// Example packages: go-github-alecthomas-chroma,
		// go-github-tdewolff-minify, go-github-spf13-viper
		pkgType = typeLibraryProgram
	case "program+library", "prog+lib", "p+l", "combined":
		// Example package: hugo
		pkgType = typeProgramLibrary
	default:
		log.Fatalf("-type=%q not recognized, aborting\n", pkgTypeString)
	}

	if pkgType != typeGuess {
		openRuyiSrc = nameFromGopkg(gopkg, pkgType, customProgPkgName, allowUnknownHoster)
		if _, err := os.Stat(openRuyiSrc); err == nil {
			log.Fatalf("Output directory %q already exists, aborting\n", openRuyiSrc)
		}
	}

	if strings.ToLower(gopkg) != gopkg {
		// Without -git_revision, specifying the package name in the wrong case
		// will lead to two checkouts, i.e. wasting bandwidth. With
		// -git_revision, packaging might fail.
		//
		// In case it turns out that Go package names should never contain any
		// uppercase letters, we can just auto-convert the argument.
		log.Printf("WARNING: Go package names are case-sensitive. Did you really mean %q instead of %q?\n",
			gopkg, strings.ToLower(gopkg))
	}

	// Create a tarball of the upstream source
	u, err := makeUpstreamSourceTarball(gopkg, gitRevision, forcePrerelease)
	if err != nil {
		log.Fatalf("Could not create a tarball of the upstream source: %v\n", err)
	}

	if pkgType == typeGuess {
		if u.firstMain != "" {
			log.Printf("Assuming you are packaging a program (because %q defines a main package), use -type to override\n", u.firstMain)
			pkgType = typeProgram
			openRuyiSrc = nameFromGopkg(gopkg, pkgType, customProgPkgName, allowUnknownHoster)
		} else {
			pkgType = typeLibrary
		}
	}

	if _, err := os.Stat(openRuyiSrc); err == nil {
		log.Fatalf("Output directory %q already exists, aborting\n", openRuyiSrc)
	}

	orig := fmt.Sprintf("%s_%s.orig.tar.%s", openRuyiSrc, u.version, u.compression)
	log.Printf("Moving tempfile to %q\n", orig)
	// We need to copy the file, merely renaming is not enough since the file
	// might be on a different filesystem (/tmp often is a tmpfs).
	if err := copyFile(u.tarPath, orig); err != nil {
		log.Fatalf("Could not rename orig tarball from %q to %q: %v\n", u.tarPath, orig, err)
	}
	if err := os.Remove(u.tarPath); err != nil {
		log.Printf("Could not remove tempfile %q: %v\n", u.tarPath, err)
	}

	// Now we create our rpm spec file from the gathered information.
	dir, err := createDirectory(openRuyiSrc)
	if err != nil {
		log.Fatalf("Could not create repository: %v\n", err)
	}

	seen := make(map[string]bool)
	pkgdependencies := make([]string, 0, len(u.repoDeps))
	for _, dep := range u.repoDeps {
		pkgname := nameFromGopkg(dep, typeLibrary, "", allowUnknownHoster)
		if !seen[pkgname] {
			seen[pkgname] = true
			pkgdependencies = append(pkgdependencies, pkgname)
		}
	}
	// optional: sort.Strings(debdependencies)

	if err := writeSpec(dir, gopkg, openRuyiSrc, openRuyiLib, openRuyiProgram, u.version,
		pkgType, pkgdependencies, u); err != nil {
		log.Fatalf("Could not create spec file: %v\n", err)
	}

	log.Println("Done!")

	fmt.Printf("\n")
	fmt.Printf("Packaging successfully created in %s\n", dir)
	switch pkgType {
	case typeLibrary:
		fmt.Printf("    Binary: %s\n", openRuyiLib)
	case typeProgram:
		fmt.Printf("    Binary: %s\n", openRuyiProgram)
	case typeLibraryProgram:
		fmt.Printf("    Binary: %s\n", openRuyiLib)
		fmt.Printf("    Binary: %s\n", openRuyiProgram)
	case typeProgramLibrary:
		fmt.Printf("    Binary: %s\n", openRuyiProgram)
		fmt.Printf("    Binary: %s\n", openRuyiLib)
	}
	fmt.Printf("\n")
}
