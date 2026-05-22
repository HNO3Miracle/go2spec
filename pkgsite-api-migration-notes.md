# pkg.go.dev API migration notes

## Goal

This migration replaces the old metadata discovery path based on GitHub API calls, `?go-get=1` HTML parsing, and `golang.org/x/tools/go/vcs` with the official pkg.go.dev API (`https://pkg.go.dev/v1beta`) plus Go module proxy fallbacks.

The main reason is that RPM spec generation should follow Go module semantics, not just Git repository layout. pkg.go.dev already resolves module paths, versions, README content, SPDX license metadata, repository URLs, and ambiguous import paths in the same ecosystem used by `go get` and `go mod`.

## Code changes

- Added `pkgsite.go` as the single pkg.go.dev API client.
- Added `pkgsite_test.go` covering API error retry, ambiguity handling, summary cleanup, license expression generation, module proxy escaping, Source0 generation, and type detection.
- Removed GitHub API client setup and credential handling from `main.go`.
- Removed `github.com/google/go-github/v60` and deprecated `golang.org/x/tools/go/vcs` dependencies from `go.mod`.
- Replaced GitHub README/license/repository lookups in `metadata.go` and `description.go` with pkgsite module/package metadata.
- Reworked `pack.go` to use pkgsite module path, package name, version, and repository URL.
- Reworked `spec.go` so `URL` and `Source0` come from pkgsite/repository/module-proxy information rather than hard-coded GitHub owner/repo parsing.

## Change classification

### API and data-source changes

These changes replace old discovery sources with Go ecosystem sources, without being output features by themselves:

- Added the pkg.go.dev v1beta client with retry handling, response-size cap, in-process metadata cache, and the HTTP cache transport wired in `main.go`.
- Removed the GitHub API client, credential/token handling, GitHub README/license lookups, `?go-get=1` HTML scraping, and `golang.org/x/tools/go/vcs` repo-root guessing.
- Replaced old metadata sources with pkgsite module/package metadata for module path, package name, repo URL, README, synopsis, license, version, imports, and ambiguity candidates.
- Added Go module proxy zip URL support and module-proxy path escaping; `.mod` parsing is still future work.
- Replaced `vcs.VCS.Tags` with local `git tag --list` over the cloned repository, and replaced VCS abstraction checkout with direct `git clone` / `git checkout`.
- Replaced the flattened `go list -f` dependency scan with structured `go list -e -json`.
- Removed `go-github`, `x/tools/go/vcs`, and obsolete transitive dependencies from `go.mod` / `go.sum`.

### Feature enhancements independent of the API migration

These are output, policy, or robustness improvements that could exist with any metadata source:

- Preserve the user-requested import path for package naming and `%define go_import_path`, while using the resolved module path for source checkout and tarball generation.
- Preserve `/vN` semantic-import-version suffixes in generated RPM names.
- Add RPM-safe summary cleanup helpers that strip markup, badges, README headings, and Go doc `Package ...` prefixes.
- Split dependencies into normal imports, test-only imports, and their union; `BuildRequires` uses the union, while library runtime `Requires` uses normal imports only. This still depends on a heuristic import-path-to-RPM conversion, so resolving imports to owning modules remains future work.
- Derive runtime/build dependency sets inside `writeSpec` instead of passing a flattened dependency list.
- Fix the missing `gopkg` argument in `writeRPMLibrarySubpackage`.
- Fall back from failed hoster tarball downloads to local `git archive` instead of failing immediately.
- Add `go.yaml.in -> yaml` to the short host-name mapping.
- Add migration notes and regression tests for retry/error handling, ambiguity, summary cleanup, license expressions, proxy escaping, Source0 generation, type detection, dependency splitting, and spec asset paths.

### Feature enhancements enabled by the new API/data sources

These user-visible improvements depend on pkgsite, module-proxy, or structured Go metadata:

- Generate real `License:` values from pkgsite SPDX license types, including multi-license SPDX expressions.
- Generate real `URL:` values for non-GitHub modules and normalize `cs.opensource.google/go/x/*` to `go.googlesource.com/*`.
- Generate `Summary:` from pkgsite package synopsis, with pkgsite README fallback and cleanup.
- Generate long `%description` from pkgsite `module.readme.contents`.
- Prefer pkgsite's latest module version, then validate it against local Git tags and check out the selected release tag before dependency/tarball discovery.
- Resolve module paths and ambiguous import paths using pkgsite candidates instead of repository-root guessing.
- Use module-proxy `Source0` zips for monorepo submodules and unsupported hosters while keeping hoster tarballs as the primary source for normal repo-root modules.
- Refuse invalid module-proxy `Source0` for commit-pinned subdirectory modules by leaving `Source0: TODO` unless a canonical pseudo-version is available.
- Use pkgsite README and license file paths to emit exact `%doc` / `%license` entries such as `Readme` or `License`, and omit `%doc` when no README exists.
- Classify program/library type from pkgsite `Package.Name == "main"`; local `main` discovery remains only as a fallback.

