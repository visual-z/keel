# keel 手册

`tools/keel.py` 是工作区的可信内核：它从 git 和真实退出码重新推出卡上的每一条声明，不采信叙述。
单文件，只用标准库，需要 Python 3.11+。技术栈相关的知识全在 `keel.toml` 里。

## 命令

| 命令 | 做什么 | 失败意味着 |
|---|---|---|
| `init --name N --stack S` | 用 `tools/presets/S.toml` 生成 `keel.toml`，替换占位符，启用 `.githooks` | — |
| `adopt` | 已有项目接入 keel：把现存的不合规卡写进存量清单（只写一次） | — |
| `new <type> <slug> [--light]` | 从模板开卡，填好类型、日期和基线 commit | — |
| `status [--all]` | 按状态列出卡 | — |
| `lint [卡…]` | 卡的结构：front matter、必需的节、`D<n>` 都绑定了检查、状态与证据一致 | 卡的格式不合格，或者声称完成但缺少证据 |
| `freeze-check [卡…]` | 拿当前完成定义和开卡提交（冻结点）比较 | 完成定义被悄悄改动或删除 |
| `verify-red <卡>` | 在临时 worktree 里重跑每条 `RED:` 和每个 `keel-inject`，必须红；再在工作树里重跑，必须绿 | 测试不在测它声称的东西 |
| `audit-red [--retain]` | 所有卡的 RED commit 都还在，并已钉在 `refs/keel/red/` 下 | 证据丢了 |
| `snapshot <路径…>` | 把 HEAD 加上指定文件提交成一个私有快照，不动 index 和分支；输出 sha，供 `RED:` 使用 | — |
| `assert-guard [--base B]` | 在 diff 里查删掉的断言、新增的 skip/only、测试里的早退出和 `__eq__` 覆盖、被删的测试文件 | 测试被弱化了 |
| `gate [--record 卡] [--keep-going] [--only 名…]` | 依次跑 `[[gate.step]]`，直接读退出码，解析测试计数，输出一行回执 | 起点或终点不是绿的 |
| `check-docs` | AGENTS 行数上限、PLAYBOOK 规则数上限、坏链接、地图里的过期路径、决策编号、注册表指向的位置、词表禁词 | 文档和代码脱节了 |
| `check-boundaries` | `[[boundary.rule]]` 定义的正则级架构边界 | 越界依赖或禁用调用 |
| `mutate [--base B]` | 跑配置好的、只针对 diff 的变异测试工具 | 存活的变异体，就是测试分辨不出的改动 |

所有命令失败时都退出 1，并输出 `FAIL  <原因>`；用法错误退出 2。

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

```diff keel-inject test_name_substring
--- a/src/calc.py
+++ b/src/calc.py
@@ …
```

GATE: 9f8e7d6c5b4a+dirty ok build=0 test=0[812p/0f] … (2026-01-02)
````

规则：

- **必需的节。** 完整卡要有 来源 / 范围 / 观测面 / 环境假设 / 完成定义 / 设计 / 验证命令 / 证据；轻量卡要有 问题 / 完成定义 / 证据。
- **检查的 kind。** `test` `property` `inject` `cmd` `grep` `author` `measure`。其中 test、property、cmd、grep 要把目标写在反引号里。
- **占位符。** 模板用 `⟨…⟩` 标占位（不用 `<…>`，因为代码里的泛型合法）。状态离开 todo 后还有占位符，lint 失败。
- **RED 的要求。** feat、fix、perf 卡进入 awaiting-author 或 done 时，必须有 `RED:` 或 `keel-inject`；这三类不允许写 `RED: none`。
- **done 的要求。** 需要一行 `GATE: … ok`，并且所有 author 类检查都带「已确认：日期」。
- **不解析的地方。** 代码块和 HTML 注释里的 `RED:` / `GATE:` 不算声明。
- **修订。** `## 修订` 下写「D<n> … 作者确认 YYYY-MM-DD」的条目，`freeze-check` 才放行对应编号的改动。
- **允许弱化。** `WEAKEN-OK: <测试文件> <理由>（作者确认 YYYY-MM-DD）` 让 `assert-guard` 放过那个文件。

## 在已有项目中采用

已有项目的老卡格式各不相同，不需要一次改完：

```bash
cp <keel>/tools/keel.py tools/keel.py      # 内核原样复制，不就地修改
# 写 keel.toml：gate 步骤沿用项目原有的「完成」定义
python3 tools/keel.py adopt                 # 生成 docs/tasks/legacy.txt
```

- 清单里的卡**豁免** lint / freeze-check / audit-red / 链接检查；不在清单里的卡必须合规。
- 清单**只能缩短**：`freeze-check` 拿当前清单和它第一次提交的版本比较，多出来的名字就失败。迁移一张老卡，就是把它改合规，然后删掉它那一行。
- 清单里列的卡如果已经不存在，lint 失败（防止清单腐烂）。
- 文档预算按现状设成**棘轮**：`agents_max_lines`、`playbook_max_rules` 填当前值，只降不升。

