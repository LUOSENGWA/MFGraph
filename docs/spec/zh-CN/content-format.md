# MFGraph 内容格式（第 1 版）

[English](../content-format.md) | [简体中文](content-format.md)

本文规定 MFGraph 中决定内容 hash 与树 hash 的字节级格式：内容身份、路径文法、manifest 树节点和存储 key。目标是让另一个实现能够独立产生并校验完全相同的 hash。测试向量见 [`testdata/vectors`](../../../testdata/vectors/README.md)。

文中“必须（MUST）/不得（MUST NOT）/应当（SHOULD）/可以（MAY）”按 RFC 2119 理解。

本文中任何会改变 hash 的修改都需要新的格式版本（`"format": 2`）；按第 1 版写入的数据始终有效。

## 1. 内容身份

1. 文件的身份是其**原始字节**的 SHA-256。不做任何规范化：只在换行符或 Unicode 规范化形式上不同的两个文件，身份也不同。
2. 身份与存储、传输方式无关：分块边界、分片上传的 part 大小、存储后端、上传路径都不影响身份。
3. 文本形式：`sha256:` 加 64 位**小写**十六进制摘要，例如 `hello` 为 `sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824`。解析时必须拒绝其他一切写法（大写十六进制、大写前缀、其他算法名、缺少前缀、前后空白、长度不对），保证每个 hash 只有一种表示。
4. 数据库形式：不带前缀的 64 位小写十六进制摘要，另设算法列，取值 `sha256`。

## 2. 路径

路径表示文件相对 workspace 根的位置，由 `/` 分隔的若干段组成。目录只作为文件路径的前缀存在（没有空目录）；符号链接不属于数据模型。

### 2.1 规范化

服务端按以下顺序规范化每个路径，第一个失败的步骤决定错误类别：

1. 输入必须是合法 UTF-8（`invalid_utf8`）且非空（`empty`）。
2. 不得以 `/` 开头（`absolute`），不得包含 `\`（`backslash`）或控制字符 U+0000–U+001F、U+007F（`control_char`）。
3. 转换为 Unicode 规范化形式 C（NFC）。
4. 按 `/` 切段：段不得为空（`a//b`、结尾 `/`：`empty_segment`），不得是 `.` 或 `..`（`dot_segment`）。
5. 每段至多 255 字节（`segment_too_long`），整个路径至多 4096 字节（`path_too_long`），均按 NFC 之后的 UTF-8 字节数计算。

第 3 步的结果就是规范路径。没有其他规则：例如 C1 控制字符（U+0080–U+009F）、双向或零宽字符、全角斜杠 U+FF0F、部分操作系统保留的名字（`CON`、以点或空格结尾的名字）都是允许的。

### 2.2 同级名字

同一目录内：

* 同一个名字不得同时用作文件和目录（`file_dir_conflict`）；
* 同一路径不得出现两次——包括只在 Unicode 规范化形式上不同的两个输入（`duplicate`）；
* 两个不同的名字不得具有相同的**折叠键**（`case_conflict`）。

名字的折叠键 = 对其 NFC 形式做 Unicode 完全大小写折叠（CaseFolding.txt 中状态为 C 和 F 的映射，不含土耳其语映射）。例如：`Plan.dwg` 与 `plan.dwg` 冲突；`straße` 与 `STRASSE` 冲突（都折叠为 `strasse`）；`σ` 与 `ς` 冲突；`İ`（U+0130）与 `i` 不冲突。

### 2.3 分支名

分支名按单个路径段的规则校验（对一个段执行第 1–5 步），因此永远不含 `/`；出现 `/` 时报 `contains_slash`。保存其 NFC 形式。

### 2.4 Unicode 版本

NFC 和大小写折叠依赖 Unicode 字符数据库。参考实现使用 Unicode 17.0.0。对于在较旧 Unicode 版本中尚未分配的字符，不同实现的结果可能不同；以服务端的规范化结果为准。

## 3. 树节点

快照的内容是一棵由不可变节点组成的树。每个节点以其规范字节的 SHA-256 寻址；根节点的 hash 就是快照的 `root_tree_hash`。

### 3.1 规范编码