Future enhancements that depend on the same data-source migration include per-package `Provides` from `/v1beta/packages/{module}`, cleaner `BuildRequires` from module proxy `.mod` files, cgo detection from imports, multi-module repository fan-out, and import-path-to-owning-module resolution.

## Fields improved by official API data

| Field | Old behavior | New behavior |
|---|---|---|
| Module path | `vcs.RepoRootForImportPath` / repo root guessing | `package.modulePath` and ambiguity candidates from pkgsite |
| Version | `git describe` over repository tags | pkgsite latest module version is preferred, then validated against Git tags |
| Summary | GitHub repo description or README fallback | pkgsite package synopsis, cleaned for RPM Summary |
| License | GitHub license API or `TODO` | pkgsite SPDX license types |
| Repository URL | GitHub-only parsing / `TODO` for non-GitHub | pkgsite `module.repoUrl`; `cs.opensource.google/go/x/*` is normalized to `go.googlesource.com/*` |
| Source0 | Hoster table only | Hoster table plus `proxy.golang.org` zip fallback |
| Type guess | Any `package main` in repo meant program | Requested package `name == "main"` means program; command subpackages no longer misclassify libraries. If pkgsite reports no package name, local `main` detection remains a fallback |

## License handling

The old `githubLicenseToSPDXLicense` path converted GitHub license API keys such as `mit` or `apache-2.0` into local SPDX strings. That local conversion is intentionally removed because the new metadata source already returns SPDX license identifiers.

Current behavior:

- `getLicenseForGopkg` reads `module.licenses` from pkgsite and falls back to `package.licenses` only if the module has no license data.
- `pkgsiteLicenseExpression` uses `licenses[].types` exactly as returned by pkgsite. It does not translate GitHub keys or infer license names from filenames.
- If one license file has multiple pkgsite-detected types, they are joined with `OR`, for example `(MIT OR OFL-1.1)`.
- If multiple license files apply, the per-file groups are joined with `AND`.
- Top-level license files are preferred over nested license files, because nested files often belong to vendored or generated content.
- If pkgsite returns no license types, the generated `License:` field stays `TODO` instead of guessing.
- `%license` file entries use pkgsite `licenses[].filePath`, sanitized for spec output, so casing such as `License` is preserved.

This means `License:` correctness now depends on pkg.go.dev's official license detection data, not on GitHub's license API or a local GitHub-key-to-SPDX mapping. The generator still owns only the RPM expression policy: top-level preference, per-file `OR`, cross-file `AND`, duplicate removal, stable sorting, and `TODO` when the API does not provide SPDX types.

## Important behavior changes

- `/vN` module paths are preserved in generated package names, for example `github.com/alecthomas/chroma/v2` becomes `go-github-alecthomas-chroma-v2`. This is intentional because pkgsite reports the module path, not only the Git repository root.
- Summary differences versus the original tool are expected. The new summaries come from pkgsite package synopsis or cleaned README text.
- `golang.org/x/*` URLs now point at `go.googlesource.com/*` and Source0 uses `proxy.golang.org`.
- Monorepo submodules such as `github.com/charmbracelet/x/ansi` use module-proxy Source0 so the source is scoped to that module instead of the whole monorepo tarball.
- Root libraries with command subpackages, such as `go.yaml.in/yaml/v4`, are treated as libraries by default. Use `-type` to override when a package should be generated as a program or combined package.
- Requested subpackage paths are preserved for package naming and `%define go_import_path`; the module root is used only for source checkout and tarball generation.
- Dependency discovery now keeps normal imports separate from `TestImports` and `XTestImports`. `BuildRequires` uses the union, while library runtime `Requires` uses normal imports only.
- Commit-pinned subdirectory modules no longer emit invalid module-proxy `Source0` URLs; a canonical pseudo-version is required before the proxy can serve those sources, otherwise `Source0` is left as `TODO` for manual handling.

## Dependency data

The old implementation did not run a full compile to discover dependencies. It ran:

```bash
go list -e -f '{{join .Imports "\n"}}
{{join .TestImports "\n"}}
{{join .XTestImports "\n"}}' <repo>/...
```

