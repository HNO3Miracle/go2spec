package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	maxRepomdBytes      = 2 << 20
	maxPrimaryBytes     = 256 << 20
	maxPrimaryOpenBytes = 512 << 20
	maxPrimaryPackages  = 1_000_000
	maxKeyringBytes     = 16 << 20
)

var repositoryArchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

type repositoryOptions struct {
	Distro           string
	Repository       string
	BaseURL          string
	Arch             string
	Keyrings         []string
	RequireSignature bool
	HTTPClient       *http.Client
	VerifySignature  func(string, []byte, []byte, []string) (string, error)
}

type repositorySnapshot struct {
	Distro                    string `json:"distro"`
	Repository                string `json:"repository"`
	BaseURL                   string `json:"base_url"`
	Arch                      string `json:"arch"`
	Revision                  string `json:"revision"`
	RepomdSHA256              string `json:"repomd_sha256"`
	PrimaryURL                string `json:"primary_url"`
	PrimaryChecksumType       string `json:"primary_checksum_type"`
	PrimaryChecksum           string `json:"primary_checksum"`
	PrimaryOpenChecksumType   string `json:"primary_open_checksum_type,omitempty"`
	PrimaryOpenChecksum       string `json:"primary_open_checksum,omitempty"`
	PrimaryOpenChecksumStatus string `json:"primary_open_checksum_status"`
	PrimarySize               int64  `json:"primary_size"`
	PrimaryOpenSize           int64  `json:"primary_open_size,omitempty"`
	PackageCount              int    `json:"package_count"`
	SignatureStatus           string `json:"signature_status"`
	SignatureFingerprint      string `json:"signature_fingerprint,omitempty"`
}

type rpmEVR struct {
	Epoch   string `json:"epoch"`
	Version string `json:"version"`
	Release string `json:"release,omitempty"`
}

type rpmProvider struct {
	Capability string  `json:"capability"`
	Package    string  `json:"package"`
	Arch       string  `json:"arch"`
	PackageEVR rpmEVR  `json:"package_evr"`
	Flags      string  `json:"provide_flags,omitempty"`
	ProvideEVR *rpmEVR `json:"provide_evr,omitempty"`
	Location   string  `json:"location,omitempty"`
}

type repositoryCatalog struct {
	Snapshot  repositorySnapshot
	Providers map[string][]rpmProvider
}

type repomdDocument struct {
	XMLName  xml.Name       `xml:"repomd"`
	Revision string         `xml:"revision"`
	Data     []repomdRecord `xml:"data"`
}

type repomdRecord struct {
	Type         string          `xml:"type,attr"`
	Checksum     repomdChecksum  `xml:"checksum"`
	OpenChecksum *repomdChecksum `xml:"open-checksum"`
	Location     repomdLocation  `xml:"location"`
	Size         int64           `xml:"size"`
	OpenSize     *int64          `xml:"open-size"`
}

type repomdChecksum struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type repomdLocation struct {
	Href string `xml:"href,attr"`
}

type primaryPackage struct {
	Name     string             `xml:"name"`
	Arch     string             `xml:"arch"`
	Version  primaryVersion     `xml:"version"`
	Location primaryLocation    `xml:"location"`
	Format   primaryPackageInfo `xml:"format"`
}

type primaryVersion struct {
	Epoch   string `xml:"epoch,attr"`
	Version string `xml:"ver,attr"`
	Release string `xml:"rel,attr"`
}

type primaryLocation struct {
	Href string `xml:"href,attr"`
}

type primaryPackageInfo struct {
	Provides primaryProvides `xml:"provides"`
}

type primaryProvides struct {
	Entries []primaryProvide `xml:"entry"`
}

type primaryProvide struct {
	Name    string `xml:"name,attr"`
	Flags   string `xml:"flags,attr"`
	Epoch   string `xml:"epoch,attr"`
	Version string `xml:"ver,attr"`
	Release string `xml:"rel,attr"`
}

