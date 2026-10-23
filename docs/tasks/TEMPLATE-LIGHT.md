---
type: ⟨fix|chore|docs|debt⟩
status: todo        # todo | doing | blocked | awaiting-author | done
weight: light
created: ⟨YYYY-MM-DD⟩
baseline: ⟨开卡时的 commit⟩
---

# ⟨一句话标题⟩

<!--
轻量卡：缺陷修复或清理，并且同时满足——不改外部契约（协议、API、存储、配置格式；把行为修回既有契约除外），
不新增依赖，源码改动不超过 5 个文件。做到一半超出条件：停下，改用 TEMPLATE.md，不在轻量卡里扩范围。
-->

## 问题

⟨现象、复现步骤；一直就有，还是哪个提交引入的。⟩

## 完成定义（动工前冻结；实现中不得放宽）

- **D1** ⟨可检查的行为判据⟩
  - 检：test `⟨完整测试名⟩`
- **D2** gate 全绿；测试计数只因本卡新增的测试而增加，既有测试没有被弱化。
  - 检：cmd `python3 tools/keel.py assert-guard`

<!-- 纯删除或纯文档改动：type 用 chore/docs，判据写 grep 或计数，证据写「RED: none — 理由」。 -->

## 证据

⟨RED: <commit> <过滤串>  /  keel-inject 补丁  /  GATE 回执由 gate --record 写入⟩
