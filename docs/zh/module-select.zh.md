# Module 筛选辅助

[![English](https://img.shields.io/badge/docs-English-blue)](../en/module-select.en.md)

在 openRuyi 目前的 go rpm 包打包体系中，采取的策略是**各个 module 使用自己选定的正式版本和对应源码，最后组合到一个 RPM。**在打包多 module 包过程中，需要进行大量的 module 选择。

`module-select` 用于**根据指定 Go 项目辅助选择 Module**，报告**候选依赖 Module**、版本和依据。它只提供辅助信息，不决定最终打包清单、不生成 spec，也不更新已有 spec。

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

项目 pin 必须是 canonical Go Module 版本或完整的 40 位 commit SHA。拒绝 `latest`、branch、未固定 tag、短 hash 和非 canonical 版本。

`--package` 可重复指定，每项必须是项目 Module 内的精确 import path；拒绝 pattern、仓库目录和 Module 外部路径。不传时，分析固定 Module 中的 `./...`。

## 结果含义

每个候选报告包括：

- 根 `go.mod` 中声明的最低版本，以及 `direct`、`indirect` 或未在根文件声明的状态；
- Go Module graph 中 MVS 选定的版本。若高于根 `go.mod` 中的最低版本，会同时显示两者；
- 实际 package import path，按目标架构分别列出；
- 用途标签：`production-import`、`test-import`、`transitive`、`declared-unused` 或 `graph-only`。

特别是，`graph-only` 只说明 Module 出现在解析图里，没有观察到实际 package import；`declared-unused` 表示根 `go.mod` 有声明但扫描中没有实际 import 证据。测试专用 imports 可以是构建依赖线索，但不能据此断言它们是运行时依赖。Go 版本与 RPM EVR、Module 版本与 API 兼容性之间均不作自动等价推断。

| 标签                                       | 含义                                           |
| ------------------------------------------ | ---------------------------------------------- |
| `production-import`                        | 生产代码直接导入                               |
| `test-import`                              | 测试代码直接导入                               |
| `transitive`                               | 被其他依赖间接导入                             |
| `graph-only`                               | 出现在 Go module graph，但没有被扫描到实际导入 |
| `declared-unused`                          | 在 go.mod 声明了，但没有发现实际使用           |
| `production-import,test-import`            | 生产和测试代码都使用                           |
| `production-import,test-import,transitive` | 同时具有多种关系                               |

`module-select` 只列出候选和理由。维护者审阅报告后，仍需显式选择 `module@version` 作为 combine 输入。‘

若根 `go.mod` 含 `replace` 或 `exclude`，该模式明确拒绝，不猜测被替换源码或排除规则的效果。Go package 分析不完整、metadata 不合法、来源验证失败或架构分析失败时，命令以非零状态退出，

## 示例

固定 Azure SDK `azidentity` Module 版本并分析：

```bash
go2spec module-select --format text \
  github.com/Azure/azure-sdk-for-go/sdk/azidentity@v1.8.1
```

JSON 校验结果中，根项目 pin 与 Go 解析版本均为 `v1.8.1`，共报告 **36 个 module graph 候选**。与打包目标同属 `github.com/Azure/azure-sdk-for-go/sdk` 的实际候选包括：

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

此示例只验证候选分析和 spec 生成，没有进行 RPM 构建、OBS source-service 验收或 API 兼容性验证。生成的 spec 仍需维护者 review，并在目标环境构建。



现有显式 Source 工作流见 [pack 文档](pack.zh.md#多个-module-分别选版本合成一个-rpm)。
