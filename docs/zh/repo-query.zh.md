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
- `--module-require module/path@canonical-version`：记录 Go Module 来源身份。Go Module 版本**不会**自动与 RPM EVR 比较。
- `--format text|json`：选择便于阅读或稳定的机器可读报告。

## 仓库信任与 metadata

命令下载 `repomd.xml`，校验其中声明的 primary 索引压缩/解压 checksum 和大小，并限制 metadata 下载及解压体积。目前支持 XML 和 gzip 压缩的 XML primary 索引。压缩格式不支持、XML 损坏、记录缺失、路径不安全、checksum 不符、下载截断或数据过大，都会明确报错，不会伪装成空仓库。

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
- `version-insufficient`：存在 provider，但没有一个满足显式 EVR 要求；
- `missing`：目标架构及 `noarch` 中均没有精确 provider；
- `multiple-providers`：存在多个精确 provider，go2spec 不会擅自选择；
- `unknown`：provider 没有可比较的 EVR、RPM 比较器不可用，或输入不足以安全比较。

Go Module 版本只作为来源信息，不会把 provider 判为 `satisfied`；Go Module 与 RPM 版本处于不同语义空间。查询失败返回非零状态，不会被转换成 `missing` 或 `satisfied`。

匹配使用完整 package capability，不用 module 根路径前缀推断。例如，`go(example.org/sdk/submarine)` 不满足 `go(example.org/sdk/sub)`。只考虑与目标架构相同或 `noarch` 的二进制包；忽略源码包及其他架构。

## 可复现性与范围

JSON 报告中的依赖和 provider 按稳定顺序排列。该命令仅提供诊断：不写 spec、不创建软件包、不选择更新版本，也不证明 API 兼容性。固定 metadata fixture 覆盖损坏数据、checksum/size 错误、架构过滤、重复 provider、依赖缺失和版本不可比较；真实 openRuyi 仓库查询用于补充验证仓库事实，不代表完整依赖求解。
