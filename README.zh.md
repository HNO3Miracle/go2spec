# go2spec

[![状态](https://img.shields.io/badge/status-active_development-orange)](#项目概览)
[![许可证](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![English](https://img.shields.io/badge/docs-English-blue)](README.md)
[![中文文档](https://img.shields.io/badge/docs-%E4%B8%AD%E6%96%87-red)](README.zh.md)

## 项目概览

go2spec 是面向 openRuyi RPM 打包流程的 Go 包辅助工具。它从 Go 生态获取上游元数据，生成源码归档，并输出 RPM spec。

## 安装指南

### 从源码构建

```bash
git clone <go2spec-repo-url>
cd go2spec
go build -o go2spec .
```

运行：

```bash
./go2spec help
```

## 快速开始

### 生成 spec

```bash
go2spec pack golang.org/x/net
```

该命令会在当前目录生成类似下面的文件结构：

```text
go-golang-x-net/
go-golang-x-net/go-golang-x-net.spec
go-golang-x-net_<version>.orig.tar.<gz|xz>
```

### 将多个 module 的独立版本合成一个 RPM

使用 `pack -combine <import-root> -url <project-homepage> -package_name <name> -package_version <version>`，
为每个位置参数显式指定 `module@version`，生成**一个 library 源码 RPM spec**。spec 中的
每个 `SourceN` 是固定的 Go Module Proxy URL，并带有 `#!RemoteAsset` 校验值；源码由
OBS service 在源码准备阶段下载，不需要把 ZIP 提交到仓库。不加 `-combine` 时，原有多参数分别
打包的语义不变。

具体用法、输出和首版限制见 [pack 文档](docs/zh/pack.zh.md#多个-module-分别选版本合成一个-rpm)。
目标环境需要 `go-rpm-macros >= 0.3`，仍须人工审查与构建验证。旧 updater 会拒绝
这种合并 spec，dry run 也不例外。

### 不写入文件地检查现有 spec 更新

```bash
go2spec update -n /path/to/SPECS
```

`update` 会扫描 `/path/to/SPECS/go-*/*.spec`，然后检查上游最新版本。

### 并发执行更新

```bash
go2spec update -j 4 /path/to/SPECS
```

> 建议始终先执行 dry run，再修改真实 spec 文件。

## 文档导航

| 内容 | English | 中文 |
|---|---|---|
| 主 README | [README.md](README.md) | [README.zh.md](README.zh.md) |
| `pack` 命令 | [docs/en/pack.en.md](docs/en/pack.en.md) | [docs/zh/pack.zh.md](docs/zh/pack.zh.md) |
| `update` 命令 | [docs/en/update.en.md](docs/en/update.en.md) | [docs/zh/update.zh.md](docs/zh/update.zh.md) |

## 贡献

- 阅读 [openRuyi 行为准则](https://openruyi.cn/governance/legal/code-of-conduct) 并同意遵守。
- 阅读 [AI 辅助贡献政策](https://openruyi.cn/governance/policy/ai-contribution-policy)，且此 Pull Request 包含实质性的 AI 辅助内容时需要说明。

## 许可证

go2spec 使用 BSD-3-Clause 许可证，见 [LICENSE](LICENSE)。