func normalizeRepositoryOptions(options repositoryOptions) (repositoryOptions, error) {
	if strings.TrimSpace(options.Distro) == "" {
		return options, fmt.Errorf("repository distro must be specified")
	}
	if strings.TrimSpace(options.Repository) == "" {
		return options, fmt.Errorf("repository identity must be specified")
	}
	if !repositoryArchPattern.MatchString(options.Arch) || options.Arch == "src" || options.Arch == "nosrc" || options.Arch == "noarch" {
		return options, fmt.Errorf("target architecture %q is invalid; specify a binary architecture", options.Arch)
	}
	u, err := url.Parse(options.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return options, fmt.Errorf("repository base URL must be HTTPS without credentials, query or fragment")
	}
	if strings.ContainsAny(options.BaseURL, "\r\n\x00") {
		return options, fmt.Errorf("repository base URL contains control characters")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/"
	u.RawPath = ""
	options.BaseURL = u.String()
	if options.RequireSignature && len(options.Keyrings) == 0 {
		return options, fmt.Errorf("--require-signature requires at least one --gpg-keyring")
	}
	for _, keyring := range options.Keyrings {
		info, err := os.Stat(keyring)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxKeyringBytes {
			return options, fmt.Errorf("GPG keyring %q is not a bounded regular file", keyring)
		}
	}
	if options.VerifySignature == nil {
		options.VerifySignature = verifyRepomdSignature
	}
	if options.HTTPClient == nil {
		options.HTTPClient = repositoryHTTPClient(options.BaseURL)
	}
	return options, nil
}

func repositoryHTTPClient(baseURL string) *http.Client {
	base, _ := url.Parse(baseURL)
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.Host != base.Host {
				return fmt.Errorf("repository redirect must remain on the same HTTPS host")
			}
			return nil
		},
	}
}

