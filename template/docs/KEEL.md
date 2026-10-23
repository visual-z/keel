# keel 手册

`keel` 是工作区的可信内核：它从 git 和真实退出码重新推出卡上的每一条声明，不采信叙述。
它是一个没有运行时依赖的静态二进制，和项目用什么语言无关；技术栈相关的知识全在 `keel.toml` 里。
源码与发布：[github.com/visual-z/keel](https://github.com/visual-z/keel)。

## 安装与版本

每个仓库在 `keel.toml` 里钉住 keel 的版本，版本不一致时 keel 拒绝运行：

```toml
[keel]
version = "1.0.1"
```

安装钉住的版本（任选一种）：

```bash
mise install                                             # 推荐：mise.toml 里已写好 "github:visual-z/keel"
go install github.com/visual-z/keel/cmd/keel@v1.0.1      # 有 Go 工具链时
# 或从 GitHub Release 下载对应平台的二进制，核对 checksums.txt
```

升级 keel 是一次刻意的改动：改 `keel.toml` 与 `mise.toml` 里的版本，跑一遍 `keel gate`，作为一次提交。

## 命令

| 命令 | 做什么 | 失败意味着 |
|---|---|---|
| `init --name N --stack S` | 在空仓库里写入模板、`keel.toml`（预设 S）、`mise.toml`，启用 `.githooks` | — |
| `adopt [--stack S]` | 已有项目接入：只补缺的文件（`keel.toml`、卡模板、hooks），把现存的不合规卡写进存量清单（只写一次） | — |
| `new <type> <slug> [--light]` | 从模板开卡，填好类型、日期和基线 commit | — |
| `status [--all]` | 按状态列出卡 | — |
| `lint [卡…]` | 卡的结构：front matter、必需的节、每条 `D<n>` 都绑定了检查、状态与证据一致 | 卡的格式不合格，或者声称完成但缺少证据 |
| `freeze-check [卡…]` | 拿当前完成定义和开卡提交（冻结点）比较；存量清单只减不增 | 完成定义被悄悄改动或删除 |
| `verify-red <卡> [--at REV] [--repeat N] [--no-green]` | 在临时 worktree 里重跑每条 `RED:` 和每个 `keel-inject`，必须红；再在工作树里重跑，必须绿 | 测试不在测它声称的东西 |
| `audit-red [--retain] [--json]` | 所有卡（含存量卡）的 RED commit 都还在，并已钉在保留 ref 下；`--retain` 先校验全部再一次性钉住 | 证据丢了，或声明格式错 |
| `snapshot <路径…>` | 把 HEAD 加上指定文件提交成一个私有快照，不动 index 和分支；输出 sha，供 `RED:` 使用 | — |
| `assert-guard [--base B]` | 在 diff 里查删掉的断言、新增的 skip/only、测试里的早退出和 `__eq__` 覆盖、被删的测试文件 | 测试被弱化了 |
| `gate [--record 卡] [--keep-going] [--only a,b]` | 依次跑 `[[gate.step]]`，直接读退出码，解析测试计数，输出一行回执；`--only` 的结果不能 `--record` | 起点或终点不是绿的 |
| `check-docs` | AGENTS 行数上限、PLAYBOOK 规则数上限、坏链接与绝对路径链接、地图里的过期路径、决策编号、注册表指向的位置、PROGRESS 顺序、词表禁词 | 文档和代码脱节了 |
| `check-boundaries` | `[[boundary.rule]]` 定义的正则级架构边界 | 越界依赖或禁用调用 |
| `mutate [--base B]` | 跑配置好的、只针对 diff 的变异测试工具 | 存活的变异体，就是测试分辨不出的改动 |
| `hook pre-commit` / `hook commit-msg <文件>` | git hooks：快速检查（lint、冻结、文档、边界、弱化测试）全部跑完再汇总；提交信息须为 Conventional Commits | — |

退出码：0 通过，1 检查失败（输出 `FAIL  <原因>`），2 用法错误。

## 卡片格式

````markdown
---
type: fix            # feat fix perf refactor debt docs chore research review release
status: doing        # todo doing blocked awaiting-author done
weight: light        # full | light
created: 2026-01-01
baseline: 1a2b3c4d5e6f
reason: …            # 只有 blocked 时必填
---

## 完成定义（动工前冻结；实现中不得放宽）

- **D1** 一条可检查的判据。
  - 检：test `完整测试名或过滤串`
- **D2** 作者手测的判据。
  - 检：author 打开设置页，切换主题  已确认：2026-01-02

## 证据

RED: 1a2b3c4d5e6f test_name_substring  # D1
RED(web): 9f8e7d6c5b4a formats binary units

```diff keel-inject test_name_substring
--- a/src/calc.py
+++ b/src/calc.py
@@ -2,1 +2,1 @@
-    return a + b
+    return a - b
```

GATE: 9f8e7d6c5b4a+dirty ok build=0 test=0[812p/0f] … (2026-01-02)
````

规则：

- **必需的节。** 完整卡要有 来源 / 范围 / 观测面 / 环境假设 / 完成定义 / 设计 / 验证命令 / 证据；轻量卡要有 问题 / 完成定义 / 证据。
- **检查的 kind。** `test` `property` `inject` `cmd` `grep` `author` `measure`。其中 test、property、cmd、grep 要把目标写在反引号里。
- **占位符。** 模板用 `⟨…⟩` 标占位（不用 `<…>`，因为代码里的泛型合法）。状态离开 todo 后还有占位符，lint 失败。
- **RED 声明的语法是严格的。** 以 `RED:` 或 `RED(<profile>):` 开头的行必须是 `<7–64 位十六进制 commit> <非空过滤串>`；分支名、`HEAD`、占位符、空过滤串都是错误，不会被悄悄忽略。行尾 `# D1, D2` 是注释。`RED: none <理由>` 只允许用在非行为卡。
- **RED 的要求。** feat、fix、perf 卡进入 awaiting-author 或 done 时，必须有 `RED:` 或 `keel-inject`。
- **done 的要求。** 需要一行覆盖全部 gate 步骤、且每步退出码为 0 的 `GATE: … ok` 回执，并且所有 author 类检查都带「已确认：日期」。
- **不解析的地方。** 代码块和 HTML 注释里的内容不算声明。
- **修订。** `## 修订` 下写「D<n> … 作者确认 YYYY-MM-DD」的条目，`freeze-check` 才放行对应编号的改动。
- **允许弱化。** `WEAKEN-OK: <测试文件> <理由>（作者确认 YYYY-MM-DD）` 让 `assert-guard` 放过那个文件。

## verify-red 判定什么

**红**必须同时满足：
- 没有超时，没有被信号杀死；
- 输出能被解析，并且没有编译错误、加载错误、收集错误、包级失败；
- 过滤器选中并**实际执行**了至少 1 个测试（跳过的、`#[ignore]` 的不算）；
- 至少 1 个测试失败；
- 退出码是该格式表示「有测试失败」的那个码（cargo 101，其余 1）。退出码与报告不一致就不算红。

**绿**必须同时满足：退出码为 0，选中并执行的测试数大于 0，失败数为 0，输出无异常。

**重复。** `[red] repeat = N`（或 `--repeat N`）让每个声明跑 N 次，每次都必须满足判定；偶发的测试证明不了任何东西。

**故障补丁**只能改产品代码：碰到 `tests.paths` 里的文件会被拒绝。补丁在 HEAD 的临时 worktree 里打上（允许零上下文的单行补丁），主工作区不受影响。

**保留。** RED 用的 commit 只在验证通过之后钉到 `<ref_prefix><sha>`（默认 `refs/keel/red/`），squash 或 reflog 过期都抹不掉它。已存在的 ref 指向别处、或是符号引用时，keel 拒绝并且不移动它。

## 输出格式（runner）

keel 认的是**输出格式**，不是工具。能用结构化输出就不解析文本：

| runner | 读什么 | 适用 |
|---|---|---|
| `go-json` | `go test -json` 的事件流 | Go |
| `junit` | 命令写到 `{junit}` 的 JUnit XML 报告 | pytest `--junitxml`、vitest/bun `--reporter=junit`、jest-junit、cargo-nextest |
| `cargo` | `test result:` 行（cargo 没有稳定的结构化输出） | `cargo test` |
| `bun` | 恰好一份正常的 bun 摘要 | `bun test` |
| `custom` | `selected_re` / `failed_re` / 可选 `invalid_re` / `red_exit` | 其他 |

JUnit 报告里的 `<error>`（setup、collection 出错）、重复的 testcase、空报告都不算证据；思路参照 [regression-witness](https://github.com/qjf1597300-cloud/regression-witness)。

## keel.toml

```toml
[keel]
version = "1.0.1"             # 必填：钉住的 keel 版本

[[gate.step]]                 # 按顺序执行；第一条失败即停
name = "test"
cmd = ["cargo", "test", "--locked"]
runner = "cargo"              # 可选：解析计数；选中 0 个测试就判失败
cwd = "."                     # 可选：相对仓库根的运行目录
timeout_s = 0                 # 可选：0 = 不限

[red]
runner = "cargo"
command = ["cargo", "test", "--locked", "{args}"]
link = []                     # 链进临时 worktree 的依赖目录，例如 ["node_modules"]
env = { CARGO_TARGET_DIR = "{cache}/cargo-target" }   # 只给临时 worktree；{cache} 指 .keel/cache
timeout_s = 3600
repeat = 1                    # 每个声明跑几次，必须全部一致
ref_prefix = "refs/keel/red/" # 保留 ref 的前缀（已有项目可沿用自己的）

[red.profile.web]             # 多套测试命令；未写的字段继承 [red]
runner = "junit"
cwd = "web"
command = ["node_modules/.bin/vitest", "run", "-t", "{filter}", "--reporter=junit", "--outputFile.junit={junit}"]
link = ["web/node_modules"]

[tests]                       # assert-guard 与 keel-inject 用
paths = ["tests/**"]          # 什么是测试文件（故障补丁不得改它们）
inline_paths = ["src/**"]     # 另外要统计断言的位置（Rust 的内联测试和 debug_assert）
assert_patterns = ['…']
skip_patterns = ['…']
suspicious_patterns = ['…']   # 早退出、永远相等

[paths]
tasks = "docs/tasks"
legacy_manifest = "docs/tasks/legacy.txt"

[docs]
agents = "AGENTS.md"
agents_max_lines = 150
playbook = "docs/PLAYBOOK.md"
playbook_max_rules = 120
progress = "docs/PROGRESS.md"
plan = "PLAN.md"
decisions_heading = "决策记录"   # 只检查这个标题下的表格编号
invariants = "docs/INVARIANTS.md"
fixtures = "docs/FIXTURES.md"
glossary = "CONTEXT.md"
glossary_source_dirs = ["src"]
link_exclude = []                # 不做链接检查的 glob
map_files = []                   # 检查反引号路径的「地图」文件；空 = agents、fixtures、invariants

[[boundary.rule]]
name = "core 不做 I/O"
paths = ["src/core/**"]
except = []
forbid = ['^use std::fs']

[mutate]
command = ["cargo", "mutants", "--in-diff", "{diff}"]   # {diff} 为本分支 diff 文件，{base} 为合并基点
```

未知的键是错误：拼错的键就是一项永远不会运行的检查。

命令里的占位符：

| 占位符 | 替换为 |
|---|---|
| `{filter}` | 声明里的过滤串，作为一个参数 |
| `{args}` | 过滤串按 shell 规则拆成多个参数（`-p server --test ssh_agent live_policy::`） |
| `{junit}` | 本次运行要写的 JUnit 报告路径（也可用在 `env` 的值里） |
| `{existing:a,b}` | 列出的路径中，在运行目录里存在的那些（老快照里可能还没有某个测试目录） |

## 在已有项目中采用

```bash
keel adopt --stack cargo     # 没有 keel.toml 时从预设生成；已有就沿用
# 把 [[gate.step]] 改成这个项目原有的「完成」定义，不放宽任何一条
```

- 存量清单里的卡**豁免**格式检查（lint / freeze-check / 链接检查）；不在清单里的卡必须合规。
- 存量卡里的 `RED:` 行**仍然是声明**，`audit-red` 照样检查它们的语法和 commit。
- 清单**只能缩短**：`freeze-check` 拿当前清单和它第一次提交的版本比较，多出来的名字就失败。迁移一张老卡，就是把它改合规，然后删掉它那一行。清单里列的卡如果已经不存在，lint 失败。
- 文档预算按现状设成**棘轮**：`agents_max_lines`、`playbook_max_rules` 填当前值，只降不升。

## 回执与日志

- `gate` 的完整输出写到 `.keel/logs/`（已被 gitignore，原始日志不入库），卡里只记一行 `GATE:` 回执。
- 回执里的 `+dirty` 表示跑的时候有未提交改动。这是常态：回执通常和实现在同一次提交里写进卡。
