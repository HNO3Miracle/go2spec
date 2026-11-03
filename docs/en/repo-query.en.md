# repo-query command

`repo-query` diagnoses the Go RPM capabilities available in an explicitly selected RPM repository. It does not use the host RPM database, modify a spec, select modules, or solve the complete dependency graph.

## Usage

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --require 'go(aead.dev/mem)>=0:0.2.0' \
  --format text
```

`--repository` accepts either a repository identity or an HTTPS repository base URL. When it is an identity, pass `--repository-url` separately. `--distro`, `--arch`, and the repository are always explicit; the command never falls back to the development machine's rpmdb.

To inspect dependencies from a generated spec, use `--spec`:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --spec go-example-sdk/go-example-sdk.spec \
  --format json
```

Additional inputs are repeatable:

- `go(import/path)` positional arguments query exact capabilities.
- `--require 'go(import/path)>=epoch:version-release'` supplies an explicit RPM EVR requirement.
- `--module-require module/path@canonical-version` records Go Module provenance. Go Module versions are **not** automatically compared with RPM EVRs; conflicting versions for the same Module path are reported instead of choosing one by input order.
- `--format text|json` selects the stable human-readable or machine-readable report.

## Spec dependency parsing limits

With `--spec`, go2spec reads literal `BuildRequires` and `Requires` for the requested `--arch`. Simple `%ifarch`, `%ifnarch`, and their `%else` branches are evaluated against that target architecture; the host architecture is not substituted. This bounded parser does not execute RPM macros. Macro-expanded dependency values and conditions it cannot safely evaluate, such as ordinary `%if` or `%elif`, are retained as `unknown` diagnostics and cause a non-zero exit rather than being silently ignored or applied to the wrong architecture. Specs requiring full RPM macro expansion should first be evaluated in the target RPM environment, then queried using explicit literal requirements.

RPM EVR inputs are validated for a decimal Epoch and supported Version/Release characters. Malformed requirements are errors. Repository-provided EVRs receive the same validation before comparison; an invalid or incomparable value cannot be reported as satisfied.

## Repository trust and metadata

The command downloads `repomd.xml`. The primary compressed checksum and size are required and verified. `open-checksum` and `open-size` are optional: each is verified when supplied; when absent, decompression remains size-bounded and the report marks the open checksum as `not-provided`. Reports show the declared checksum algorithms and compressed/open sizes. The command accepts XML and gzip-compressed XML primary metadata. Unsupported compression, malformed XML, missing records, unsafe locations, checksum mismatches, truncated responses, and excessive sizes are errors—not an empty repository.

Use a trusted local keyring when the distribution publishes a repomd signature:

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --gpg-keyring /usr/share/pki/rpm-gpg/RPM-GPG-KEY-openruyi-obs \
  --require-signature \
  --require 'go(aead.dev/mem)>=0:0.2.0'
```

The keyring must already be trusted by the maintainer and usable by `gpgv`; go2spec never downloads or trusts a remote key automatically. Without `--require-signature`, the report says `signature-not-requested` rather than claiming repository authenticity. The report includes the distro, repository, architecture, repomd revision/hash, primary checksum/size and signature state so a snapshot can be reproduced.

## Result states

Each exact `go(import/path)` requirement produces one of:

- `satisfied`: an exact provider satisfies the explicit RPM EVR requirement;
- `present-no-version-requirement`: one exact provider exists, but no RPM version requirement was supplied;
- `version-insufficient`: there is one exact provider, but it does not satisfy the explicit RPM EVR requirement; multiple providers are not collapsed into this state;
- `missing`: no exact provider exists for the selected architecture or `noarch`;
- `multiple-providers`: more than one exact provider exists; this status takes precedence regardless of their version comparisons, and `provider_checks` reports each comparison without selecting a provider;
- `unknown`: the provider has no comparable EVR, the RPM comparator is unavailable, the input/condition/macro cannot be safely resolved, or version semantics are incomparable.

A Go Module requirement is reported as provenance and does not turn a provider into `satisfied`; Go Module and RPM version spaces have different semantics. Query failures return a non-zero exit status and are not converted into `missing` or `satisfied`.

The parser matches the full package capability, not a module-root prefix. For example, `go(example.org/sdk/submarine)` does not satisfy `go(example.org/sdk/sub)`. Binary packages matching the target architecture and `noarch` are considered; source packages and other architectures are ignored.

## Reproducibility and scope

The JSON report sorts dependencies by capability, scope, RPM requirement, and Module provenance; providers are sorted by their complete records. Explicit RPM version requirements include per-provider `provider_checks`, and `go_module` uses `path` and `version` keys. The command is diagnostic only: it does not write a spec, create a package, select a newer Module version, or prove API compatibility. Fixed metadata fixtures cover malformed data, checksum/size errors, architecture filtering, duplicate providers, missing providers and incomparable versions. A real openRuyi snapshot is an additional repository-fact check, not a complete dependency-solver result.
