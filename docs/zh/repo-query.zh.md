# repo-query 命令

`repo-query` 回答一个具体问题：**指定发行版、仓库和架构里，有没有 RPM 包提供这个 Go package import path？**这里的 Provider 指“提供该 Go package capability 的 RPM 包”。

## 最简单的查询

先给出发行版、仓库 URL、架构，以及要检查的完整 Go package capability：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  'go(aead.dev/mem)'
```

`go(aead.dev/mem)` 是一个 capability。它表示 RPM 提供的 Go package import path。

命令也可以一次检查多个 capability：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --format text \
  'go(aead.dev/mem)' \
  'go(golang.org/x/sys/unix)'
```

`--repository` 可以直接写 HTTPS 仓库根 URL；也可以写仓库名称，并另传 `--repository-url`：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository openruyi-x86_64 \
  --repository-url https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  'go(aead.dev/mem)'
```

`--distro`、`--repository` 和 `--arch` 都必须明确指定，命令不会用开发机上的 rpmdb 代替目标仓库。

## 检查生成 spec 的依赖

`--spec` 会读取 spec 里写明的 Go `BuildRequires`（构建时需要的包）和 `Requires`（安装/运行时需要的包），并按你指定的 `--arch` 查询。

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --spec go-example-sdk/go-example-sdk.spec \
  --format text
```

也可以同时提供显式 RPM 版本要求：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --require 'go(aead.dev/mem)>=0:0.2.0' \
  --format text
```

这里 `0:0.2.0` 是 RPM EVR：`0` 是 Epoch，`0.2.0` 是 Version。只有明确提供 `--require` 时，工具才会比较 RPM 的 Provide EVR。Go Module 版本不能直接当成 RPM 版本比较。

## 如何看结果

常见状态如下：

| 状态 | 对使用者的含义 |
|---|---|
| `satisfied` | 找到一个精确 Provider，且满足你明确给出的 RPM 版本要求。 |
| `present-no-version-requirement` | 找到了一个 Provider；你没有要求比较 RPM 版本，所以这只说明“存在”。 |
| `version-insufficient` | 只有一个 Provider，但它不满足明确的 RPM 版本要求。 |
| `missing` | 目标架构及 `noarch` 包中都没有这个精确 capability。 |
| `multiple-providers` | 找到多个 Provider，程序不会擅自替你挑一个；JSON 中的 `provider_checks` 会列出各自版本判断。 |
| `unknown` | 目前无法安全判断，例如版本信息不完整、RPM 比较器不可用，或 spec 条件/宏不能解析。请查看报告中的 `reason`。 |

文本结果示意：

```text
Repository: openruyi/x86_64 (openruyi)
Base URL: https://repo.build.openruyi.cn/openruyi/x86_64/
Architecture: x86_64

[present-no-version-requirement] explicit: go(aead.dev/mem)
  reason: an exact provider exists, but no RPM EVR requirement was supplied
  provider: go-aead-dev-mem.noarch EVR=0.2.0-3.28.or provide=0.2.0 ...
```

上面仅展示格式；具体 Provider 和版本以你查询到的仓库快照为准。**`present-no-version-requirement` 不等于版本兼容已经确认。**`missing` 也只表示在所选仓库快照和架构中没有匹配的 capability，不表示上游没有这个 Go Module。

## 选择仓库签名验证（可选）

若发行版提供仓库签名，可传入维护者已经信任的本地 keyring，并要求验证：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --gpg-keyring /usr/share/pki/rpm-gpg/RPM-GPG-KEY-openruyi-obs \
  --require-signature \
  'go(aead.dev/mem)'
```

keyring 应来自你信任的发行版来源；go2spec 不会下载或自动信任远程密钥。不传 `--require-signature` 时，结果会写明 `signature-not-requested`，不能据此声称仓库来源已认证。

## 结果格式与可复现信息

- 默认 `--format text`，适合人工阅读；传 `--format json` 可供脚本处理。
- 报告会记录发行版、仓库 URL、架构、repomd revision/hash、primary metadata checksum/大小和签名状态。
- JSON 中依赖和 Provider 会稳定排序；因此可以比较不同输入顺序下的同一查询结果。

## 可选：记录 Go Module 来源版本

`--module-require` 用于把 Go Module 来源版本作为附加信息关联到 package 查询，不会把 Go 版本换算成 RPM 版本。实际使用时和 capability/version 查询一起传入：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --require 'go(example.org/sdk/sub)' \
  --module-require example.org/sdk@v1.2.3 \
  --format json
```

同一个 Module path 若指定多个不同版本，工具会报告冲突，不按参数顺序任选一个。

## `--spec` 的解析范围

`--spec` 按指定的 `--arch` 处理字面的 `BuildRequires` 和 `Requires`。简单的 `%ifarch`、`%ifnarch`、逗号或空格分隔的架构列表及 `%else` 会针对目标架构判断。

工具不会执行 RPM 宏或完整展开 spec。宏形式依赖及普通 `%if` 等无法安全判断的条件会报告为 `unknown`，并以非零状态退出。



`repo-query` 读取仓库的 RPM-MD `primary` 包清单（不是逐个下载 RPM），只查询完整 package capability，并只考虑目标架构和 `noarch` 二进制包。当前支持 XML 和 gzip 格式的包清单，其他格式会明确报错。压缩 checksum/大小为必需项；仓库若提供 `open-checksum`、`open-size` 就会校验，缺少时仍限制解压大小并在报告中说明。