节点按 RFC 8785（JSON 规范化方案，JCS）序列化，取值只限于对象、数组、字符串和整数：

* 对象的键排序（所有键都是 ASCII，因此就是字节序）；不得有重复键；
* token 之间没有空白；UTF-8，不带 BOM；末尾没有换行；
* 字符串只转义以下字符：`"` 写作 `\"`，`\` 写作 `\\`，U+0008 写作 `\b`，U+0009 写作 `\t`，U+000A 写作 `\n`，U+000C 写作 `\f`，U+000D 写作 `\r`，其余低于 U+0020 的码位写作 `\u00xx`（十六进制小写）；其他所有字符（包括 `/`、`<`、`>`、`&`、U+007F 和全部非 ASCII 字符）都按原样输出 UTF-8；
* 整数用十进制，不带正负号、前导零、小数和指数，取值范围 0 … 2^53−1（保证任何 RFC 8785 实现都能逐字节复现）。

数组的顺序不由 RFC 8785 规定，由下文规定。

### 3.2 节点类型

每个节点都有 `"format": 1`，并属于以下三种之一。键按表中顺序出现（即 RFC 8785 的顺序）。

| kind | 结构 |
|---|---|
| `dir` | `{"entries":[<条目>,…],"format":1,"kind":"dir"}` —— 一个完整目录 |
| `dir_page` | `{"entries":[<条目>,…],"format":1,"kind":"dir_page"}` —— 大目录的一页（不得为空） |
| `dir_index` | `{"format":1,"kind":"dir_index","pages":[<引用>,…]}` —— 指向页或下一层索引（不得为空） |

一个节点至多 1024 个条目或引用，规范编码至多 1,048,576 字节。

### 3.3 条目

| 类型 | 结构 |
|---|---|
| 文件 | `{"hash":"sha256:<hex>","mode":420,"name":"a.txt","size":1234,"type":"file"}` |
| 目录 | `{"hash":"sha256:<hex>","name":"docs","type":"dir"}` |

* `name` 是一个路径段的规范（NFC）形式。
* 文件的 `hash` 是其内容身份（第 1 节），`size` 是字节数，`mode` 只能是 `420`（0644，普通文件）或 `493`（0755，可执行）。实现把本地权限中任一执行位置位映射为 `493`，否则为 `420`。不保存其他元数据：没有修改时间、属主和其他权限位。
* 目录的 `hash` 是该目录顶层节点的 hash：`dir` 节点或顶层 `dir_index` 节点，绝不是 `dir_page`。
* 条目按 `name` 的 UTF-8 字节序严格升序排列（这不是 UTF-16 顺序：`｡` U+FF61 排在 `𝄞` U+1D11E 之前）。同级名字遵守 2.2 节。

### 3.4 索引引用

`dir_index` 中的引用为 `{"count":N,"first":"<名字>","hash":"sha256:<hex>"}`：

* `hash` 是一个 `dir_page` 或下一层 `dir_index` 的 hash；
* `count` 是被引用节点下面的目录条目数（下一层索引时为其各引用 count 之和）；
* `first` 是被引用节点下面第一个目录条目的名字。

引用按 `first` 的 UTF-8 字节序严格升序排列。

### 3.5 大目录拆分

阈值在第 1 版中固定，不可配置。

```text
layout(entries):                      # entries 已按 3.3 排序
  if len(entries) <= 1024 and size(由 entries 组成的 dir 节点) <= 1048576:
      return node("dir", entries)
  pages = greedy(entries, "dir_page")
  refs  = [ref(count=len(p), first=p[0].name, node("dir_page", p)) for p in pages]
  while len(refs) > 1024 or size(由 refs 组成的 dir_index 节点) > 1048576:
      groups = greedy(refs, "dir_index")
      refs   = [ref(count=sum(r.count for r in g), first=g[0].first, node("dir_index", g))
                for g in groups]
  return node("dir_index", refs)

greedy(items, kind):                  # 保持顺序，依次填满每个节点
  开一个新节点；对每一项：若加入后会超过 1024 项或使节点编码超过 1048576 字节，
  就关闭当前节点、开新节点；然后加入该项。
