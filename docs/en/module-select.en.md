# Module selection assistance

[![中文](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](../zh/module-select.zh.md)

The `module-select` command analyzes one explicitly pinned Go Module and reports dependency-module candidates with Go-selected versions and package-import evidence. It is advisory: it does not choose a final source list, generate a spec, or update an existing spec.

## Usage

```bash
go2spec module-select [--package import/path]... [--format text|json] module@canonical-version-or-full-commit-sha
```

Examples:

```bash
# Analyze packages in the pinned module; the default scope is ./...
go2spec module-select --format text github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1

# Restrict analysis to exact package import paths within the pinned module
go2spec module-select \
  --package github.com/Azure/azure-sdk-for-go/sdk/azidentity \
  --format json \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

A project pin must be a canonical Go Module version or a full 40-character commit SHA. Floating names such as `latest`, branches, unversioned tags, short hashes and non-canonical versions are rejected. A commit SHA must resolve to a pseudo-version and Go must provide a matching full VCS origin hash; otherwise analysis fails rather than treating the pseudo-version's abbreviated revision as proof of the full hash. The report keeps both the requested pin and the resolved version.

`--package` can be repeated. Each value must be an exact import path within the project Module; patterns, repository directories and paths outside the root are rejected. Without this flag, the command analyzes `./...` in the pinned Module.

## Analysis and result semantics

- The command downloads the requested Module using Go tooling into a private temporary module cache, with `GOWORK=off` and `GOTOOLCHAIN=local`. Configured `GOPROXY` and private-module settings are preserved. It validates the module identity, `.info` and `.mod` metadata, ZIP contents, and the `go.mod` extracted from the ZIP. It does not execute dependency code.
- Root `go.mod` files with `replace` or `exclude` directives are rejected for this initial implementation. Those directives can alter source ownership or version semantics and require manual handling.
- Go's module graph (`go list -m -json all`) supplies MVS-selected versions. `go list -deps -test -json` supplies actual imports, test imports and the Go toolchain's package-to-Module attribution. The project is analyzed for both `linux/amd64` and `linux/riscv64` using baseline target features.
- The root `go.mod` requirement and direct/indirect status are shown separately from the graph-selected version. A selected version may be higher than the declared minimum; the tool reports both and never changes the pinned project version.
- Candidates are labeled `production-import`, `test-import`, `transitive`, `declared-unused`, or `graph-only`. Exact import paths and architecture-specific evidence are listed. A module is not inferred from a string-prefix match; ownership comes from Go package metadata.
- The default scope is the pinned Module ZIP and its `./...` packages. The command does not clone or walk a whole Git repository to discover every nested Module. Unreferenced nested Modules are not selected; a nested Module that appears as a dependency in Go's module graph is reported as a dependency candidate, not automatically bundled.

`graph-only` means the Module is present in the resolved graph but no actual package import from the selected project/dependency package set was observed. It is not proof that the package is used. A `declared-unused` item is present in the root `go.mod` but has no observed package-import evidence. Test-only imports can be relevant to `BuildRequires` without becoming runtime dependencies.

The command emits no report until module download, graph resolution and both architecture analyses complete. Failures return nonzero without partial JSON output. Go may update `go.mod`/`go.sum` only in the disposable extracted copy while resolving the graph; the caller's files are not modified. The temporary source and cache are removed after the command.

The result is an explainable candidate list, **not** a final packaging decision, API compatibility proof, target-repository query, or complete dependency solver. Maintainers choose the final set and pass exact `module@version` values to the existing `pack -combine` command.

## Public example: Azure SDK identity Module

Analyze the public `azidentity` Module at a fixed release:

```bash
go2spec module-select --format text \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

The 2026-11-17 run reported these same-import-root candidates:

| Candidate Module | Selected version | Root `go.mod` requirement | Observed use |
|---|---:|---|---|
| `.../sdk/azcore` | `v1.17.0` | direct `v1.17.0` | production and test imports on both architectures |
| `.../sdk/internal` | `v1.10.0` | direct `v1.10.0` | production and test imports on both architectures |
| `.../sdk/azidentity/cache` | `v0.3.1` | direct `v0.3.1` | test-only import on both architectures |

The analyzed project itself is `.../sdk/azidentity@v1.8.1`. The maintainer explicitly chose that root Module plus the three candidates above; other graph entries such as Microsoft Authentication Library and `golang.org/x/*` were left as external RPM dependency candidates, not bundled Sources.

The reviewed selection was passed explicitly to `pack -combine` in a fresh temporary directory:

```bash
out=$(mktemp -d /tmp/go2spec-module-select-pack.XXXXXX)
cd "$out"
go2spec pack \
  -combine github.com/Azure/azure-sdk-for-go/sdk \
  -url https://github.com/Azure/azure-sdk-for-go \
  -package_name go-azure-sdk-auth-selection-demo \
  -package_version 20261117 \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1 \
  github.com/Azure/azure-sdk-for-go/sdk/azcore@v1.17.0 \
  github.com/Azure/azure-sdk-for-go/sdk/internal@v1.10.0 \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache@v0.3.1
```

The generated spec excerpt showed the independent module-to-RPM version mapping (the aggregate RPM `Version` is deliberately different):

```spec
Version: 20261117
%global go_submodules %{expand:
github.com/Azure/azure-sdk-for-go/sdk/azcore 1.17.0
github.com/Azure/azure-sdk-for-go/sdk/azidentity 1.8.1
github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache 0.3.1
github.com/Azure/azure-sdk-for-go/sdk/internal 1.10.0
}
Source0: https://proxy.golang.org/github.com/!azure/azure-sdk-for-go/sdk/azcore/@v/v1.17.0.zip#/module-0-v1.17.0.zip
#!RemoteAsset: sha256:c95138b1a47c41965adc4d70ef708142902bd7867918080d458cc905a8c75dff
Source1: https://proxy.golang.org/github.com/!azure/azure-sdk-for-go/sdk/azidentity/@v/v1.8.1.zip#/module-1-v1.8.1.zip
#!RemoteAsset: sha256:526d50227dc8b764883980827fdd9192adc18f154033c5e58cc331f1dc1f87f5
Source2: https://proxy.golang.org/github.com/!azure/azure-sdk-for-go/sdk/azidentity/cache/@v/v0.3.1.zip#/module-2-v0.3.1.zip
#!RemoteAsset: sha256:77d6b79af70fdd3bc6d2f5cfe7c5083bb0b411daac190386fdbb175241bdede7
Source3: https://proxy.golang.org/github.com/!azure/azure-sdk-for-go/sdk/internal/@v/v1.10.0.zip#/module-3-v1.10.0.zip
#!RemoteAsset: sha256:0a9b4fe658e348b9a6535b1ae0698fa5cf5ba9ba3710530edbdc763eeffe2d24
```

The checked spec was 6,485 bytes and had SHA256 `ce0eafd18fca6739394b15b5c6c9180bb3112e5aa5567b7662bb892106140d37`.

This example validates candidate analysis and spec generation only. It does not claim an RPM build, OBS source-service acceptance, or API compatibility; the generated spec still needs maintainer review and a real target-environment build.

## Existing-spec updates are out of scope

This phase implements only Module-selection assistance. It does **not** add, remove or update Sources in an existing combined spec; rewrite hashes, versions, license, dependencies or install steps; or provide a dry-run spec rewrite. The legacy updater's refusal to modify specs marked with `%global go2spec_combine 1` remains unchanged. See the [pack guide](pack.en.md#combine-independent-module-releases-into-one-rpm) for the existing explicit-source workflow.