Then it removed imports that are part of the packaged repository and imports from the Go standard library. The remaining import paths were all written as RPM `BuildRequires`. This means regular compile imports, internal test imports, and external test-package imports were already included, but they were merged into one set and emitted only as `BuildRequires`; no `%check` or test-only bucket existed.

The pkg.go.dev API has package-level dependency data, but it is not a complete module-level RPM dependency source:

- `/v1beta/package/{path}?imports=true` returns the direct imports for one package. These are package import paths, not module paths or RPM package names.
- `/v1beta/packages/{module}` enumerates packages in a module, but does not return the dependency graph by itself.
- `/v1beta/module/{module}` exposes module metadata such as version and repository URL. Although the OpenAPI schema contains a `goModContents` field, the field is currently empty or partial in `v1beta` responses; do not use it. Fetch `https://proxy.golang.org/<module>/@v/<version>.mod` directly instead.

For cleaner `BuildRequires`, the preferred future approach is to fetch the official module proxy `.mod` file for the selected version and parse it with `golang.org/x/mod/modfile`. Direct `require` entries can represent normal module dependencies, while `// indirect` entries can be handled separately or ignored depending on RPM policy. Package-level pkgsite `imports=true` can still be useful for cgo detection, identifying direct package imports, and resolving import paths back to modules when needed.

Test-only dependencies are now tracked separately by reading structured `go list -json` output. The generator stores normal imports, test-only imports, and their union separately. Today the union is emitted as `BuildRequires`, and only normal imports are emitted as library runtime `Requires`. Because import paths are still collapsed to RPM provides by a heuristic, a test-only import that shares the same top-level provide as a runtime import can still appear in runtime `Requires`; resolving imports to owning module paths is a follow-up. A future RPM-macro-specific enhancement can emit the test-only set as `%check`-scoped dependencies or another separate generated list.

If a dependency is needed only by tests but does not appear in the generated spec, the likely causes are:

- `go list -e -json` may return partial package data when a package cannot be loaded. The current code logs package load errors; these diagnostics still need policy decisions for when generation should fail.
- Only the default build context is analyzed. Test files behind non-default build tags, GOOS, GOARCH, cgo settings, or downstream RPM-specific tags are not visible unless those settings are passed to `go list`.
- `passthroughEnv` currently preserves only a small environment allowlist. It does not pass variables such as `GOFLAGS`, `GOOS`, `GOARCH`, `CGO_ENABLED`, `GOPROXY`, `GONOSUMDB`, or `GOPRIVATE`, so local settings that would make additional test files or private/proxy dependencies visible can be lost.
- Imports equal to the packaged repo path or prefixed by `<repo>/` are dropped as internal packages. This is correct for normal subpackages, but can be wrong for monorepos or nested modules where a same-prefix import is actually a separate packaged module.
- `convertDependenciesToRPM` collapses import paths to a top-level path such as `github.com/user/repo`, with only a simple `/vN` exception. Nested modules, vanity paths, and monorepo submodules can therefore be converted to the wrong RPM virtual provide, making a dependency look missing.
- Test cgo imports are represented as `C`, and the current code explicitly ignores `C`, so C compiler, pkg-config, and C library requirements used only by tests are not derived.
- Generated test files are not considered because `go list` does not run `go generate`.

The safer design is to keep dependency provenance all the way through generation: normal imports, in-package test imports, external test-package imports, cgo usage, and module `require` data should be stored as separate sets. The spec writer can then emit ordinary build dependencies and test-only dependencies independently, instead of filtering a flattened import list after the fact.

## Examples verified

### `go.yaml.in/yaml/v4`

pkgsite reports the root package name as `yaml`, while `cmd/go-yaml` is a separate `main` subpackage. This package is not part of the `go.mod` comparison set; it was manually run as a regression check for library/program detection. The generated spec is now a library package:

```spec
Name:           go-yaml-yaml-v4
Version:        4.0.0~rc4
Summary:        Implements YAML 1.1/1.2 encoding and decoding for Go programs
License:        Apache-2.0
BuildArch:      noarch
BuildSystem:    golangmodules
Provides:       go(go.yaml.in/yaml/v4) = %{version}
```

### `golang.org/x/net`

Old output used `TODO` metadata. New output uses official metadata and module proxy Source0:

```spec
License:        BSD-3-Clause
URL:            https://go.googlesource.com/net
Source0:        https://proxy.golang.org/golang.org/x/net/@v/v%{version}.zip#/%{_name}-%{version}.zip
```

### `github.com/charmbracelet/x/ansi`

This is a submodule inside the `github.com/charmbracelet/x` monorepo. Source0 now points at the module proxy zip:

