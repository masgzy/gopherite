<!-- 关联 Issue: Closes #123 / Fixes #456 -->

## 变更说明

<!-- 简述这次 PR 做了什么、为什么这么做 -->

## 变更类型

- [ ] feat（新能力，对应 Roadmap 里程碑）
- [ ] fix（错误修复）
- [ ] perf（性能优化，附基准数据）
- [ ] refactor（不改变外部行为的重构）
- [ ] test（补充测试）
- [ ] docs / ci / chore

## 自查清单

- [ ] `gofmt -l .` 无输出
- [ ] `go vet ./...` 通过
- [ ] `go test -race ./...` 全绿
- [ ] 协议行为改动附验证依据（反编译对照 / wiki 链接 / 抓包）
- [ ] 涉及用户可见行为时已更新 README / 配置默认值说明
- [ ] commit message 符合 Conventional Commits

## 性能影响（如适用）

<!-- 热路径改动请附 go test -bench 前后对比 -->
