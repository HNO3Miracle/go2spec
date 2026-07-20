# go2spec

[![Status](https://img.shields.io/badge/status-active_development-orange)](#overview)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Language](https://img.shields.io/badge/docs-English-blue)](README.md)
[![中文文档](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](README.zh.md)

## Overview

go2spec is a Go package helper for the openRuyi RPM packaging workflow. It collects upstream metadata from the Go ecosystem, generates source archives, and writes RPM spec files.

## Installation

### Build From Source

```bash
git clone <go2spec-repo-url>
cd go2spec
go build -o go2spec .
```

Run it with:

```bash
./go2spec help
```

## Quick Start

### Generate a spec

```bash
go2spec pack golang.org/x/net
```

The command creates a file layout similar to this in the current directory:

```text
go-golang-x-net/
go-golang-x-net/go-golang-x-net.spec
go-golang-x-net_<version>.orig.tar.<gz|xz>
```

### Check existing spec updates without writing files

```bash
go2spec update -n /path/to/SPECS
```

`update` scans `/path/to/SPECS/go-*/*.spec`, then checks the latest upstream version.

### Run updates concurrently

```bash
go2spec update -j 4 /path/to/SPECS
```

> Always run dry run mode first before modifying real spec files.

## Documentation

| Area | English | Chinese |
|---|---|---|
| Main README | [README.md](README.md) | [README.zh.md](docs/zh/README.zh.md) |
| `pack` command | [docs/en/pack.en.md](docs/en/pack.en.md) | [docs/zh/pack.zh.md](docs/zh/pack.zh.md) |
| `update` command | [docs/en/update.en.md](docs/en/update.en.md) | [docs/zh/update.zh.md](docs/zh/update.zh.md) |

## Contributing

- Read the [openRuyi Code of Conduct](https://openruyi.cn/governance/legal/code-of-conduct) and agree to follow it.
- Read the [AI-assisted Contribution Policy](https://openruyi.cn/governance/policy/ai-contribution-policy), and note that this Pull Request contains substantial AI-assisted content when applicable.

## License

go2spec uses the BSD-3-Clause license. See [LICENSE](LICENSE).