```spec
Name:           go-github-charmbracelet-x-ansi
Source0:        https://proxy.golang.org/github.com/charmbracelet/x/ansi/@v/v%{version}.zip#/%{_name}-%{version}.zip
```

### Dependency split validation

Recent manual validation confirmed that test/build-only imports are excluded from library runtime `Requires`:

- `github.com/charmbracelet/lipgloss`: `go(github.com/aymanbagabas/go-udiff)` appears in `BuildRequires` only.
- `github.com/aliyun/alibabacloud-oss-go-sdk-v2`: `go(github.com/stretchr/testify)` appears in `BuildRequires` only.
- `google.golang.org/grpc`: `go(google.golang.org/protobuf/testing)` appears in `BuildRequires` only.

## Final comparison artifacts

The latest full comparison run is:

```text
comparison-runs/go-mod-all-final-20260522184105/
```

Important files:

- `summary.txt` - readable per-package comparison.
- `fields-full.tsv` - full field-level data for original/current output.
- `diff-summary-full.tsv` - compact list of differing fields.

This run used all 27 `require` entries from `go.mod`.

Headline result:

- Current implementation: 27/27 packages generated successfully.
- Current generated specs: no `Summary`, `License`, `URL`, or `Source0` field remains `TODO`.
- Original implementation: 2 packages failed in the comparison baseline.

Notable improvements:

- `golang.org/x/*` packages now have real URL, License, and Source0 fields.
- `github.com/charmbracelet/glamour` and `github.com/charmbracelet/x/exp/slice` generated successfully in the current implementation.
- Multi-license modules such as `github.com/alecthomas/chroma/v2` now get SPDX expressions like `(MIT OR OFL-1.1)`.

Older `comparison-runs/` directories are intermediate states from the migration and review process. Use `go-mod-all-final-20260522184105` as the current reference.

Additional ad-hoc validation after the comparison run covered `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`, `github.com/go-openapi/testify`, `github.com/hashicorp/go-secure-stdlib`, `github.com/aliyun/alibabacloud-oss-go-sdk-v2`, `github.com/aymerick/douceur`, `google.golang.org/grpc`, `github.com/apache/arrow`, `github.com/aws/smithy-go`, and `github.com/charmbracelet/x`. These runs verified the dependency split for normal module cases and exposed the legacy GOPATH / package-collection cases tracked below.

## Verification commands

```bash
go test ./...
go run . pack go.yaml.in/yaml/v4
go run . pack golang.org/x/net
go run . pack github.com/charmbracelet/x/ansi
```

## Caveats

- pkg.go.dev API is currently `v1beta`; schema and behavior may change.
- pkg.go.dev only covers public modules. Private modules still need a different path.
- Newly tagged versions may have pkgsite indexing delay.
- `pkgsiteInfoCache` is in-process only and not version-keyed yet, so requesting two versions of the same module in one process can reuse the first cached result.
- There is also an in-memory HTTP cache transport for pkgsite requests, so cache behavior has two process-local layers.
- pkgsite response bodies are capped by `pkgsiteMaxResponseBytes`.
- pkgsite requests currently retry up to 3 times with fixed linear sleeps on transport errors, HTTP 429, or HTTP 5xx responses.
- Module-proxy zip Source0 archives unpack as module-version paths such as `module@vX.Y.Z`; generated specs may still need `%setup`/`%autosetup` adjustments depending on downstream RPM macros.
- Subdirectory modules still need an end-to-end checkout/layout review. The spec `Source0` may use module-proxy zip correctly, but dependency discovery currently clones the repository and may not analyze the actual submodule directory layout.
- For requested subpath programs, `%define _name` follows the requested path while hoster tarballs usually unpack using the repository or module root name. This can require `%setup`/`%autosetup -n` handling and needs a dedicated fix before declaring subpath program output fully buildable.

## Possible split-out work: multi-module and legacy repositories

This section is not part of the current implementation plan. It records cases that may need a separate design pass after the pkgsite migration and dependency-splitting behavior are validated.

`github.com/hashicorp/go-secure-stdlib` is a package-collection repository, not a normal single Go module. pkgsite reports the repository root with `hasGoMod: false`, while subpaths such as `github.com/hashicorp/go-secure-stdlib/base62` are separate modules with their own `go.mod`, versions, tags, dependencies, and potentially licenses. `/v1beta/packages/github.com/hashicorp/go-secure-stdlib` is module-scoped, so it returns only packages in the root pseudo-module, not sibling modules.

`hasGoMod: false` alone is not enough to classify a repository as a package collection. `github.com/aymerick/douceur` is a legacy GOPATH-style repository: pkgsite reports `hasGoMod: false`, but `/v1beta/packages` returns several importable packages under the same root rather than separate sibling modules. These repositories need GOPATH-mode dependency discovery, not fan-out into submodules.

