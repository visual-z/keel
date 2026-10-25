# keel

[English](README.md) | **中文**

这个仓库包含两部分：
- 一个和语言无关的工作区模板；
- `keel`：一个检查任务卡上各项声明的单文件静态二进制。

keel 把每张任务卡当作一个**待证明的命题**：完成定义是规约，测试是证明，`keel` 是证明检查器。
卡上的每条声明，比如「这个测试修复前是红的」「完成定义没有放宽」「gate 全绿」，都由 keel 从 git 和真实的退出码重新推出来，不采信叙述。

## 为什么需要它

让 coding agent 自己报告「做完了」并不可靠。常见的捷径有：
- 删掉断言、加 skip；
- 在断言之前退出进程；
- 改宽松验收标准；
- 写一个从来没红过的测试。

人在压力下也会这样。keel 的做法是**只信检查器**：任何声明都必须能被机器重新验证，否则不算数。

项目自己的构建工具（cargo、go、bun、tsc、pytest）检查的是**代码**。keel 检查的是**关于历史的声明**，这些没有哪个语言工具链会检查：
- 「这个测试在 commit X 上是红的」；
- 「这条判据自开卡以来没改过」；
- 「这次 diff 删掉了断言」；
- 「这张回执覆盖了全部步骤」。

这一层和项目用什么语言无关，所以 keel 只实现一次：用 Go 写，发布成一个没有运行时依赖的二进制。每个项目钉住自己用的版本，就像 `rust-toolchain.toml` 钉住 rustc 一样。

## 安装

```bash
mise use github:visual-z/keel@1.0.2                 # 推荐：版本写进 mise.toml
go install github.com/visual-z/keel/cmd/keel@v1.0.2 # 有 Go 工具链时
```

