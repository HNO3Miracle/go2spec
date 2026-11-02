package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

var downloadRetryLog func(string)

var errRemoteAssetSkip = errors.New("skip remote asset sha256")

type downloadCandidate struct {
	source string
	url    string
}

func repoRefForVersion(version string) string {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if canonical := semver.Canonical(v); canonical != "" {
		return canonical
	}
	return v
}

func specVersionForVersion(version string) string {
	return strings.TrimPrefix(repoRefForVersion(version), "v")
}

func verifyDownload(ctx context.Context, spec *specInfo) error {
	if err := rejectAggregateUpdate(spec.FilePath); err != nil {
		return err
	}
	if spec.GoImportPath == "" {
		return fmt.Errorf("no go_import_path")
	}
	if spec.LatestVersion == "" {
		return fmt.Errorf("no latest version")
	}

	info, err := getPkgsiteInfo(ctx, spec.GoImportPath)
	if err != nil {
		return fmt.Errorf("get pkgsite info: %w", err)
	}

	candidates, archiveErr, err := downloadCandidatesForSpec(spec, info)
	if err != nil {
		return err
	}
	if archiveErr != nil {
		reportDownloadRetry("  FALLBACK: %s repo archive unavailable (%v), trying module proxy",
			spec.GoImportPath, archiveErr)
	}
	return verifyDownloadCandidates(ctx, spec.GoImportPath, candidates)
}

func downloadCandidatesForSpec(spec *specInfo, info *pkgsiteInfo) ([]downloadCandidate, error, error) {
	repoURL := gitCloneURLFromRepoURL(info.Module.RepoURL)
	if repoURL == "" {
		return nil, nil, fmt.Errorf("no repository URL")
	}

	version := spec.LatestVersion
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	repoRef := repoRefForVersion(version)
	sourceModulePath := sourceImportPathForPackage(spec.GoImportPath, info)
	proxyURL := fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.zip",
		moduleProxyEscapedPath(sourceModulePath),
		moduleProxyVersionForSpec(version))

	if moduleUsesRepoSubdir(sourceModulePath, repoURL) {
		return []downloadCandidate{
			{source: "proxy", url: proxyURL},
		}, nil, nil
	}

	tarURL, err := (&upstream{repoURL: repoURL}).tarballURLForRef(repoRef, "gz")
	if err != nil {
		return []downloadCandidate{
			{source: "proxy", url: proxyURL},
		}, err, nil
	}

	return []downloadCandidate{
		{source: "repo", url: tarURL},
		{source: "proxy", url: proxyURL},
	}, nil, nil
}

func remoteAssetSHA256(ctx context.Context, spec *specInfo, newVersion string) (string, error) {
	sourceURL, err := resolvedSource0URL(spec.FilePath, sourceVersionForSpec(newVersion))
	if err != nil {
		return "", err
	}
	releaseURL, tag := releasePageURLForSource(sourceURL)
	if releaseURL == "" {
		reportDownloadRetry("  HASH:   %s no release checksum page, downloading Source0", spec.GoImportPath)
		return downloadAssetSHA256(ctx, sourceURL)
	}
	content, err := fetchTextContext(ctx, releaseURL)
	if err == nil {
		if sum := extractSHA256FromHTML(content, tag); sum != "" {
			return sum, nil
		}
	}
	reportDownloadRetry("  HASH:   %s release checksum unavailable, downloading Source0", spec.GoImportPath)
	return downloadAssetSHA256(ctx, sourceURL)
}

func sourceVersionForSpec(version string) string {
	return specVersionForVersion(version)
}

