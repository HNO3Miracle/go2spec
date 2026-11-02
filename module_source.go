package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

type moduleSource struct {
	Module       module.Version
	RPMVersion   string
	FileName     string
	SourceURL    string
	SHA256       string
	Zip          string
	Dir          string
	GoVersion    string
	Requires     []module.Version
	LicenseFiles []string
}

type moduleDownload struct {
	Path    string
	Version string
	Info    string
	GoMod   string
	Zip     string
	Error   string
	Origin  *moduleOrigin
}

// These optional Go metadata fields are consistency checks, not a replacement
// for Go's download/checksum verification or proof from an independent VCS read.
type moduleOrigin struct {
	Hash string
	Ref  string
}

type publicModuleZipFetcher func(context.Context, string, module.Version, string, int) (string, error)

var fetchPublicModuleZip publicModuleZipFetcher = downloadPublicModuleZip

func moduleProxySourceURL(mod module.Version, fileName string) (string, error) {
	path, err := module.EscapePath(mod.Path)
	if err != nil {
		return "", err
	}
	version, err := module.EscapeVersion(mod.Version)
	if err != nil {
		return "", err
	}
	return "https://proxy.golang.org/" + path + "/@v/" + version + ".zip#/" + fileName, nil
}

func checkModuleOrigin(version string, origin *moduleOrigin) error {
	if origin == nil {
		return nil
	}
	if module.IsPseudoVersion(version) {
		rev, err := module.PseudoVersionRev(version)
		if err != nil || (origin.Hash != "" && !strings.HasPrefix(origin.Hash, rev)) {
			return fmt.Errorf("Origin hash differs from the selected pseudo-version")
		}
	} else if strings.HasPrefix(origin.Ref, "refs/tags/") && path.Base(origin.Ref) != strings.TrimSuffix(version, "+incompatible") {
		return fmt.Errorf("Origin tag differs from the selected version")
	}
	return nil
}

