# `repo-query` command

`repo-query` answers a specific question: **Does an RPM in the specified distribution, repository and architecture provide this Go package import path?** A Provider is the RPM package that provides the requested Go package capability.

## The simplest query

Specify the distribution, repository URL, architecture and the full Go package capability to check:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  'go(aead.dev/mem)'
```

`go(aead.dev/mem)` is a capability. It represents a Go package import path provided by an RPM.

You can query several capabilities at once:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --format text \
  'go(aead.dev/mem)' \
  'go(golang.org/x/sys/unix)'
```

`--repository` can be an HTTPS repository root URL. You can also specify a repository name and pass its URL separately:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository openruyi-x86_64 \
  --repository-url https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  'go(aead.dev/mem)'
```

`--distro`, `--repository` and `--arch` must all be specified. The command does not use the development machine's rpmdb as a substitute for the target repository.

## Check dependencies from a generated spec

`--spec` reads the literal Go `BuildRequires` (packages needed to build) and `Requires` (packages needed after installation) from the spec, then queries them for the architecture you specify:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --spec go-example-sdk/go-example-sdk.spec \
  --format text
```

You can also provide an explicit RPM version requirement:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --require 'go(aead.dev/mem)>=0:0.2.0' \
  --format text
```

Here `0:0.2.0` is an RPM EVR: `0` is the Epoch and `0.2.0` is the Version. The tool compares RPM Provide versions only when you explicitly provide `--require`. A Go Module version cannot be used directly as an RPM version.

## Read the results

Common statuses are:

| Status | Meaning for users |
|---|---|
| `satisfied` | An exact Provider was found and meets the RPM version requirement you specified. |
| `present-no-version-requirement` | A Provider was found, but you did not request an RPM version comparison; this only means “present.” |
| `version-insufficient` | There is one Provider, but it does not meet the explicit RPM version requirement. |
| `missing` | No exact capability was found among packages for the selected architecture or `noarch`. |
| `multiple-providers` | Multiple Providers were found. The tool does not choose one; JSON `provider_checks` lists the version result for each. |
| `unknown` | The tool cannot safely decide—for example, version data is incomplete, the RPM comparator is unavailable, or a spec condition/macro cannot be parsed. Check the report's `reason`. |

Example text output:

```text
Repository: openruyi/x86_64 (openruyi)
Base URL: https://repo.build.openruyi.cn/openruyi/x86_64/
Architecture: x86_64

[present-no-version-requirement] explicit: go(aead.dev/mem)
  reason: an exact provider exists, but no RPM EVR requirement was supplied
  provider: go-aead-dev-mem.noarch EVR=0.2.0-3.28.or provide=0.2.0 ...
```

This output is illustrative; the actual Provider and version depend on the repository snapshot you query. **`present-no-version-requirement` does not confirm version compatibility.** `missing` means only that no matching capability was found in the selected repository snapshot and architecture; it does not mean the upstream Go Module does not exist.

## Optional: verify the repository signature

If the distribution signs its repository metadata, provide a local keyring that you already trust and require signature verification:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --gpg-keyring /usr/share/pki/rpm-gpg/RPM-GPG-KEY-openruyi-obs \
  --require-signature \
  'go(aead.dev/mem)'
```

The keyring should come from a distribution source you trust; go2spec does not download or automatically trust remote keys. Without `--require-signature`, the result says `signature-not-requested`; you must not treat that as proof that the repository origin was authenticated.

## Output format and reproducibility

- The default `--format text` is intended for people; use `--format json` for scripts.
- The report records the distribution, repository URL, architecture, repomd revision/hash, primary metadata checksums/sizes and signature state.
- JSON dependencies and Providers have stable ordering, so results for the same inputs can be compared.

## Optional: record the source Go Module version

`--module-require` associates the source Go Module version with a package query. It does not convert the Go version to an RPM version. Use it together with a capability or version query:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --require 'go(example.org/sdk/sub)' \
  --module-require example.org/sdk@v1.2.3 \
  --format json
```

If you specify different versions for the same Module path, the tool reports a conflict instead of choosing one based on argument order.

## How `--spec` is parsed

For `--spec`, the command reads literal `BuildRequires` and `Requires` for the specified `--arch`. Simple `%ifarch` and `%ifnarch` conditions—including comma- or whitespace-separated architecture lists—and `%else` branches are evaluated for the target architecture.

The tool does not execute RPM macros or fully expand a spec. Macro-based dependencies and conditions such as ordinary `%if` that cannot be evaluated safely are reported as `unknown` and cause a non-zero exit.

The command reads the repository's RPM-MD `primary` package listing; it does not download individual RPMs. It looks up full package capabilities and considers only binary packages for the target architecture and `noarch`. Primary package metadata is supported in XML and gzip formats. A compressed checksum and size are required; if the repository provides `open-checksum` or `open-size`, they are verified. Decompression remains size-limited if either field is absent.