## 多套测试命令

多语言仓库在 `[red.profile.<名字>]` 里声明另外的测试命令，未声明的字段继承 `[red]`；`cwd` 是相对仓库根的运行目录：

```toml
[red.profile.web]
runner = "vitest"
cwd = "web"
command = ["pnpm", "exec", "vitest", "run", "-t", "{filter}"]
link = ["web/node_modules"]
```

卡里用 `RED(web): <commit> <过滤串>`，故障补丁用 ```` ```diff keel-inject(web) <过滤串> ````。

`command` 里的 `{filter}` 会把过滤串当成一个参数整体替换；`{args}` 则按 shell 规则拆成多个参数，适合需要多个选择参数的测试命令：

```toml
command = ["cargo", "test", "--locked", "{args}"]
# RED: 1a2b3c4 -p server --test ssh_agent live_policy::
```

## verify-red 判定什么

**红**必须同时满足：
- 退出码非 0，没有被信号杀死，没有超时；
- 过滤器选中了至少 1 个测试；
- 至少 1 个测试失败；
- 输出里没有编译错误、加载错误、收集错误。

**绿**必须同时满足：退出码为 0，选中数大于 0，失败数为 0。

**故障补丁**只能改产品代码：碰到 `tests.paths` 里的文件会被拒绝。补丁在 HEAD 的临时 worktree 里打上，主工作区不受影响。

内置的输出解析器有 `cargo`、`go`、`bun`、`pytest`、`vitest`、`jest`、`unittest`。其他栈设 `runner = "custom"`，再提供 `selected_re`、`failed_re`，可选 `invalid_re`。

## keel.toml

```toml
[[gate.step]]                 # 按顺序执行；第一条失败即停
name = "test"
cmd = ["cargo", "test", "--locked"]
runner = "cargo"              # 可选：解析计数；选中 0 个测试就判失败
cwd = "."                     # 可选：相对仓库根的运行目录

[red]
runner = "cargo"
command = ["cargo", "test", "--locked", "{filter}"]
link = []                     # 链进临时 worktree 的依赖目录，例如 ["node_modules"]
env = { CARGO_TARGET_DIR = "{cache}/cargo-target" }   # {cache} 指 .keel/cache
timeout_s = 3600

[tests]                       # assert-guard 与 keel-inject 用
paths = ["tests/**"]          # 什么是测试文件（故障补丁不得改它们）
inline_paths = ["src/**"]     # 另外要统计断言的位置（例如 Rust 的内联测试和 debug_assert）
assert_patterns = ['…']
skip_patterns = ['…']
suspicious_patterns = ['…']   # 早退出、永远相等

[docs]
agents_max_lines = 150
playbook_max_rules = 120
glossary_source_dirs = ["src"]
# 文档位置（以下为默认值）
agents = "AGENTS.md"
playbook = "docs/PLAYBOOK.md"
progress = "docs/PROGRESS.md"
plan = "PLAN.md"
decisions_heading = "决策记录"   # 只检查这个标题下的表格编号
invariants = "docs/INVARIANTS.md"
fixtures = "docs/FIXTURES.md"
glossary = "CONTEXT.md"
link_exclude = []                # 不做链接检查的 glob，例如归档的调研目录
map_files = []                   # 检查反引号路径的「地图」文件；空 = agents、fixtures、invariants

[paths]
tasks = "docs/tasks"
legacy_manifest = "docs/tasks/legacy.txt"

[[boundary.rule]]
name = "core 不做 I/O"
paths = ["src/core/**"]
except = []
forbid = ['^use std::fs']

[mutate]
command = ["cargo", "mutants", "--in-diff", "{diff}"]   # {diff} 为本分支 diff 文件，{base} 为合并基点
```

## 回执与日志

- `gate` 的完整输出写到 `.keel/logs/`（已被 gitignore，原始日志不入库），卡里只记一行 `GATE:` 回执。
- 回执里的 `+dirty` 表示跑的时候有未提交改动。这是常态：回执通常和实现在同一次提交里写进卡。

## 修改 keel 本身

- keel 也是代码。新增或修改任何检查，都要在 `tools/tests/test_keel.py` 里同时写一条「抓到违规」的测试和一条「干净输入通过」的测试。
- 然后跑 `python3 tools/self_mutation.py`：它把每条判定逐一改坏，要求测试变红。新增的检查要同时加一个对应的变异体。
  只拿关键词断言失败原因的测试，可能被另一条判定「碰巧」满足；自变异就是用来抓这种情况的。
- 内核保持单文件、只用标准库，这条由 `keel.toml` 里的 boundary 规则强制。