func loadRepositoryCatalog(ctx context.Context, rawOptions repositoryOptions) (repositoryCatalog, error) {
	options, err := normalizeRepositoryOptions(rawOptions)
	if err != nil {
		return repositoryCatalog{}, err
	}
	work, err := os.MkdirTemp("", "go2spec-repo-query-")
	if err != nil {
		return repositoryCatalog{}, fmt.Errorf("create metadata work directory: %w", err)
	}
	defer os.RemoveAll(work)

	repomdURL := options.BaseURL + "repodata/repomd.xml"
	repomd, err := fetchRepositoryBytes(ctx, options.HTTPClient, repomdURL, maxRepomdBytes)
	if err != nil {
		return repositoryCatalog{}, fmt.Errorf("download repomd.xml: %w", err)
	}
	snapshot := repositorySnapshot{
		Distro:          options.Distro,
		Repository:      options.Repository,
		BaseURL:         options.BaseURL,
		Arch:            options.Arch,
		RepomdSHA256:    sha256Hex(repomd),
		SignatureStatus: "signature-not-requested",
	}
	var doc repomdDocument
	if err := decodeXMLDocument(repomd, &doc); err != nil {
		return repositoryCatalog{}, fmt.Errorf("parse repomd.xml: %w", err)
	}
	if doc.XMLName.Local != "repomd" {
		return repositoryCatalog{}, fmt.Errorf("repomd document has unexpected root %q", doc.XMLName.Local)
	}
	snapshot.Revision = strings.TrimSpace(doc.Revision)
	primary, err := findPrimaryRecord(doc.Data)
	if err != nil {
		return repositoryCatalog{}, err
	}
	primaryURL, err := resolveMetadataURL(options.BaseURL, primary.Location.Href)
	if err != nil {
		return repositoryCatalog{}, err
	}
	snapshot.PrimaryURL = primaryURL
	snapshot.PrimaryChecksumType = strings.ToLower(primary.Checksum.Type)
	snapshot.PrimaryChecksum = strings.ToLower(strings.TrimSpace(primary.Checksum.Value))
	snapshot.PrimaryOpenChecksumStatus = "not-provided"
	snapshot.PrimarySize = primary.Size
	if hasRepomdChecksum(primary.OpenChecksum) {
		snapshot.PrimaryOpenChecksumType = strings.ToLower(primary.OpenChecksum.Type)
		snapshot.PrimaryOpenChecksum = strings.ToLower(strings.TrimSpace(primary.OpenChecksum.Value))
	}
	if primary.OpenSize != nil {
		snapshot.PrimaryOpenSize = *primary.OpenSize
	}
	if err := validateRepomdRecord(primary); err != nil {
		return repositoryCatalog{}, err
	}

	if len(options.Keyrings) > 0 {
		signatureURL := repomdURL + ".asc"
		signature, err := fetchRepositoryBytes(ctx, options.HTTPClient, signatureURL, 1<<20)
		if err != nil {
			return repositoryCatalog{}, fmt.Errorf("download repomd signature: %w", err)
		}
		fingerprint, err := options.VerifySignature(work, repomd, signature, options.Keyrings)
		if err != nil {
			return repositoryCatalog{}, err
		}
		snapshot.SignatureStatus = "verified"
		snapshot.SignatureFingerprint = fingerprint
	} else if options.RequireSignature {
		return repositoryCatalog{}, fmt.Errorf("repository signature is required but no trusted keyring was supplied")
	}

	compressedPath := filepath.Join(work, "primary.compressed")
	compressedSize, compressedDigest, err := downloadRepositoryFile(ctx, options.HTTPClient, primaryURL, compressedPath, maxPrimaryBytes, primary.Checksum.Type)
	if err != nil {
		return repositoryCatalog{}, fmt.Errorf("download primary metadata: %w", err)
	}
	if compressedSize != primary.Size || compressedDigest != strings.ToLower(strings.TrimSpace(primary.Checksum.Value)) {
		return repositoryCatalog{}, fmt.Errorf("primary metadata compressed size/checksum does not match repomd.xml")
	}

	openPath := filepath.Join(work, "primary.xml")
	openChecksumType := ""
	if hasRepomdChecksum(primary.OpenChecksum) {
		openChecksumType = primary.OpenChecksum.Type
	}
	openSize, openDigest, err := expandPrimaryMetadata(compressedPath, primary.Location.Href, openPath, maxPrimaryOpenBytes, openChecksumType)
	if err != nil {
		return repositoryCatalog{}, fmt.Errorf("expand primary metadata: %w", err)
	}
	if primary.OpenSize != nil && openSize != *primary.OpenSize {
		return repositoryCatalog{}, fmt.Errorf("primary metadata open size does not match repomd.xml")
	}
	if hasRepomdChecksum(primary.OpenChecksum) {
		if openDigest != strings.ToLower(strings.TrimSpace(primary.OpenChecksum.Value)) {
			return repositoryCatalog{}, fmt.Errorf("primary metadata open checksum does not match repomd.xml")
		}
		snapshot.PrimaryOpenChecksumStatus = "verified"
	}

	file, err := os.Open(openPath)
	if err != nil {
		return repositoryCatalog{}, err
	}
	providers, packageCount, parseErr := parsePrimaryMetadata(file, options.Arch)
	closeErr := file.Close()
	if parseErr != nil {
		return repositoryCatalog{}, fmt.Errorf("parse primary metadata: %w", parseErr)
	}
	if closeErr != nil {
		return repositoryCatalog{}, closeErr
	}
	snapshot.PackageCount = packageCount
	return repositoryCatalog{Snapshot: snapshot, Providers: providers}, nil
}

func findPrimaryRecord(records []repomdRecord) (repomdRecord, error) {
	var primary repomdRecord
	found := false
	for _, record := range records {
		if record.Type != "primary" {
			continue
		}
		if found {
			return primary, fmt.Errorf("repomd.xml contains multiple primary records")
		}
		primary, found = record, true
	}
	if !found {
		return primary, fmt.Errorf("repomd.xml has no primary metadata record")
	}
	return primary, nil
}

func validateRepomdRecord(record repomdRecord) error {
	if record.Size <= 0 || record.Size > maxPrimaryBytes {
		return fmt.Errorf("primary compressed size is missing or exceeds configured limits")
	}
	if record.OpenSize != nil && (*record.OpenSize <= 0 || *record.OpenSize > maxPrimaryOpenBytes) {
		return fmt.Errorf("primary open size is invalid or exceeds configured limits")
	}
	if record.Location.Href == "" {
		return fmt.Errorf("primary metadata location is missing")
	}
	if err := validateRepomdChecksum("compressed", record.Checksum); err != nil {
		return err
	}
	if record.OpenChecksum != nil {
		if err := validateRepomdChecksum("open", *record.OpenChecksum); err != nil {
			return err
		}
	}
	return nil
}

