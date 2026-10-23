# keel

和项目无关的工作区模板：所有新项目从这里开始。

keel 把每张任务卡当作一个**待证明的命题**：完成定义是规约，测试是证明，`tools/keel.py` 是证明检查器。
卡上的每条声明，例如「这个测试修复前是红的」「完成定义没有放宽」「gate 全绿」，都由 keel 从 git 和真实的退出码重新推出来，不采信叙述。

设计依据见 [docs/PRINCIPLES.md](docs/PRINCIPLES.md)，主要来自以下几方面：

- 形式化验证的几条经验：短规约、只信检查器、警惕空洞证明、注错比随机测试抓到更多 bug、观测面会被截断、规约要和现实对拍、冗长会撞墙。
- 长时 agent harness 和 harness engineering 的实践。
- 社区对 agent「看起来完成」类捷径（reward hacking）的研究。

## 开始一个新项目

```bash
gh repo create <name> --private --template <owner>/keel --clone
cd <name>
python3 tools/keel.py init --name <name> --stack <cargo|go|bun|vitest|jest|pytest>
```

`init` 会做这些事：

- 用 `tools/presets/<stack>.toml` 生成 `keel.toml`；
- 替换 `{{PROJECT_NAME}}`；
- 把 `README.project.md` 换成你的 README；
- 启用 `.githooks`；
- 在 `docs/PROGRESS.md` 里记一笔。

然后把 `AGENTS.md`、`PLAN.md`、`CONTEXT.md` 里的 `⟨…⟩` 占位符填掉，开第一张卡：

```bash
python3 tools/keel.py new feat skeleton
```

需要 Python 3.11+ 和 git，不需要安装别的东西。

## 里面有什么

```
AGENTS.md            agent 指引的唯一入口（地图，≤150 行）；CLAUDE.md 只 import 它
PLAN.md              目标、非目标、预算、里程碑（每层一条可检查的退出条件）、决策记录
CONTEXT.md           词表；「避免」列的词出现在源码里即失败
keel.toml            唯一的机器配置：gate 步骤、RED 的跑法、测试与断言模式、边界规则
docs/
  PRINCIPLES.md      为什么是这套流程（每条原则 → 来源 → 机制）
  PLAYBOOK.md        专题规则，「一句话 + 判据」，总数有上限
  KEEL.md            keel 命令与卡片格式手册
  INVARIANTS.md      不变量登记（代码断言 > property > 模型检查 > 样例）
  FIXTURES.md        可复用的验证夹具（「引理」），卡里按名字引用
  PROGRESS.md        会话交接，最新在上
  tasks/             任务卡与两种模板
  examples/          一张填好的完整卡
  adr/  research/    重大决策与调研笔记的模板
tools/
  keel.py            可信内核：单文件、只用标准库
  presets/           各技术栈的 keel.toml 预设
  tests/             keel 自己的测试（每项检查都演示过抓得住违规）
  self_mutation.py   把 keel 的每条判定逐一改坏，要求测试变红
.githooks/           pre-commit（lint、冻结、文档、弱化测试检查）与 commit-msg（Conventional Commits）
.github/             CI：跑 gate，并对 PR 改动到的卡跑 verify-red
.claude/settings.json  push、amend、`git add -A` 等操作需要确认或被禁止
```

## 一张卡的生命周期

1. `keel new fix <slug>` 开卡，写完成定义：每条 `D<n>` 都要绑定一个检查。
2. **开卡提交**：卡加上新写的红测试。这个 commit 就是 RED 证据，也是完成定义的冻结点。
3. 实现。完成定义只能加严，要改就在 `## 修订` 里写明，并由作者确认。
4. 跑 `keel verify-red`（在 RED commit 上必须红，修好后必须绿）、`keel assert-guard`（没有弱化测试）、`keel gate --record`（写入回执）。
5. 状态改为 `done`；作者可见的改动先停在 `awaiting-author`，等作者手测确认。

## 改进这个模板

这个仓库也用它自己检查自己：`python3 tools/keel.py gate`。改 keel 的检查时，`tools/tests/` 里要同时有「抓到违规」和「干净通过」两条测试。