func combinedGoEnv(work string) []string {
	overrides := map[string]string{
		"GOWORK":       "off",
		"GO111MODULE":  "on",
		"GOTOOLCHAIN":  "local",
		"GOFLAGS":      "",
		"GOEXPERIMENT": "",
		"GOMODCACHE":   filepath.Join(work, "modcache"),
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, replaced := overrides[key]; !replaced {
			env = append(env, value)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func downloadModuleSource(ctx context.Context, work string, mod module.Version, date string, index int) (moduleSource, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", mod.Path+"@"+mod.Version)
	cmd.Dir = work
	cmd.Env = combinedGoEnv(work)
	data, err := cmd.Output()
	if err != nil {
		// Go diagnostics can contain authenticated proxy URLs. Do not echo them.
		if ctx.Err() != nil {
			return moduleSource{}, fmt.Errorf("%s@%s: download: %w", mod.Path, mod.Version, ctx.Err())
		}
		return moduleSource{}, fmt.Errorf("%s@%s: go mod download failed; check the selected version and Go network configuration", mod.Path, mod.Version)
	}
	var result moduleDownload
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&result); err != nil {
		return moduleSource{}, fmt.Errorf("%s@%s: invalid download metadata", mod.Path, mod.Version)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return moduleSource{}, fmt.Errorf("%s@%s: expected one download result", mod.Path, mod.Version)
	}
	if result.Error != "" || result.Path != mod.Path || result.Version != mod.Version {
		return moduleSource{}, fmt.Errorf("%s@%s: download identity/version mismatch or module error", mod.Path, mod.Version)
	}
	return verifyModuleSource(ctx, work, mod, date, index, result)
}

func verifyModuleSource(ctx context.Context, work string, mod module.Version, date string, index int, result moduleDownload) (moduleSource, error) {
	fail := func(err error) (moduleSource, error) {
		return moduleSource{}, fmt.Errorf("%s@%s: source verification: %w", mod.Path, mod.Version, err)
	}
	for _, file := range []struct {
		name  string
		limit int64
	}{{result.Info, 1 << 20}, {result.GoMod, modzip.MaxGoMod}, {result.Zip, modzip.MaxZipFile}} {
		if err := moduleCacheFile(work, file.name, file.limit); err != nil {
			return fail(err)
		}
	}
	infoBytes, err := os.ReadFile(result.Info)
	if err != nil {
		return fail(err)
	}
	var info struct {
		Version string
		Origin  *moduleOrigin
	}
	if err := json.Unmarshal(infoBytes, &info); err != nil || info.Version != mod.Version {
		return fail(fmt.Errorf("version in .info differs from the request"))
	}
	for _, origin := range []*moduleOrigin{result.Origin, info.Origin} {
		if err := checkModuleOrigin(mod.Version, origin); err != nil {
			return fail(err)
		}
	}
	modBytes, err := os.ReadFile(result.GoMod)
	if err != nil {
		return fail(err)
	}
	parsed, err := modfile.Parse("go.mod", modBytes, nil)
	if err != nil {
		return fail(err)
	}
	if parsed.Module == nil || parsed.Module.Mod.Path != mod.Path {
		return fail(fmt.Errorf("module directive differs from the requested path"))
	}
	// GOPATH builds cannot reproduce replacement or exclusion semantics.
	if len(parsed.Replace) != 0 || len(parsed.Exclude) != 0 {
		return fail(fmt.Errorf("replace/exclude directives require manual source and dependency review"))
	}
	if _, err := modzip.CheckZip(mod, result.Zip); err != nil {
		return fail(err)
	}
	if err := checkModuleZipExtraction(result.Zip); err != nil {
		return fail(err)
	}
	dir := filepath.Join(work, fmt.Sprintf("module-%d", index))
	if err := modzip.Unzip(dir, mod, result.Zip); err != nil {
		return fail(err)
	}
	installedMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return fail(fmt.Errorf("the source ZIP must contain its own go.mod: %w", err))
	}
	if !bytes.Equal(installedMod, modBytes) {
		return fail(fmt.Errorf("go.mod in the ZIP differs from the verified module metadata"))
	}
	rpmVersion, err := rpmModuleVersion(mod.Version, date)
	if err != nil {
		return fail(err)
	}
	hash, err := fileSHA256(result.Zip)
	if err != nil {
		return fail(err)
	}
	goVersion := "1.16"
	if parsed.Go != nil {
		goVersion = parsed.Go.Version
	}
	var requires []module.Version
	for _, r := range parsed.Require {
		requires = append(requires, r.Mod)
	}
	fileName := fmt.Sprintf("module-%d-%s.zip", index, mod.Version)
	sourceURL, err := moduleProxySourceURL(mod, fileName)
	if err != nil {
		return fail(fmt.Errorf("construct public Module Proxy Source URL: %w", err))
	}
	publicZip, err := fetchPublicModuleZip(ctx, work, mod, sourceURL, index)
	if err != nil {
		return fail(fmt.Errorf("validate emitted public Source URL: %w", err))
	}
	publicHash, err := fileSHA256(publicZip)
	if err != nil {
		return fail(fmt.Errorf("hash emitted public Source: %w", err))
	}
	if publicHash != hash {
		return fail(fmt.Errorf("emitted public Source bytes differ from the configured Go proxy ZIP; refusing a non-reproducible Source"))
	}
	if _, err := modzip.CheckZip(mod, publicZip); err != nil {
		return fail(fmt.Errorf("emitted public Source identity check: %w", err))
	}
	return moduleSource{
		Module: mod, RPMVersion: rpmVersion,
		FileName: fileName, SourceURL: sourceURL,
		SHA256: hash, Zip: result.Zip, Dir: dir, GoVersion: goVersion, Requires: requires,
	}, nil
}

func downloadPublicModuleZip(ctx context.Context, work string, mod module.Version, sourceURL string, index int) (string, error) {
	u, err := url.Parse(sourceURL)
	if err != nil || u.Scheme != "https" || u.Host != "proxy.golang.org" || u.User != nil || u.RawQuery != "" {
		return "", fmt.Errorf("emitted Source URL is not a public Go Module Proxy URL")
	}
	u.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %s", u.String(), response.Status)
	}
	if response.ContentLength > modzip.MaxZipFile {
		return "", fmt.Errorf("public Module Proxy ZIP exceeds the Go ZIP limit")
	}
	name := filepath.Join(work, fmt.Sprintf("public-module-%d.zip", index))
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, modzip.MaxZipFile+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written > modzip.MaxZipFile || (response.ContentLength >= 0 && written != response.ContentLength) {
		return "", fmt.Errorf("public Module Proxy ZIP size is invalid")
	}
	return name, nil
}

