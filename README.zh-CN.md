# keel

[English](README.md) | **中文**

和项目无关的工作区模板，供所有新项目起步使用。

keel 把每张任务卡当作一个**待证明的命题**：完成定义是规约，测试是证明，`tools/keel.py` 是证明检查器。
卡上的每条声明，比如「这个测试修复前是红的」「完成定义没有放宽」「gate 全绿」，都由 keel 从 git 和真实的退出码重新推出来，不采信叙述。

## 为什么需要它

让 coding agent 自己报告「做完了」并不可靠。常见的捷径有：
- 删掉断言、加 skip；
- 在断言之前退出进程；
- 改宽松验收标准；
- 写一个从来没红过的测试。

人在压力下也会这样。keel 的做法是**只信检查器**：任何声明都必须能被机器重新验证，否则不算数。

设计依据见 [docs/PRINCIPLES.md](docs/PRINCIPLES.md)。每条原则都写明了来源（形式化验证的经验、长时 agent harness、harness engineering、对 reward hacking 的研究）和落地机制。

## 开始一个新项目

```bash
gh repo create <name> --private --template visual-z/keel --clone
cd <name>
python3 tools/keel.py init --name <name> --stack <cargo|go|bun|vitest|jest|pytest>
```

`init` 做这几件事：
- 用 `tools/presets/<stack>.toml` 生成 `keel.toml`；
- 替换 `{{PROJECT_NAME}}`；
- 把 README 换成项目自己的版本；
- 启用 `.githooks`；
- 在 `docs/PROGRESS.md` 里记一笔。

然后填掉 `AGENTS.md`、`PLAN.md`、`CONTEXT.md` 里的 `⟨…⟩` 占位符，开第一张卡：

```bash
python3 tools/keel.py new feat skeleton
```

只需要 Python 3.11+ 和 git。

## keel 检查什么

| 命令 | 检查的声明 |
|---|---|
| `verify-red <卡>` | 「这个测试修复前是红的」：在 RED commit 的临时 worktree 里重跑，必须红；在当前工作树里必须绿。编译错误、加载错误、匹配 0 个测试、超时都不算红。 |
| `verify-red`（故障补丁） | 「测试真能抓住这个 bug」：把卡里的 `keel-inject` 补丁打到 HEAD 上，测试必须变红；补丁只能改产品代码，不能改测试。 |
| `freeze-check` | 「完成定义没有放宽」：和开卡提交（冻结点）对比，条目只能增加；修改需要作者在 `## 修订` 里确认。 |
| `assert-guard` | 「没有弱化测试」：diff 里删掉的断言、新增的 skip/only、测试里的早退出与 `__eq__` 覆盖、被删的测试文件。 |
| `gate --record <卡>` | 「全部检查通过」：直接读每一步的退出码，解析测试计数（选中 0 个测试也算失败），写一行回执到卡里。 |
| `lint` | 卡的结构完整；每条 `D<n>` 都绑定了检查；声明完成时证据齐全；作者手测已确认。 |
| `check-docs` | AGENTS.md 行数和规则手册条数在预算内；没有坏链接和过期路径；决策编号递增；不变量和夹具指向真实存在的代码；词表禁用的词没出现在源码里。 |
| `check-boundaries` | `keel.toml` 里定义的、正则级别的架构边界。 |
| `mutate` | 只对本分支改动的代码跑变异测试。 |

完整手册：[docs/KEEL.md](docs/KEEL.md)。

## 一张卡的生命周期

1. `keel new fix <slug>` 开卡，写完成定义，每条 `D<n>` 都绑定一个检查。
2. **开卡提交**：提交卡和新写的红测试。这个 commit 既是 RED 证据，也是完成定义的冻结点。
3. 实现。完成定义只能加严。
4. 跑 `keel verify-red`、`keel assert-guard`、`keel gate --record`。
5. 状态改为 `done`；作者可见的改动停在 `awaiting-author`，等作者手测确认。

**没有 CI**：所有证据都在本地产生，由 git hook 和上面这些命令保证。pre-commit 只跑快的检查（lint、冻结、文档、边界、弱化测试）；完整的 gate 和 verify-red 在把卡标为 done 之前手动跑。

## 目录

```
AGENTS.md            agent 指引的唯一入口（地图，≤150 行）；CLAUDE.md 只 import 它
PLAN.md              目标、非目标、预算、分层里程碑、决策记录
CONTEXT.md           词表；「避免」列的词出现在源码里即失败
keel.toml            唯一的机器配置：gate 步骤、RED 的跑法、测试模式、边界规则
docs/
  PRINCIPLES.md      为什么是这套流程（原则 → 来源 → 机制）
  PLAYBOOK.md        专题规则，「一句话 + 判据」，总数有上限
  KEEL.md            keel 命令与卡片格式手册
  INVARIANTS.md      不变量登记（代码断言 > property > 模型检查 > 样例）
  FIXTURES.md        可复用的验证夹具（「引理」）
  PROGRESS.md        会话交接，最新在上
  tasks/             任务卡与两种模板
  examples/          一张填好的完整卡
  adr/  research/    重大决策与调研笔记的模板
tools/
  keel.py            可信内核：单文件、只用标准库
  presets/           各技术栈的 keel.toml 预设
  tests/             keel 自己的测试
  self_mutation.py   把 keel 的每条判定逐一改坏，要求测试变红
.githooks/           pre-commit 与 commit-msg（Conventional Commits）
.claude/settings.json  push、amend 需确认；force push、`git add -A` 被禁止
```

## 改进这个模板

这个仓库也用 keel 检查自己：

```bash
python3 tools/keel.py gate          # 测试 + 全部检查
python3 tools/self_mutation.py      # 每条检查都必须有测试能抓到它被删除
```

新增或修改检查时，在 `tools/tests/test_keel.py` 里同时加一条「抓到违规」和一条「干净输入通过」的测试，并在 `tools/self_mutation.py` 里加一个对应的变异体。