func verifyDownloadCandidates(ctx context.Context, importPath string, candidates []downloadCandidate) error {
	var errs []string
	for i, candidate := range candidates {
		err := verifyHTTPRequest(ctx, candidate.url)
		if err == nil {
			if i > 0 {
				reportDownloadRetry("  OK:     %s verified via %s after fallback", importPath, candidate.source)
			}
			return nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", candidate.source, err))
		if i+1 < len(candidates) {
			reportDownloadRetry("  FALLBACK: %s %s probe failed (%v), trying %s",
				importPath, candidate.source, err, candidates[i+1].source)
		}
	}
	return errors.New(strings.Join(errs, "; "))
}

func verifyHTTPRequest(ctx context.Context, downloadURL string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := verifyHTTPHead(ctx, downloadURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if err := verifyHTTPRangeGet(ctx, downloadURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < 3 && isRetryableDownloadError(lastErr) {
			backoff := time.Duration(attempt*2) * time.Second
			reportDownloadRetry("  RETRY:  download probe %s failed (%v), retry %d/3 in %s",
				downloadURL, lastErr, attempt+1, backoff)
			if err := sleepWithContext(ctx, backoff); err != nil {
				return err
			}
			continue
		}
		break
	}
	return lastErr
}

func verifyHTTPHead(ctx context.Context, downloadURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create HEAD request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HEAD request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, downloadURL)
	}

	if resp.ContentLength == 0 {
		return fmt.Errorf("HEAD response has no content length from %s", downloadURL)
	}

	return nil
}

func verifyHTTPRangeGet(ctx context.Context, downloadURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create GET request: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, downloadURL)
	}

	buf := make([]byte, 1)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read download probe: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("empty download from %s", downloadURL)
	}

	return nil
}

func isRetryableDownloadError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout exceeded") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "HTTP 408") ||
		strings.Contains(msg, "HTTP 425") ||
		strings.Contains(msg, "HTTP 429") ||
		strings.Contains(msg, "HTTP 500") ||
		strings.Contains(msg, "HTTP 502") ||
		strings.Contains(msg, "HTTP 503") ||
		strings.Contains(msg, "HTTP 504")
}

func reportDownloadRetry(format string, args ...any) {
	if downloadRetryLog != nil {
		downloadRetryLog(fmt.Sprintf(format, args...))
	}
}