func validateRepomdChecksum(kind string, checksum repomdChecksum) error {
	h, err := newDigest(checksum.Type)
	if err != nil {
		return fmt.Errorf("primary %s checksum: %w", kind, err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(checksum.Value))
	if err != nil || len(raw) != h.Size() {
		return fmt.Errorf("primary %s checksum has an invalid value", kind)
	}
	return nil
}

func hasRepomdChecksum(checksum *repomdChecksum) bool {
	return checksum != nil
}

func resolveMetadataURL(base, href string) (string, error) {
	if href == "" || strings.ContainsAny(href, "\\\r\n\x00") {
		return "", fmt.Errorf("primary metadata location is empty or unsafe")
	}
	ref, err := url.Parse(href)
	if err != nil || ref.IsAbs() || ref.Host != "" || ref.User != nil || ref.RawQuery != "" || ref.Fragment != "" || strings.HasPrefix(ref.Path, "/") {
		return "", fmt.Errorf("primary metadata location %q must be a relative path", href)
	}
	decoded, err := url.PathUnescape(ref.EscapedPath())
	if err != nil || strings.ContainsAny(decoded, "\\\r\n\x00") || path.Clean(decoded) != decoded || decoded == "." || decoded == ".." || strings.HasPrefix(decoded, "../") {
		return "", fmt.Errorf("primary metadata location %q is not a safe relative path", href)
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	resolved := baseURL.ResolveReference(ref)
	if resolved.Scheme != baseURL.Scheme || resolved.Host != baseURL.Host || resolved.User != nil {
		return "", fmt.Errorf("primary metadata location escapes the repository origin")
	}
	return resolved.String(), nil
}

func decodeXMLDocument(data []byte, target any) error {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if text, ok := token.(xml.CharData); ok && strings.TrimSpace(string(text)) == "" {
			continue
		}
		return fmt.Errorf("unexpected content after XML document")
	}
}

func fetchRepositoryBytes(ctx context.Context, client *http.Client, rawURL string, max int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", rawURL, response.Status)
	}
	if response.ContentLength > max {
		return nil, fmt.Errorf("GET %s exceeds %d-byte limit", rawURL, max)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("GET %s exceeds %d-byte limit", rawURL, max)
	}
	return data, nil
}

func downloadRepositoryFile(ctx context.Context, client *http.Client, rawURL, dest string, max int64, algorithm string) (int64, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("GET %s returned %s", rawURL, response.Status)
	}
	if response.ContentLength > max {
		return 0, "", fmt.Errorf("GET %s exceeds %d-byte limit", rawURL, max)
	}
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, "", err
	}
	h, err := newDigest(algorithm)
	if err != nil {
		_ = output.Close()
		return 0, "", err
	}
	written, copyErr := io.Copy(io.MultiWriter(output, h), io.LimitReader(response.Body, max+1))
	closeErr := output.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	if written > max {
		return 0, "", fmt.Errorf("GET %s exceeds %d-byte limit", rawURL, max)
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return 0, "", fmt.Errorf("GET %s size mismatch: received %d, expected %d", rawURL, written, response.ContentLength)
	}
	return written, hex.EncodeToString(h.Sum(nil)), nil
}

func expandPrimaryMetadata(compressedPath, href, dest string, max int64, algorithm string) (int64, string, error) {
	input, err := os.Open(compressedPath)
	if err != nil {
		return 0, "", err
	}
	defer input.Close()
	var reader io.Reader = input
	if strings.HasSuffix(strings.ToLower(href), ".gz") {
		gz, err := gzip.NewReader(input)
		if err != nil {
			return 0, "", err
		}
		defer gz.Close()
		reader = gz
	} else if !strings.HasSuffix(strings.ToLower(href), ".xml") {
		return 0, "", fmt.Errorf("unsupported primary metadata compression for %q", href)
	}
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, "", err
	}
	var digest hash.Hash
	writers := []io.Writer{output}
	if algorithm != "" {
		digest, err = newDigest(algorithm)
		if err != nil {
			_ = output.Close()
			return 0, "", err
		}
		writers = append(writers, digest)
	}
	written, copyErr := io.Copy(io.MultiWriter(writers...), io.LimitReader(reader, max+1))
	closeErr := output.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	if written > max {
		return 0, "", fmt.Errorf("expanded primary metadata exceeds %d-byte limit", max)
	}
	if digest == nil {
		return written, "", nil
	}
	return written, hex.EncodeToString(digest.Sum(nil)), nil
}