func checkModuleZipExtraction(name string) error {
	reader, err := zip.OpenReader(name)
	if err != nil {
		return err
	}
	defer reader.Close()
	raw, err := os.Open(name)
	if err != nil {
		return err
	}
	defer raw.Close()
	seen := make(map[string]bool)
	for _, file := range reader.File {
		// Go's Unzip ignores Unix modes, but the RPM's unzip would honor links.
		mode := file.Mode()
		if (!mode.IsRegular() && !mode.IsDir()) || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || file.Flags&1 != 0 {
			return fmt.Errorf("ZIP entry %q has unsupported permissions or encryption", file.Name)
		}
		if mode.IsDir() != strings.HasSuffix(file.Name, "/") || mode.Perm()&0400 == 0 || (mode.IsDir() && mode.Perm()&0100 == 0) {
			return fmt.Errorf("ZIP entry %q has inconsistent type or unreadable permissions", file.Name)
		}
		if path.Base(strings.TrimSuffix(file.Name, "/")) == ".goipath" {
			return fmt.Errorf("Source contains reserved auto-Provides marker %q", file.Name)
		}
		if seen[file.Name] {
			return fmt.Errorf("duplicate ZIP entry %q", file.Name)
		}
		seen[file.Name] = true
		// ZIP64 and timestamps do not change names or file types. Reject alternate
		// path/link encodings that a system unzip could interpret differently.
		for extra := file.Extra; len(extra) > 0; {
			if len(extra) < 4 {
				return fmt.Errorf("malformed ZIP extra field")
			}
			id, size := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
			if size > len(extra)-4 || (id != 0x0001 && id != 0x5455) {
				return fmt.Errorf("ZIP entry %q has an unsupported extra field", file.Name)
			}
			extra = extra[4+size:]
		}
		// archive/zip exposes central-directory metadata. Require the local
		// header to carry the same name, flags, method and extra fields, rather
		// than letting the system extractor make a different interpretation.
		offset, err := file.DataOffset()
		if err != nil {
			return err
		}
		header := make([]byte, 30+len(file.Name)+len(file.Extra))
		if _, err := raw.ReadAt(header, offset-int64(len(header))); err != nil {
			return fmt.Errorf("ZIP entry %q has unsupported local metadata", file.Name)
		}
		localCRC := binary.LittleEndian.Uint32(header[14:])
		localCompressed := binary.LittleEndian.Uint32(header[18:])
		localUncompressed := binary.LittleEndian.Uint32(header[22:])
		if binary.LittleEndian.Uint32(header) != 0x04034b50 ||
			binary.LittleEndian.Uint16(header[6:]) != file.Flags ||
			binary.LittleEndian.Uint16(header[8:]) != file.Method ||
			int(binary.LittleEndian.Uint16(header[26:])) != len(file.Name) ||
			int(binary.LittleEndian.Uint16(header[28:])) != len(file.Extra) ||
			string(header[30:30+len(file.Name)]) != file.Name ||
			!bytes.Equal(header[30+len(file.Name):], file.Extra) {
			return fmt.Errorf("ZIP entry %q has inconsistent local metadata", file.Name)
		}
		if file.Flags&0x0008 != 0 {
			if err := validateZIPDataDescriptor(raw, offset+int64(file.CompressedSize64), file); err != nil {
				return fmt.Errorf("ZIP entry %q has invalid data descriptor: %w", file.Name, err)
			}
		} else if localCRC != file.CRC32 || uint64(localCompressed) != file.CompressedSize64 || uint64(localUncompressed) != file.UncompressedSize64 {
			return fmt.Errorf("ZIP entry %q has inconsistent local CRC or size", file.Name)
		}
		content, err := file.Open()
		if err != nil {
			return fmt.Errorf("ZIP entry %q cannot be opened: %w", file.Name, err)
		}
		_, copyErr := io.Copy(io.Discard, content)
		closeErr := content.Close()
		if copyErr != nil {
			return fmt.Errorf("ZIP entry %q has invalid content or CRC32: %w", file.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("ZIP entry %q cannot be closed: %w", file.Name, closeErr)
		}
	}
	return nil
}

func validateZIPDataDescriptor(raw *os.File, offset int64, file *zip.File) error {
	buf := make([]byte, 24)
	n, err := raw.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return err
	}
	if n < 12 {
		return fmt.Errorf("truncated data descriptor")
	}
	pos := 0
	if binary.LittleEndian.Uint32(buf) == 0x08074b50 {
		pos = 4
	}
	if file.CompressedSize64 > uint64(^uint32(0)) || file.UncompressedSize64 > uint64(^uint32(0)) {
		if pos+20 > n {
			return fmt.Errorf("truncated ZIP64 descriptor")
		}
		if binary.LittleEndian.Uint32(buf[pos:]) != file.CRC32 ||
			binary.LittleEndian.Uint64(buf[pos+4:]) != file.CompressedSize64 ||
			binary.LittleEndian.Uint64(buf[pos+12:]) != file.UncompressedSize64 {
			return fmt.Errorf("descriptor CRC or size differs from central directory")
		}
		return nil
	}
	if pos+12 > n {
		return fmt.Errorf("truncated data descriptor")
	}
	if binary.LittleEndian.Uint32(buf[pos:]) != file.CRC32 ||
		binary.LittleEndian.Uint32(buf[pos+4:]) != uint32(file.CompressedSize64) ||
		binary.LittleEndian.Uint32(buf[pos+8:]) != uint32(file.UncompressedSize64) {
		return fmt.Errorf("descriptor CRC or size differs from central directory")
	}
	return nil
}

func moduleCacheFile(work, name string, limit int64) error {
	if name == "" || !filepath.IsAbs(name) {
		return fmt.Errorf("download metadata has no absolute artifact path")
	}
	cache, err := filepath.EvalSymlinks(filepath.Join(work, "modcache"))
	if err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(cache, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("download artifact is outside the isolated module cache")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return fmt.Errorf("download artifact is not a bounded regular file")
	}
	return nil
}
