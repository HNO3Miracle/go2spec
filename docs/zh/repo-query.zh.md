# repo-query 命令

`repo-query` 用于诊断显式指定的 RPM 仓库中是否提供 Go RPM capability。它不读取开发机 RPM 数据库，不修改 spec，不选择 Module，也不求解完整依赖图。

## 用法

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --require 'go(aead.dev/mem)>=0:0.2.0' \
  --format text
```

`--repository` 可以填写仓库标识或 HTTPS 仓库根 URL。若填写标识，则另外传入 `--repository-url`。`--distro`、`--arch` 和仓库必须明确指定；命令不会回退到开发机 rpmdb。

检查生成 spec 中的 Go 依赖：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --spec go-example-sdk/go-example-sdk.spec \
  --format json
```

额外输入可重复指定：

- 位置参数 `go(import/path)`：查询精确 capability。
- `--require 'go(import/path)>=epoch:version-release'`：显式提供 RPM EVR 版本要求。
- `--module-require module/path@canonical-version`：记录 Go Module 来源身份。Go Module 版本**不会**自动与 RPM EVR 比较；同一 Module path 指定多个不同版本时会明确报冲突，不会按输入顺序任选一个。
- `--format text|json`：选择便于阅读或稳定的机器可读报告。

## spec 依赖解析边界

`--spec` 按目标 `--arch` 解析字面的 `BuildRequires` 和 `Requires`。简单的 `%ifarch`、`%ifnarch` 及其 `%else` 分支会按目标架构筛选；程序不会使用开发机架构。此解析器不会执行 RPM 宏：宏展开的依赖、普通 `%if`/`%elif` 等无法安全判定的条件会作为 `unknown` 诊断保留，命令返回非零状态，不会静默忽略或套用到错误架构。需要完整 RPM 宏展开的 spec，应先在目标 RPM 环境中解析，再使用明确的字面依赖输入。

RPM EVR 输入会校验十进制 Epoch 及受支持的 Version/Release 字符；格式无效的要求会报错。对仓库提供的 EVR 也使用同样的安全校验，无法比较时不会据此判为满足。

## 仓库信任与 metadata

命令下载 `repomd.xml`，要求 primary 的压缩 checksum 和压缩大小完整，并校验其声明值。`open-checksum` 和 `open-size` 是可选字段：仓库提供哪一项就验证哪一项；缺失时仍限制解压后的最大体积，并在报告中标记 open checksum 为 `not-provided`。报告使用仓库声明的 checksum 算法，并显示压缩/解压大小。目前支持 XML 和 gzip 压缩的 XML primary 索引。压缩格式不支持、XML 损坏、记录缺失、路径不安全、checksum 不符、下载截断或数据过大，都会明确报错，不会伪装成空仓库。

若发行版提供 repomd 签名，使用维护者已经信任的本地 keyring：

```bash
go2spec repo-query \
  --distro openruyi \
  --repository https://repo.build.openruyi.cn/openruyi/x86_64/ \
  --arch x86_64 \
  --gpg-keyring /usr/share/pki/rpm-gpg/RPM-GPG-KEY-openruyi-obs \
  --require-signature \
  --require 'go(aead.dev/mem)>=0:0.2.0'
```

keyring 必须由维护者事先信任，并可供 `gpgv` 使用；go2spec 不会自动下载或信任远程密钥。不传 `--require-signature` 时，报告会标为 `signature-not-requested`，而不会声称仓库已认证。报告包含发行版、仓库、架构、repomd revision/hash、primary checksum/size 和签名状态，便于复现目标快照。

## 结果状态

每个精确 `go(import/path)` 依赖会得到以下状态之一：

- `satisfied`：存在一个精确 provider，且满足显式 RPM EVR 要求；
- `present-no-version-requirement`：存在一个精确 provider，但调用方没有提供 RPM 版本要求；
- `version-insufficient`：只有一个精确 provider，但它不满足显式 RPM EVR 要求；多个 provider 时不以此状态掩盖 provider 歧义；
- `missing`：目标架构及 `noarch` 中均没有精确 provider；
- `multiple-providers`：存在多个精确 provider；无论其版本比较结果如何，程序都不替维护者选择，并在 `provider_checks` 中逐个列出版本判断；
- `unknown`：provider 没有可比较的 EVR、RPM 比较器不可用、输入格式/条件/宏无法安全解析，或版本语义无法比较。

Go Module 版本只作为来源信息，不会把 provider 判为 `satisfied`；Go Module 与 RPM 版本处于不同语义空间。查询失败返回非零状态，不会被转换成 `missing` 或 `satisfied`。

匹配使用完整 package capability，不用 module 根路径前缀推断。例如，`go(example.org/sdk/submarine)` 不满足 `go(example.org/sdk/sub)`。只考虑与目标架构相同或 `noarch` 的二进制包；忽略源码包及其他架构。

## 可复现性与范围

JSON 报告中的依赖按 capability、范围、RPM 要求和 Module 身份稳定排序；Provider 也按完整记录排序。显式 RPM 版本要求会附带逐 Provider 的 `provider_checks`；`go_module` 使用 `path`、`version` 字段。该命令仅提供诊断：不写 spec、不创建软件包、不选择更新版本，也不证明 API 兼容性。固定 metadata fixture 覆盖损坏数据、checksum/size 错误、架构过滤、重复 provider、依赖缺失和版本不可比较；真实 openRuyi 仓库查询用于补充验证仓库事实，不代表完整依赖求解。
