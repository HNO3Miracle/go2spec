# Module selection assistance

[![中文](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](../zh/module-select.zh.md)

In openRuyi's current Go RPM packaging workflow, each Module uses its own selected release and corresponding source, and the Modules are then combined into one RPM. Selecting Modules for a multi-Module package can require substantial analysis.

`module-select` assists with choosing Modules for a specified Go project. It reports candidate dependency Modules, versions and reasons. It is advisory only: it does not decide the final package list, generate a spec, or update an existing spec.

## Usage

```bash
go2spec module-select [--package import/path]... [--format text|json] module@canonical-version-or-full-commit-sha
```

Example:

```bash
# Analyze ./... in the pinned Module by default
go2spec module-select --format text github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1

# Restrict analysis to an exact package import path within the Module
go2spec module-select \
  --package github.com/Azure/azure-sdk-for-go/sdk/azidentity \
  --format json \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

The project pin must be a canonical Go Module version or a full 40-character commit SHA. `latest`, branches, unpinned tags, short hashes and non-canonical versions are rejected.

`--package` can be repeated. Each value must be an exact package import path within the project Module; patterns, repository directories and paths outside the Module are rejected. Without this flag, the command analyzes `./...` in the pinned Module.

## Understanding the results

Each candidate includes:

- the minimum version declared in the root `go.mod`, and whether it is direct, indirect, or not declared in that file;
- the version selected by Go's Module graph using Minimal Version Selection (MVS). If it is higher than the root `go.mod` minimum, both versions are shown;
- actual package import paths, listed by target architecture;
- a usage label: `production-import`, `test-import`, `transitive`, `declared-unused`, or `graph-only`.

In particular, `graph-only` means only that a Module appears in the graph; no actual package import was observed. `declared-unused` means the root `go.mod` lists the Module but no package import was found. Test-only imports may be build-dependency candidates, but do not establish that a Module is needed at runtime. The tool does not equate Go versions with RPM EVRs or infer API compatibility.

| Label | Meaning |
|---|---|
| `production-import` | Imported directly by production code. |
| `test-import` | Imported directly by test code. |
| `transitive` | Imported indirectly by another dependency. |
| `graph-only` | Present in Go's Module graph, but no actual import was observed. |
| `declared-unused` | Declared in `go.mod`, but no actual use was found. |
| `production-import,test-import` | Used by both production and test code. |
| `production-import,test-import,transitive` | Has multiple usage relationships. |

`module-select` only lists candidates and reasons. Maintainers review the report and explicitly choose the `module@version` values to pass to `pack -combine`.

If the root `go.mod` contains `replace` or `exclude`, the command refuses to guess the effect of replaced sources or exclusions. Incomplete Go package analysis, invalid metadata, source-verification failures or architecture-analysis failures produce a non-zero exit.

## Example

Pin the Azure SDK `azidentity` Module and analyze it:

```bash
go2spec module-select --format text \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

The JSON-validated run resolved the project pin to `v1.8.1` and reported **36 Module graph candidates**. These candidates share the `github.com/Azure/azure-sdk-for-go/sdk` import root:

| Candidate Module | Go-selected version | Root `go.mod` requirement | Usage evidence |
|---|---:|---|---|
| `.../sdk/azcore` | `v1.17.0` | direct `v1.17.0` | Production and test imports on amd64 and riscv64 |
| `.../sdk/internal` | `v1.10.0` | direct `v1.10.0` | Production and test imports on amd64 and riscv64 |
| `.../sdk/azidentity/cache` | `v0.3.1` | direct `v0.3.1` | Test imports on amd64 and riscv64 |

The root Module is the explicitly specified `.../sdk/azidentity@v1.8.1`. In this example, the maintainer chose the root Module and the three candidates above. Other graph entries, such as Microsoft Authentication Library and `golang.org/x/*`, were not added as Sources; they remain external RPM dependency candidates.

The selected exact versions were then passed to the existing `pack -combine` command in a new temporary directory:

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

The generated Module version mapping is separate from the aggregate RPM `Version`:

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

The generated directory contains one spec. The spec is 6,485 bytes and has SHA256 `ce0eafd18fca6739394b15b5c6c9180bb3112e5aa5567b7662bb892106140d37`.

This example verifies candidate analysis and spec generation only. It does not include an RPM build, OBS source-service acceptance, or API-compatibility verification. The spec still needs maintainer review and a build in the target environment.

The existing explicit-Source workflow is described in the [pack guide](pack.en.md#combine-independent-module-releases-into-one-rpm).