```

含 `i1…in` 的节点大小即其编码长度：该类型空节点的大小 + 各项大小 + `n−1` 个逗号。空节点编码分别为 38 字节（`dir`）、43 字节（`dir_page`）、42 字节（`dir_index`）。

由此：1024 个条目是一个 `dir` 节点；1025 个条目是一个 `dir_index` 加两页（1024 和 1 个条目）；超过 1024 × 1024 个条目时增加第二层索引。在大目录靠前位置插入名字，会改写其后的各页（第 1 版接受这一点）。

说明（非规范性）：在第 2 节的路径限制下，单个条目编码至多 651 字节，1024 个条目约 667 KB 以内，因此对合法名字来说字节上限不会触发；它仍是格式的一部分。

### 3.6 根与空目录

根是 `dir` 或 `dir_index` 节点。只有根可以为空，此时为 `{"entries":[],"format":1,"kind":"dir"}`（hash `sha256:e515bab4a24abb9c4929143865d247855e153af697b270afb65a0dcb1b62d9c4`）。指向空节点的目录条目是非法的。

### 3.7 节点 hash

节点 hash 是其**未压缩**规范字节的 SHA-256，采用第 1 节的文本形式。节点如何压缩、存在哪里都不影响 hash。

### 3.8 严格解码

读取方必须拒绝节点，除非以下条件全部满足：

1. 不超过 1,048,576 字节，是合法 UTF-8，是单个 JSON 对象，键与其类型完全一致，`format` = 1，`kind` 已知；
2. 条目和引用的键与其类型完全一致；名字是已为 NFC 的合法路径段；hash 为规范形式；mode 为 420 或 493；整数在范围内；不存在 symlink 或其他类型；
3. 名字（以及 `first`）严格升序，节点内同级名字遵守 2.2 节；`dir_page` 和 `dir_index` 不为空；
4. 按 3.1 节重新编码解码结果，得到与输入完全相同的字节，并且字节的 SHA-256 等于期望的 hash。

遍历整棵树的读取方还必须检查：除根以外没有空目录；每个引用的 `count` 和 `first` 与被引用节点一致；一个目录所有页中的条目整体升序且无冲突；每个分页目录的拆分方式与 3.5 节完全一致。以上规则合在一起，保证每一组文件只有唯一一棵合法的树。

### 3.9 示例

只含一个文件 `a.txt`（内容为 `hello\n`，6 字节）的树：

```json
{"entries":[{"hash":"sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03","mode":420,"name":"a.txt","size":6,"type":"file"}],"format":1,"kind":"dir"}
```

它的 hash（也就是根 hash）为 `sha256:c054d695dcf341ac6d5570a0a7e0c1cbcf14379854ea857a1643c9bddf34695a`。

## 4. 存储 key

对象存放在由 hash 推导出的 key 下。`<hex>` 是 64 位小写 SHA-256，`<hh>` 是它的前两个字符。

| 对象 | 相对 key |
|---|---|
| 文件内容 | `objects/<hh>/<hex>` |
| 树节点 | `tree/<hh>/<hex>.json.zst` —— 规范字节经 zstd 压缩后存储；`<hex>` 是未压缩字节的 hash |

* 完整对象 key = `<storage_prefix>/<相对 key>`。数据库只保存相对 key。
* `storage_prefix` 在创建 workspace 时确定，之后不再改变。它非空，没有首尾 `/`，每一段都满足第 2 节，已是 NFC，至多 1024 字节。
* project 级 workspace 的默认前缀为 `tenants/<tenant_id>/projects/<project_id>/workspaces/<workspace_id>`。宿主平台可以选择其他前缀。
* 内容对象不在 workspace 之间共享。
* 树节点的压缩方式只是存储细节，以后可以更换；读取方解压后校验规范字节的 hash。

## 5. 核对一个实现

* `testdata/vectors/contenthash.json`、`pathx.json` 和 `tree.json` 给出输入和期望结果；生成规则见 [向量说明](../../../testdata/vectors/README.md)。
* `mfgraph debug tree <目录> --out <输出目录>` 打印本地目录对应树的每个节点，并把每个节点的规范字节写成 `<输出目录>/<hex>.json`；对这些文件运行 `shasum -a 256`，结果必须与文件名一致。
