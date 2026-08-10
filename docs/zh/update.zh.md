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