func newDigest(algorithm string) (hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported checksum algorithm %q; only SHA-256 and SHA-512 are accepted", algorithm)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func parsePrimaryMetadata(reader io.Reader, targetArch string) (map[string][]rpmProvider, int, error) {
	decoder := xml.NewDecoder(reader)
	var root xml.StartElement
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, 0, fmt.Errorf("read primary metadata root: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			root = start
			break
		}
	}
	if root.Name.Local != "metadata" {
		return nil, 0, fmt.Errorf("primary metadata has unexpected root %q", root.Name.Local)
	}
	catalog := make(map[string][]rpmProvider)
	seenProviders := make(map[string]bool)
	packageCount := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, packageCount, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "package" {
			if err := decoder.Skip(); err != nil {
				return nil, packageCount, err
			}
			continue
		}
		packageCount++
		if packageCount > maxPrimaryPackages {
			return nil, packageCount, fmt.Errorf("primary metadata exceeds %d package records", maxPrimaryPackages)
		}
		var pkg primaryPackage
		if err := decoder.DecodeElement(&pkg, &start); err != nil {
			return nil, packageCount, fmt.Errorf("decode package record %d: %w", packageCount, err)
		}
		if err := addPrimaryPackage(catalog, seenProviders, pkg, targetArch); err != nil {
			return nil, packageCount, fmt.Errorf("package record %d: %w", packageCount, err)
		}
	}
	if packageCount == 0 {
		return nil, 0, fmt.Errorf("primary metadata contains no package records")
	}
	for capability := range catalog {
		slices.SortFunc(catalog[capability], compareRPMProvider)
	}
	return catalog, packageCount, nil
}

func addPrimaryPackage(catalog map[string][]rpmProvider, seen map[string]bool, pkg primaryPackage, targetArch string) error {
	if pkg.Name == "" || pkg.Arch == "" || pkg.Version.Version == "" {
		return fmt.Errorf("package is missing name, arch or version")
	}
	if pkg.Arch == "src" || pkg.Arch == "nosrc" || (pkg.Arch != "noarch" && pkg.Arch != targetArch) {
		return nil
	}
	if pkg.Location.Href == "" || !safeRepositoryLocation(pkg.Location.Href) {
		return fmt.Errorf("package %s has an unsafe or missing location %q", pkg.Name, pkg.Location.Href)
	}
	packageEVR := rpmEVR{Epoch: normalizeEpoch(pkg.Version.Epoch), Version: pkg.Version.Version, Release: pkg.Version.Release}
	for _, provide := range pkg.Format.Provides.Entries {
		if !strings.HasPrefix(provide.Name, "go(") || !strings.HasSuffix(provide.Name, ")") || strings.ContainsAny(provide.Name, "\r\n\x00") {
			continue
		}
		provider := rpmProvider{
			Capability: provide.Name,
			Package:    pkg.Name,
			Arch:       pkg.Arch,
			PackageEVR: packageEVR,
			Flags:      strings.ToUpper(provide.Flags),
			Location:   pkg.Location.Href,
		}
		if provide.Version != "" || provide.Release != "" || provide.Epoch != "" {
			evr := rpmEVR{Epoch: normalizeEpoch(provide.Epoch), Version: provide.Version, Release: provide.Release}
			if evr.Version == "" {
				return fmt.Errorf("provider %s from %s has incomplete EVR", provide.Name, pkg.Name)
			}
			provider.ProvideEVR = &evr
		}
		key := providerIdentity(provider)
		if seen[key] {
			continue
		}
		seen[key] = true
		catalog[provide.Name] = append(catalog[provide.Name], provider)
	}
	return nil
}

