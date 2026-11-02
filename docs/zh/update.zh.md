# update 命令

[![English](https://img.shields.io/badge/docs-English-blue)](../en/update.en.md)
[![pack 命令](https://img.shields.io/badge/docs-pack-blue)](pack.zh.md)

`update` 扫描已有 Golang spec 包，检查上游最新版本，并可更新 spec 版本。

## 用法

```bash
go2spec update [options] <SPECS_DIR>
```

`<SPECS_DIR>` 必须包含带 spec 文件的 `go-*` 子目录。

```text
SPECS_DIR/
go-golang-x-net/
go-golang-x-net/go-golang-x-net.spec
```

## 参数

```text
-n
    dry run，只检测更新，不修改文件

-j <num>
    并发检查数量，默认 3
```

## 不支持聚合 spec

旧 updater 只更新一个 `Version` 和 `Source0`，不能用于独立发布版本的多模块聚合
spec。扫描范围 `go-*/*.spec` 中只要有 spec 包含字面的
`%global go2spec_combine ...` 或 `%define go2spec_combine ...` 定义，命令就会报告
`unsupported`，并在任何网络请求或文件更新前以非零状态返回。`-n`（dry run）也
同样拒绝；即使扫描目录同时包含普通 spec，也会拒绝整个扫描批次。

这里只检查定义是否存在，不求值：值为 `0`、空值或位于条件分支中的定义也会被
拒绝。不执行 RPM 宏、shell、Lua 或条件判断。`# %global go2spec_combine 1` 这类
注释中的示例不是标记。直接调用更新入口也会拒绝带标记的文件；无标记 spec
保留原行为。不要删除标记来绕过保护，聚合感知的 updater 尚未实现。

## 推荐流程

先执行 dry run：

```bash
go2spec update -n /path/to/SPECS
```

确认输出后再执行更新：

```bash
go2spec update -j 4 /path/to/SPECS
```

提交 packaging 变更前必须 review 生成的 diff。
