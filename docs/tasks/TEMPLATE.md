---
type: ⟨feat|fix|perf|refactor|debt|docs|chore|research|review|release⟩
status: todo        # todo | doing | blocked | awaiting-author | done
weight: full
created: ⟨YYYY-MM-DD⟩
baseline: ⟨开卡时的 commit；gate 回执里的测试计数⟩
---

# ⟨一句话标题：写结果，不写动作⟩

<!--
每一节都对应一种防债机制，删掉一节就等于放弃那种保护：
  来源      防「为什么做这个」在三个月后无人知晓
  范围      防范围蔓延；并行卡按目录划分归属
  观测面    防 GREEN 被截断（提前退出、跳过分支、吞掉异常）
  环境假设  防「规约对，世界不是那样」——先对拍，再实现
  完成定义  防「做着做着改了验收标准」（freeze-check 以开卡提交为冻结点）
  证据      防「声称跑过了」（verify-red / gate --record 机器生成）
⟨…⟩ 是占位符：status 离开 todo 后仍有占位符，lint 失败。
-->

## 来源

⟨为什么有这张卡：哪次讨论、哪条 PLAN 决策、哪次实测。缺陷要写清是一直就有的，还是某个提交引入的。⟩

## 范围

- **独占：** ⟨本卡独占的目录或文件；同一时间其他卡不碰⟩
- **不碰：** ⟨显式排除的文件、既有断言、常量取值、外部契约⟩

## 不变量

<!-- 可选。本卡建立或依赖的不变量，登记到 docs/INVARIANTS.md 后在此引用编号。 -->

- ⟨INV-n：一句话不变量 — 检查方式：property / 代码断言 / 模型检查⟩

## 观测面

⟨本卡靠什么判定行为：返回值、事件、文件、协议消息还是退出码。⟩
⟨GREEN 可能怎样被截断（提前 return、走了跳过的分支、异常被吞、断言前进程退出），各用什么挡住。⟩

## 环境假设

⟨对外部系统（OS、内核、第三方 API、模型供应商、硬件）的每条假设，以及怎样和真实系统对拍。对拍先于实现，结果写在这里。没有外部假设就写「无」并说明理由。⟩

## 完成定义（动工前冻结；实现中不得放宽）

- **D1** ⟨一条可检查的判据，写结果不写过程。⟩
  - 检：test `⟨完整测试名或过滤串⟩`
- **D2** ⟨……⟩
  - 检：property `⟨属性测试名⟩`
- **D3** 计数：测试数只因本卡新增的测试而增加（基线见 front matter 的 baseline）。
  - 检：cmd `python3 tools/keel.py gate`
- **D4** 不弱化既有测试。
  - 检：cmd `python3 tools/keel.py assert-guard`

<!-- 检的 kind：test property inject cmd grep author measure。
     author：作者手测，作者确认后在行尾追加「已确认：YYYY-MM-DD」；没确认前状态只能停在 awaiting-author。 -->

## 设计

⟨为什么这样做，以及考虑过并否决了什么。否决的理由比选中的理由更值得写。中途决策附先例证据（项目、star 数、许可证、链接）。⟩

## 验证命令

```bash
⟨定向测试命令⟩
python3 tools/keel.py verify-red docs/tasks/⟨本卡⟩.md
python3 tools/keel.py assert-guard
python3 tools/keel.py gate --record docs/tasks/⟨本卡⟩.md
```

## 证据

<!-- 机器可读，由 keel 解析：
RED: <commit> <测试过滤串>  # D1      ← verify-red 在该 commit 重跑，必须红；再在工作树重跑，必须绿
```diff keel-inject <过滤串>           ← 可选：故意改坏产品代码的补丁，打到 HEAD 上必须红
--- a/src/x
+++ b/src/x
...
```
GATE: …                               ← 由 gate --record 写入，不要手写
-->

⟨失败断言原文（RED 时测试打印的那一句）⟩

## 修订

<!-- 冻结后对完成定义的任何改动：- D<n> 改了什么、为什么（作者确认 YYYY-MM-DD） -->