func safeRepositoryLocation(location string) bool {
	if location == "" || strings.ContainsAny(location, "\\\r\n\x00") || strings.HasPrefix(location, "/") {
		return false
	}
	u, err := url.Parse(location)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	return err == nil && !strings.ContainsAny(decoded, "\\\r\n\x00") && path.Clean(decoded) == decoded && decoded != "." && decoded != ".." && !strings.HasPrefix(decoded, "../")
}

func normalizeEpoch(epoch string) string {
	if epoch == "" {
		return "0"
	}
	return epoch
}

func providerIdentity(provider rpmProvider) string {
	provideEVR := ""
	if provider.ProvideEVR != nil {
		provideEVR = provider.ProvideEVR.Epoch + ":" + provider.ProvideEVR.Version + "-" + provider.ProvideEVR.Release
	}
	packageEVR := provider.PackageEVR.Epoch + ":" + provider.PackageEVR.Version + "-" + provider.PackageEVR.Release
	return strings.Join([]string{provider.Capability, provider.Package, provider.Arch, packageEVR, provider.Flags, provideEVR, provider.Location}, "\x00")
}

func compareRPMProvider(a, b rpmProvider) int {
	if c := strings.Compare(a.Capability, b.Capability); c != 0 {
		return c
	}
	if c := strings.Compare(a.Package, b.Package); c != 0 {
		return c
	}
	if c := strings.Compare(a.Arch, b.Arch); c != 0 {
		return c
	}
	if c := strings.Compare(a.PackageEVR.Epoch, b.PackageEVR.Epoch); c != 0 {
		return c
	}
	if c := strings.Compare(a.PackageEVR.Version, b.PackageEVR.Version); c != 0 {
		return c
	}
	if c := strings.Compare(a.PackageEVR.Release, b.PackageEVR.Release); c != 0 {
		return c
	}
	if c := strings.Compare(a.Flags, b.Flags); c != 0 {
		return c
	}
	if a.ProvideEVR == nil || b.ProvideEVR == nil {
		if a.ProvideEVR == nil && b.ProvideEVR != nil {
			return -1
		}
		if a.ProvideEVR != nil && b.ProvideEVR == nil {
			return 1
		}
	} else {
		if c := strings.Compare(a.ProvideEVR.Epoch, b.ProvideEVR.Epoch); c != 0 {
			return c
		}
		if c := strings.Compare(a.ProvideEVR.Version, b.ProvideEVR.Version); c != 0 {
			return c
		}
		if c := strings.Compare(a.ProvideEVR.Release, b.ProvideEVR.Release); c != 0 {
			return c
		}
	}
	return strings.Compare(a.Location, b.Location)
}

func verifyRepomdSignature(dir string, repomd, signature []byte, keyrings []string) (string, error) {
	if len(keyrings) == 0 {
		return "", fmt.Errorf("repomd signature verification requires a trusted keyring")
	}
	repomdPath := filepath.Join(dir, "repomd.xml")
	signaturePath := filepath.Join(dir, "repomd.xml.asc")
	if err := os.WriteFile(repomdPath, repomd, 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(signaturePath, signature, 0600); err != nil {
		return "", err
	}
	args := []string{"--status-fd=1"}
	for i, keyring := range keyrings {
		data, err := os.ReadFile(keyring)
		if err != nil {
			return "", fmt.Errorf("read GPG keyring %q: %w", keyring, err)
		}
		if bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
			converted := filepath.Join(dir, fmt.Sprintf("trusted-keyring-%d.gpg", i))
			cmd := exec.Command("gpg", "--batch", "--yes", "--dearmor", "--output", converted, keyring)
			if output, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("convert armored GPG keyring %q: %w: %s", keyring, err, strings.TrimSpace(string(output)))
			}
			keyring = converted
		}
		args = append(args, "--keyring", keyring)
	}
	args = append(args, signaturePath, repomdPath)
	cmd := exec.Command("gpgv", args...)
	status, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("verify repomd.xml signature: %w", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "[GNUPG:]" && fields[1] == "VALIDSIG" {
			return fields[2], nil
		}
	}
	return "", fmt.Errorf("gpgv returned success without a VALIDSIG fingerprint")
}
