# Module 筛选辅助

[![English](https://img.shields.io/badge/docs-English-blue)](../en/module-select.en.md)

`module-select` 分析一个明确固定版本的 Go Module，报告候选依赖 Module、Go 选定版本和 package import 依据。它只提供辅助信息，不决定最终打包清单、不生成 spec，也不更新已有 spec。

## 用法

```bash
go2spec module-select [--package import/path]... [--format text|json] module@canonical-version-or-full-commit-sha
```

示例：

```bash
# 默认分析固定 Module 中的 ./...
go2spec module-select --format text github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1

# 限定为固定 Module 中的精确 package import path
go2spec module-select \
  --package github.com/Azure/azure-sdk-for-go/sdk/azidentity \
  --format json \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

项目 pin 必须是 canonical Go Module 版本或完整的 40 位 commit SHA。拒绝 `latest`、branch、未固定 tag、短 hash 和非 canonical 版本。commit SHA 必须解析成 pseudo-version，并且 Go 工具必须提供与输入完整 commit hash 相符的 VCS origin hash；否则命令失败，不会把 pseudo-version 中的短 revision 当作完整 hash 的证明。报告同时保留请求的 pin 和解析后的版本。

`--package` 可重复指定，每项必须是项目 Module 内的精确 import path；拒绝 pattern、仓库目录和 Module 外部路径。不传时，分析固定 Module 中的 `./...`。

## 分析方式与结果含义

- 使用 Go 工具将所选版本下载到私有临时 module cache，设置 `GOWORK=off`、`GOTOOLCHAIN=local`，并保留用户的 `GOPROXY` 和私有 Module 配置。验证 Module 身份、`.info`、`.mod`、ZIP 内容和 ZIP 中的 `go.mod`；不执行依赖代码。
- 根 `go.mod` 含 `replace` 或 `exclude` 时拒绝分析。这些指令可能改变源码归属或版本语义，首版要求人工处理。
- 使用 Go 官方 module graph（`go list -m -json all`）读取 MVS 选定版本，并使用 `go list -deps -test -json` 获取实际 imports、测试 imports 和 Go 工具提供的 package-to-Module 归属。分别分析 `linux/amd64` 和 `linux/riscv64`，使用与合并包依赖扫描一致的基线目标特性。
- 根 `go.mod` 的版本要求/direct 或 indirect 属性与 graph 选定版本分开报告。Go 选定版本可能高于声明的最低要求；工具同时展示两者，不改写项目 pin。
- 候选标记包括 `production-import`、`test-import`、`transitive`、`declared-unused` 和 `graph-only`，并列出确切 import path 及各架构证据。Module 归属来自 Go package metadata，不通过字符串前缀推断。
- 默认范围是所选 Module ZIP 的 `./...` package；不会 clone 或遍历整个 Git 仓库来发现所有 nested Module。未被引用的 nested Module 不会加入候选。如果某 nested Module 出现在 Go 依赖图中，它会作为依赖候选报告，但不会自动捆绑。

`graph-only` 表示 Module 出现在解析图中，但在所选项目/依赖 package 集合中没有观察到 package import；这不代表项目实际使用了它。`declared-unused` 表示根 `go.mod` 明确声明该 Module，但没有实际 import 证据。测试专用 import 可能是 `BuildRequires` 候选，但不一定是运行时依赖。

只有下载、graph 解析和两个架构的分析都成功后才输出报告；失败返回非零，不输出部分 JSON。Go 为了解析依赖，可能只修改私有临时源码副本中的 `go.mod`/`go.sum`；不会修改调用方文件。临时源码和 cache 在命令结束后清理。

结果是可解释的候选清单，**不是**最终打包决策、API 兼容性证明、目标仓库查询或完整依赖求解。维护者需要审阅结果，并把明确选择的 `module@version` 传给现有 `pack -combine`。

## 真实公开示例：Azure SDK identity Module

固定 Azure SDK `azidentity` Module 版本并分析：

```bash
go2spec module-select --format text \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

2026-11-17 的运行在共同 import root `github.com/Azure/azure-sdk-for-go/sdk` 下发现以下候选：

| 候选 Module | Go 选定版本 | 根 `go.mod` 要求 | 使用证据 |
|---|---:|---|---|
| `.../sdk/azcore` | `v1.17.0` | direct `v1.17.0` | amd64、riscv64 上均有生产和测试 imports |
| `.../sdk/internal` | `v1.10.0` | direct `v1.10.0` | amd64、riscv64 上均有生产和测试 imports |
| `.../sdk/azidentity/cache` | `v0.3.1` | direct `v0.3.1` | amd64、riscv64 上均为测试 import |

项目根 Module 是明确指定的 `.../sdk/azidentity@v1.8.1`。示例中由维护者选择根 Module 和上面三个同 root 候选；Microsoft Authentication Library、`golang.org/x/*` 等其他 graph 项未加入 Sources，而作为外部 RPM 依赖候选保留。

之后在全新的临时目录中，将人工选择的确切版本传给现有 `pack -combine`：

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

生成 spec 的版本映射示例如下（聚合 RPM `Version` 与 Module 版本明确分开）：

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

实际生成的目录只有一个 spec，文件大小 6,485 bytes，SHA256 为 `ce0eafd18fca6739394b15b5c6c9180bb3112e5aa5567b7662bb892106140d37`。

此示例只验证候选分析和 spec 生成，没有进行 RPM 构建、OBS source-service 验收或 API 兼容性验证。生成的 spec 仍需维护者 review，并在目标 openRuyi 环境构建。

## 已有 spec 更新不在本阶段

本阶段只实现 Module 筛选辅助。**不实现任务 2**：不会给已有聚合 spec 增删 Module、更新单个 Source/checksum/版本映射/许可证/依赖/安装步骤，也不提供 spec dry-run rewrite。`%global go2spec_combine 1` 的旧 updater 保护保持不变。现有显式 Source 工作流见 [pack 文档](pack.zh.md#多个-module-分别选版本合成一个-rpm)。