也可以从 [Releases](https://github.com/visual-z/keel/releases) 下载对应平台的压缩包，并用 `checksums.txt` 核对。

每个仓库都在 `keel.toml` 里钉住所用的版本（`[keel] version = "1.0.2"`）。PATH 上的其他版本会切换到这个版本运行，做法同 `GOTOOLCHAIN=auto` 与 bazelisk：从 Release 安装，核对 `checksums.txt`，然后运行。结论不会取决于 PATH 上碰巧是哪个 keel。`KEEL_TOOLCHAIN=local` 表示不下载。

## 开始一个项目

```bash
mkdir demo && cd demo
keel init --name demo --stack cargo       # 预设：cargo、go、bun、vitest、jest、pytest
keel new feat skeleton
```

已有项目用 `keel adopt --stack <预设>`。它只补上缺的东西（`keel.toml`、卡模板、hooks），并把采用 keel 之前的卡冻结进存量清单。这个清单只能缩短，新卡必须合规。

## keel 检查什么

| 命令 | 检查的声明 |
|---|---|
| `verify-red <卡>` | 「这个测试修复前是红的」：在 RED commit 的临时 worktree 里重跑，必须红；在当前工作树里，必须绿。设了 `repeat = N` 时，每一次都必须一致。编译错误、加载错误、跳过或选中 0 个测试、超时、崩溃、退出码和报告对不上，都不算红。 |
| `verify-red`（故障补丁） | 「测试真能抓住这个 bug」：把卡里的 `keel-inject` 补丁打到 HEAD 上，测试必须变红。补丁只能改产品代码，不能改测试。补丁打不上时，就在它能打上的那个提交上提交故障，再用三方合并带到 HEAD，文件移动、上下文挪位都能跟过去；故障所在的代码已经被重写时，如实报告，不去猜。 |
| `freeze-check` | 「完成定义没有放宽」：和开卡那次提交对比，判据只能增加；要修改，必须由作者确认。 |
| `assert-guard` | 「没有弱化测试」：在 diff 里查删掉的断言、新增的 skip/only、测试里的早退出和 `__eq__` 覆盖，以及被删的测试文件。移动过的测试与它原来的内容比较，所以单纯调整目录不会报错；把测试移出测试路径，等同于删除。`--card` 从开卡提交比起；在主干上合并基点就是 HEAD 本身，这时也照样有效。 |
| `gate --record <卡>` | 「全部检查通过」：直接读每一步的退出码，只统计真正执行了的测试（执行 0 个就算失败），并列出失败的测试名。步骤可以单独重跑失败的测试（`retries`）：单独跑每次都通过的算偶发，名字写进回执；单独跑也失败的是真失败。`--record` 还会从开卡提交起检查有没有弱化测试，并且只记录通过的 gate：被测的树提交到 `refs/keel/gate/` 下，回执里写明这棵树。卡只能用「树就是当前要关闭的树」的回执来关闭，所以手写的回执、过期的回执都不算；gate 之后才写的收尾说明不会让回执过期。 |
| `replay [--since REV]` | 「测试仍然抓得住曾经抓住的每个故障」：把每张卡的故障补丁重新打到 HEAD 上，必须红；抓不住的就失败。 |
| `audit-red` | 每条声明（存量卡里的也算）指向的 commit 都还在，并且已钉在 `refs/keel/red/` 下。 |
| `lint`、`check-docs`、`check-boundaries` | 卡的结构与证据；文档预算、链接、地图里的过期路径、决策编号、注册表、词表；正则级的架构边界。 |

keel 认的是**测试的输出格式，而不是具体工具**：能用结构化输出（`go test -json`、JUnit XML）就不解析文本，只有在没有稳定结构化格式的地方（cargo、bun）才解析文本。完整手册见 [template/docs/KEEL.md](template/docs/KEEL.md)。

## 参照了哪些项目，各借鉴了什么

| 项目 | keel 借鉴的内容 |
|---|---|
| [lefthook](https://github.com/evilmartians/lefthook)、[prek](https://github.com/j178/prek) | 跨语言的开发工具做成一个不依赖运行时的静态二进制。prek 正是为了摆脱 pre-commit 的 Python 依赖而重写的，和 keel 放弃 Python 是同一个理由。 |
| [mise](https://mise.jdx.dev/dev-tools/backends/github.html) / [aqua](https://aquaproj.github.io/docs/) | 每个仓库钉住工具版本，从 GitHub Release 安装；产物命名为 `<名>_<版本>_<os>_<arch>.tar.gz`。 |
| [git-test](https://github.com/mhagger/git-test) | 测试结果按被测的树而不是按 commit 记录，并存放在仓库里。 |
| [Go toolchains](https://go.dev/doc/toolchain)、bazelisk | PATH 上的二进制运行仓库钉住的版本，必要时下载并校验。 |
| Gradle test-retry、Playwright | 只重跑失败的测试；重试通过了，第一次的失败也要记下来；大量失败时不重试。 |
| [regression-witness](https://github.com/qjf1597300-cloud/regression-witness) | 用 JUnit XML 而不是文本来判定修复前后的结果；退出码必须和报告一致；多次运行必须一致；缺失、跳过、执行出错的测试都不算证据。 |
| SWE-bench 的 FAIL_TO_PASS | 一个修复的证据，是指定的测试在修复前失败、修复后通过。 |
| [testscript](https://github.com/rogpeppe/go-internal)（go-internal） | keel 自己的端到端测试写成脚本化的场景仓库，和 Go 团队测试 `go` 命令的方式一样。 |
| xeno 的 `verify-red.ts` | keel 的原型：在临时 worktree 里重跑 RED 声明、把快照钉在 ref 下、严格校验 bun 的测试摘要。它的测试都移植成了 keel 的场景。 |

卡片格式背后的设计原则见 [template/docs/PRINCIPLES.md](template/docs/PRINCIPLES.md)，来源包括大规模形式化验证的经验、长时 agent harness 的实践，以及对 reward hacking 的研究。

## 目录

```
cmd/keel/            二进制入口
internal/keel/       各项检查；testdata/script/*.txtar 是端到端场景
internal/selfmut/    逐条改坏检查，要求测试能发现
template/            `keel init` 写入的内容（嵌在二进制里）
  presets/           各技术栈的 keel.toml 预设
scripts/release.sh   交叉编译、生成校验和、发布 Release（没有 CI）
keel.toml            keel 用自己检查自己的仓库
```

## 开发 keel

```bash
go test -race ./...           # 单元测试 + 场景仓库
go run ./internal/selfmut     # 每一项检查，被删掉时都必须有测试发现
keel gate                     # 本仓库自己的 gate
scripts/release.sh 1.0.2 --publish
```

新增或修改检查时，要同时加一个「抓到违规」的场景和一个「干净输入能通过」的场景，再在 `internal/selfmut` 里加一个对应的变异体。
