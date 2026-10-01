# 贡献指南 (Contributing to Gopherite)

首先，感谢你考虑为 Gopherite 贡献代码！这是一个从零开始的重写项目，每一行协议代码、每一个基准测试、每一处文档勘误都非常有价值。

## 行为准则

参与本项目即表示你同意遵守 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。

## 开发环境

| 要求 | 版本 | 备注 |
|---|---|---|
| Go | 1.27+ | 推荐用 [g](https://github.com/voidint/g) 安装：`g install 1.27.1` |
| git | 任意近期版本 | |
| make | 可选 | 不用 make 也可直接执行下述 go 命令 |

```bash
git clone https://github.com/masgzy/gopherite.git
cd gopherite
make test   # 或: go test ./...
make vet    # 或: go vet ./...
make fmt    # 或: gofmt -w .
make build  # 或: go build -o dist/gopherite ./cmd/gopherite
```

## 如何选择任务

1. 浏览 [Roadmap](README.md#-roadmap)，未勾选的里程碑按顺序推进，**当前里程碑优先**
2. 查看 [`good first issue`](https://github.com/masgzy/gopherite/labels/good%20first%20issue) 与 [`help wanted`](https://github.com/masgzy/gopherite/labels/help%20wanted) 标签
3. 协议行为类改动（包结构、字段、时序）请先开 Issue 讨论，附上你验证行为所用的依据（反编译代码片段 / wiki 链接 / 客户端抓包）

## 提交规范

采用 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/)：

```
<type>(<scope>): <简短描述>

<可选正文>
```

常用 type：`feat` / `fix` / `perf` / `refactor` / `test` / `docs` / `ci` / `chore`

示例：

```
feat(protocol): add zlib compression framing
perf(server): pool packet writer buffers
fix(eula): regenerate eula.txt when malformed
```

## PR 流程

1. Fork 仓库，从 `main` 切出功能分支（如 `feat/login-flow`）
2. 开发并补充/更新对应测试——**协议层的改动必须带测试**
3. 确保以下全部通过：
   ```bash
   gofmt -l .          # 无输出
   go vet ./...        # 无报错
   go test ./...       # 全绿
   ```
4. 填写 PR 模板，关联相关 Issue（`Closes #123`）
5. 等待 Review，按意见修改后 rebase

## 代码风格

- 遵循 [Effective Go](https://go.dev/doc/effective_go) 与 [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- 公共 API 必须有 doc comment；导出符号命名不加 stutter（`java.Handshake` 而非 `java.JavaHandshake`）
- 热路径（每包/每方块执行）禁止堆分配；请用调用方传入的缓冲区或包级池
- 错误处理用 `%w` 包装并携带上下文；不要吞错
- 禁止引入第三方依赖，除非与维护者达成一致（当前 zero-dependency 是设计目标）

## 协议验证方法

实现某个包/机制前，推荐交叉验证三处来源：

1. **官方 jar**：Mojang 已停止混淆，`versions/26.2/server-26.2.jar` 内为可读类名，用 [Vineflower](https://github.com/Vineflower/vineflower) 反编译对应类
2. **minecraft.wiki 协议文档**：<https://minecraft.wiki/w/Protocol>
3. **真实客户端抓包**：本地代理对比字节流

## 报告 Bug

请使用 [Bug Report 模板](.github/ISSUE_TEMPLATE/bug_report.yml)，附上 Gopherite 版本、客户端版本、复现步骤与日志。安全问题请走 [SECURITY.md](SECURITY.md) 私密渠道。

## 许可

提交即表示你同意代码以 [MIT License](LICENSE) 随项目分发。