func readSpecLines(path string) ([]string, error) {
	if err := rejectAggregateUpdate(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func writeSpecLines(path string, lines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for i, line := range lines {
		if _, err := w.WriteString(line); err != nil {
			return err
		}
		if i < len(lines)-1 {
			if _, err := w.WriteString("\n"); err != nil {
				return err
			}
		}
	}
	if len(lines) > 0 {
		if _, err := w.WriteString("\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

func updateSpecVersion(spec *specInfo, newVersion, remoteSHA256 string, updateRemoteAsset bool) error {
	lines, err := readSpecLines(spec.FilePath)
	if err != nil {
		return err
	}

	versionLineRe := regexp.MustCompile(`^(\s*Version:\s*)(\S+)(.*)$`)
	autoChangelogRe := regexp.MustCompile(`^(\s*)%\{\?autochangelog\}(\s*)$`)
	remoteAssetHashRe := regexp.MustCompile(`^(\s*)#!RemoteAsset(?::\s*sha256:[0-9a-fA-F]+)?(\s*)$`)
	source0LineRe := regexp.MustCompile(`^(\s*Source0:\s*)(\S+)(.*)$`)
	updated := false
	source0Index := -1
	source0RemoteAssetIndex := -1
	for i, line := range lines {
		matches := versionLineRe.FindStringSubmatch(line)
		if len(matches) == 4 {
			lines[i] = matches[1] + newVersion + matches[3]
			updated = true
		}
		matches = autoChangelogRe.FindStringSubmatch(line)
		if len(matches) == 3 {
			lines[i] = matches[1] + "%autochangelog" + matches[2]
		}
		if source0Index < 0 && source0LineRe.MatchString(line) {
			source0Index = i
			if i > 0 && remoteAssetHashRe.MatchString(lines[i-1]) {
				source0RemoteAssetIndex = i - 1
			}
		}
	}
	if !updated {
		return fmt.Errorf("Version line not found in %s", spec.FilePath)
	}
	if !updateRemoteAsset {
		return writeSpecLines(spec.FilePath, lines)
	}
	if source0RemoteAssetIndex >= 0 {
		matches := remoteAssetHashRe.FindStringSubmatch(lines[source0RemoteAssetIndex])
		if remoteSHA256 != "" {
			lines[source0RemoteAssetIndex] = matches[1] + "#!RemoteAsset:  sha256:" + remoteSHA256 + matches[2]
		} else {
			lines[source0RemoteAssetIndex] = matches[1] + "#!RemoteAsset" + matches[2]
		}
	} else if source0Index >= 0 {
		insert := "#!RemoteAsset"
		if remoteSHA256 != "" {
			insert = "#!RemoteAsset:  sha256:" + remoteSHA256
		}
		lines = append(lines[:source0Index], append([]string{insert}, lines[source0Index:]...)...)
	}

	return writeSpecLines(spec.FilePath, lines)
}

type downloadResult struct {
	spec *specInfo
	err  error
}

func verifyAndUpdateSpec(ctx context.Context, spec *specInfo, dryRun bool) error {
	err := verifyDownload(ctx, spec)
	if err != nil {
		return fmt.Errorf("download verification failed: %w", err)
	}

	if !dryRun {
		newVer := specVersionForVersion(spec.LatestVersion)
		remoteSHA256, err := remoteAssetSHA256(ctx, spec, newVer)
		updateRemoteAsset := true
		if errors.Is(err, errRemoteAssetSkip) {
			updateRemoteAsset = false
			err = nil
		}
		if err != nil {
			return fmt.Errorf("compute remote asset sha256: %w", err)
		}

		if err := updateSpecVersion(spec, newVer, remoteSHA256, updateRemoteAsset); err != nil {
			return fmt.Errorf("update spec file: %w", err)
		}
	}

	return nil
}

func processUpdate(ctx context.Context, spec *specInfo, dryRun bool) error {
	err := verifyDownload(ctx, spec)
	if err != nil {
		return err
	}

	if !dryRun {
		newVer := specVersionForVersion(spec.LatestVersion)
		remoteSHA256, err := remoteAssetSHA256(ctx, spec, newVer)
		updateRemoteAsset := true
		if errors.Is(err, errRemoteAssetSkip) {
			updateRemoteAsset = false
			err = nil
		}
		if err != nil {
			return fmt.Errorf("compute remote asset sha256: %w", err)
		}

		if err := updateSpecVersion(spec, newVer, remoteSHA256, updateRemoteAsset); err != nil {
			return fmt.Errorf("update spec file: %w", err)
		}
	}

	return nil
}

func resolvedSource0URL(path, newVersion string) (string, error) {
	lines, err := readSpecLines(path)
	if err != nil {
		return "", err
	}

	macros := make(map[string]string)
	defineRe := regexp.MustCompile(`^%(?:define|global)\s+(\S+)\s+(.+)$`)
	fieldRe := regexp.MustCompile(`^(Name|Version):\s+(\S+)`)
	source0Re := regexp.MustCompile(`^Source0:\s+(\S+)`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if matches := defineRe.FindStringSubmatch(trimmed); len(matches) == 3 {
			macros[matches[1]] = strings.TrimSpace(matches[2])
			continue
		}
		if matches := fieldRe.FindStringSubmatch(trimmed); len(matches) == 3 {
			switch matches[1] {
			case "Name":
				macros["name"] = matches[2]
			case "Version":
				macros["version"] = newVersion
			}
			continue
		}
		if matches := source0Re.FindStringSubmatch(trimmed); len(matches) == 2 {
			if source0UsesPinnedCommit(matches[1]) {
				return "", fmt.Errorf("%w: Source0 uses pinned commit_id", errRemoteAssetSkip)
			}
			macros["version"] = newVersion
			resolved := expandRPMMacros(matches[1], macros, 16)
			if strings.Contains(resolved, "%{") {
				return "", fmt.Errorf("unresolved Source0 macros in %s: %s", path, resolved)
			}
			return strings.SplitN(resolved, "#", 2)[0], nil
		}
	}

	return "", fmt.Errorf("Source0 line not found in %s", path)
}

func source0UsesPinnedCommit(source string) bool {
	return strings.Contains(source, "%{commit_id}") || strings.Contains(source, "%{?commit_id}")
}

func expandRPMMacros(s string, macros map[string]string, limit int) string {
	re := regexp.MustCompile(`%\{(\??)([^}]+)\}`)
	expanded := s
	for i := 0; i < limit; i++ {
		changed := false
		next := re.ReplaceAllStringFunc(expanded, func(match string) string {
			parts := re.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			optional := parts[1] == "?"
			name := parts[2]
			value, ok := macros[name]
			if !ok {
				if optional {
					changed = true
					return ""
				}
				return match
			}
			changed = true
			return value
		})
		expanded = next
		if !changed {
			break
		}
	}
	return expanded
}

func releasePageURLForSource(sourceURL string) (releaseURL, tag string) {
	u, err := url.Parse(sourceURL)
	if err != nil {
		return "", ""
	}

	tag = sourceTagFromURLPath(u.Path)
	if tag == "" {
		return "", ""
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch u.Host {
	case "github.com":
		if len(parts) < 2 {
			return "", ""
		}
		repo := fmt.Sprintf("%s://%s/%s/%s", u.Scheme, u.Host, parts[0], parts[1])
		return fmt.Sprintf("%s/releases/tag/%s", repo, tag), tag
	case "gitlab.com", "salsa.debian.org":
		dashArchive := -1
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "-" && parts[i+1] == "archive" {
				dashArchive = i
				break
			}
		}
		if dashArchive <= 0 {
			return "", ""
		}
		repo := fmt.Sprintf("%s://%s/%s", u.Scheme, u.Host, strings.Join(parts[:dashArchive], "/"))
		return fmt.Sprintf("%s/-/releases/%s", repo, tag), tag
	default:
		return "", ""
	}
}

func sourceTagFromURLPath(path string) string {
	path, err := url.PathUnescape(path)
	if err != nil {
		return ""
	}

	const refsPrefix = "refs/tags/"
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part != "archive" || i+1 >= len(parts) {
			continue
		}
		rest := strings.Join(parts[i+1:], "/")
		if strings.HasPrefix(rest, refsPrefix) {
			return trimArchiveSuffix(strings.TrimPrefix(rest, refsPrefix))
		}
		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[:idx]
		}
		return trimArchiveSuffix(rest)
	}
	return ""
}

func trimArchiveSuffix(s string) string {
	for _, suffix := range []string{".tar.gz", ".tar.xz", ".tar.bz2", ".tgz", ".zip"} {
		if strings.HasSuffix(s, suffix) {
			return strings.TrimSuffix(s, suffix)
		}
	}
	return s
}

func fetchTextContext(ctx context.Context, fetchURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return "", fmt.Errorf("create GET request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, fetchURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	return string(body), nil
}

func downloadAssetSHA256(ctx context.Context, downloadURL string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		sum, err := fetchSHA256(ctx, downloadURL)
		if err == nil {
			return sum, nil
		}
		lastErr = err
		if attempt < 3 && isRetryableDownloadError(lastErr) {
			backoff := time.Duration(attempt*2) * time.Second
			reportDownloadRetry("  RETRY:  asset hash %s failed (%v), retry %d/3 in %s",
				downloadURL, lastErr, attempt+1, backoff)
			if err := sleepWithContext(ctx, backoff); err != nil {
				return "", err
			}
			continue
		}
		break
	}
	return "", lastErr
}

func fetchSHA256(ctx context.Context, downloadURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("create GET request: %w", err)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, downloadURL)
	}

	sum := sha256.New()
	if _, err := io.Copy(sum, resp.Body); err != nil {
		return "", fmt.Errorf("read download for sha256: %w", err)
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}