Today, `hasGoMod: false` repositories can generate silently incomplete dependencies because `go list -json` runs in module-mode defaults and may match no packages. This was observed with `github.com/aymerick/douceur` and `github.com/apache/arrow`; GOPATH-mode discovery or a refusal policy is needed in the split-out work.

The generator should distinguish three shapes:

1. **Single module with many packages**, such as `golang.org/x/text`: one module path, one version, one dependency closure. Use `/v1beta/packages/{module}` to emit `Provides: go(<pkgpath>) = %{version}` for each redistributable package in the module, and keep `go list -json ./...` for dependency discovery.
2. **Legacy GOPATH repository**, such as `github.com/aymerick/douceur`: no `go.mod`, but one repository-level package set. Treat it like a single package set, but run dependency discovery in GOPATH mode and still emit per-package `Provides`.
3. **Multi-module repository / package collection**, such as `github.com/hashicorp/go-secure-stdlib` or `github.com/charmbracelet/x`: multiple subdirectories are independent modules. Do not fold them into one source RPM, because versions, Source0 paths, dependencies, and licenses can diverge.

Default behavior for confirmed package-collection roots should be refusal with a clear diagnostic, not generation of a mostly empty root spec. The diagnostic should explain that sibling modules were detected and suggest packaging a concrete submodule path, for example `github.com/hashicorp/go-secure-stdlib/base62`. An opt-in `--fanout` or `--submodules=a,b,c` mode can later generate one independent spec per discovered submodule.

Currently, packaging a bare package-collection root such as `github.com/charmbracelet/x` fails at pkgsite lookup time with "not found"; the clearer diagnostic described here is not implemented.

Submodule discovery should avoid GitHub-specific APIs. Preferred sources are `git ls-remote --tags <repoUrl>` grouped by tags like `<subdir>/vX.Y.Z`, followed by pkgsite `/v1beta/module/{repo}/{subdir}` probes to keep only paths with `hasGoMod: true`. Each discovered submodule must run the normal single-module pipeline independently: resolve its pkgsite version, Source0, license, package list, normal/test dependencies, and spec name.

This feature depends on two dependency fixes:

- Resolve import paths to owning module paths before converting them to RPM virtual provides. For example, `github.com/hashicorp/go-secure-stdlib/base62` must map to the submodule RPM name, not the repository-root name.
- Compare internal imports against the module path being packaged, not just the repository prefix. Sibling modules share a repository prefix but are external dependencies from Go module and RPM perspectives.

## Follow-up work

- Use the documented `/v1beta/packages/{module}` endpoint to enumerate all packages in a module and improve `library+program` / `program+library` generation.
- Validate and fix subdirectory module checkout/layout so dependency discovery analyzes the module subdirectory while `Source0` remains scoped to the module.
- Validate and fix requested subpath program specs so `_name`, `Source0`, and `%setup` agree on the extracted source directory.
- Use Go module proxy `.mod` files and `golang.org/x/mod/modfile` to derive direct module dependencies for cleaner `BuildRequires`.
- Emit the tracked test-only dependency set through RPM-macro-specific `%check` dependency mechanisms if the target macro set supports it.
- Use pkgsite imports or source scanning to detect cgo and improve `ExclusiveArch` / C toolchain BuildRequires.
- Improve Source0 and `%setup` semantics for module-proxy zip sources.
- Improve retry behavior by honoring `Retry-After`, adding jitter, and using exponential backoff.
- Add fixture tests for real pkgsite API responses to catch future `v1beta` schema drift.

### Dependency follow-up plan

When implementing dependency handling, keep these work items together so the known gaps above are not reintroduced:

1. Decide which `go list -json` package load errors should be fatal instead of warnings, so partial results cannot silently hide dependencies.
2. Make the analyzed build context explicit: preserve or configure `GOFLAGS`, build tags, `GOOS`, `GOARCH`, `CGO_ENABLED`, proxy/private-module settings, and any required code generation before dependency discovery.
3. Resolve import paths to module paths before converting them to RPM virtual provides. Multi-module same-prefix filtering is tracked separately in the split-out repository-shape work above.
4. Track cgo separately, including test-only `import "C"`, so C compiler, pkg-config, and C library requirements can be emitted when tests need them.
5. Emit the already-separated test-only dependency set through target-specific `%check` dependency macros when available.
6. Add regression fixtures for external test packages, build-tagged tests, cgo tests, generated test files, and vanity paths. Nested-module and monorepo same-prefix fixtures belong with the split-out repository-shape work above.
