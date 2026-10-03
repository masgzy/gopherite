<div align="center">

# ⛏️ Gopherite

**用 Go 编写的 Minecraft: Java Edition 26.2 原版服务端**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg?logo=go&logoColor=white)](https://go.dev)
[![Minecraft](https://img.shields.io/badge/Minecraft-26.2-62BF8B.svg)](https://minecraft.wiki)
[![Protocol](https://img.shields.io/badge/Protocol-776-orange.svg)](https://minecraft.wiki/w/Protocol_version)
[![CI](https://img.shields.io/badge/CI-GitHub_Actions-2088FF.svg?logo=githubactions&logoColor=white)](.github/workflows/ci.yml)
[![English](https://img.shields.io/badge/Docs-English-gray.svg)](README.EN.md)

*库 + 可执行分离 · 资源全内嵌 · 单二进制发布 · 高性能设计*

</div>

---

Gopherite 是一个**从零开始、以 Go 语言实现**的 Minecraft Java Edition 原版（vanilla）服务端，目标版本锁定 **26.2（协议号 776）**。项目不基于任何 JVM 代码，不依赖 Java 运行时——编译产物是一个 **3~4MB 的静态单二进制文件**，下载即可开服。

> 项目动机与调研结论：目前 Go 生态中不存在可用于现代 Java 版（1.21.4+ / 26.x）的服务端实现。最接近的先行者 [Zeppelin](https://github.com/ZeppelinMC/Zeppelin)（目标 1.21.3）已停更约两年。同时 Mojang 已停止对代码进行混淆，官方 jar 反编译后即可获得接近源码质量的参考实现，这使得行为级复刻的可行性大幅提高。

## ✨ 特性

### 已实现（M0 / M1 / M2）

- **单二进制，资源全内嵌**：默认配置、EULA 模板等通过 `go:embed` 编译进二进制，启动时零解压；运行时只产出必须持久化的文件（`eula.txt`、`server.properties`、后续的 `world/`）
- **Vanilla 兼容的 EULA 门禁**：首次启动生成 `eula.txt` 并拒绝启动，直至确认接受 Minecraft EULA；支持 `--accept-eula` 直通，文件格式与原版一致，现有面板/脚本无缝兼容
- **协议 776 网络层**：VarInt / VarLong / 分帧 / 包缓冲区，全链路零拷贝、热路径零分配设计
- **服务器列表 Ping 完整可用**：握手（Intent=Status）→ 状态响应（版本 / 协议号 / MOTD / 人数上限 / 图标）→ Ping/Pong，原版客户端服务器列表中可见并显示延迟
- **完整登录流程（离线 + 正版双模）**：`online-mode=true` 时执行 RSA-1024 密钥交换、AES/CFB8 加密与 Mojang 会话服务器校验；`false` 时按原版规则派生离线 UUID——两条路径均经协议级端到端测试验证
- **Zlib 压缩层**：与原版一致的压缩/加密分层（压缩在加密之内、外层长度明文），阈值可配
- **超平坦世界 + 进入世界行走**：配置阶段注册表同步（29 项动态注册表）→ 进入 play → 超平坦区块下发（高度图 / 区块节 / 天空光照）→ 传送确认、移动包处理与 keep-alive 心跳
- **多版本协议层预留**：包常量按版本子包组织（`protocol/java/v776`），为后续版本协商与多版本支持留出结构位

### 规划中

见 [Roadmap](#-roadmap)。

## 📦 快速开始

### 从源码构建

```bash
git clone https://github.com/masgzy/gopherite.git
cd gopherite
make build          # 产出 dist/gopherite（单二进制）
```

或使用 Go 工具链：

```bash
go build -trimpath -ldflags "-s -w" -o gopherite ./cmd/gopherite
```

### 启动

```bash
./gopherite                      # 首次启动：生成 eula.txt 与 server.properties 后退出
./gopherite --accept-eula        # 接受 EULA 并启动（或手动把 eula.txt 中 eula 改为 true）
```

启动后用原版 26.2 客户端添加服务器 `127.0.0.1`，即可在服务器列表看到 Gopherite。

> **EULA 说明**：运行 Minecraft 服务器即表示你需要同意 [Mojang EULA](https://aka.ms/MinecraftEULA)。Gopherite 与原版一致地强制这一步。本项目与 Mojang Studios / Microsoft 无任何关联。

### 命令行参数

| 参数 | 说明 |
|---|---|
| `--config <path>` | 配置文件路径（默认 `server.properties`） |
| `--dir <path>` | 工作目录（存放 `eula.txt`、后续的世界数据） |
| `--port <port>` | 覆盖配置文件中的 `server-port` |
| `--accept-eula` | 接受 EULA 并写入 `eula.txt` |
| `--version` | 打印版本信息 |

## ⚙️ 配置

配置沿用 vanilla `server.properties` 风格（`key=value`、`#` 注释）。Gopherite 会忽略不认识的键，因此原版配置文件不会被破坏。

| 键 | 默认值 | 说明 |
|---|---|---|
| `server-port` | `25565` | 监听端口 |
| `server-ip` | （空） | 绑定地址，空为全部接口 |
| `motd` | `A Gopherite Server` | 服务器列表公告 |
| `max-players` | `20` | 人数上限 |
| `online-mode` | `true` | 正版验证开关（M2 已生效） |
| `network-compression-threshold` | `256` | 压缩阈值（M2 已生效） |
| `server-icon` | （空） | 64×64 PNG 图标路径 |
| `read-timeout` | `30` | 握手/状态阶段读超时（秒） |
| `write-timeout` | `10` | 单包写超时（秒）；防止慢客户端阻塞广播 |

## 🏗️ 架构

```
gopherite/
├── cmd/gopherite/       # 瘦入口：参数解析、EULA 门禁、装配与信号处理
├── config/              # server.properties 解析 + 内嵌默认配置
├── eula/                # vanilla 兼容 EULA 状态机
├── protocol/            # 版本无关协议原语（VarInt、缓冲区、分帧）
│   └── java/            # Java 版通用层（握手、状态、意图）
│       └── v776/        # 26.2 专属常量（多版本预留位）
└── server/              # 服务端内核（连接状态机、状态 Ping、选项）
```

**设计原则**

1. **库 + 可执行分离**：`server`、`protocol` 等包是可被第三方 import 的库；`cmd/gopherite` 只是薄薄一层装配。未来 Paper/Purpur 补丁可以以"替换 / 扩展库组件"的形式叠加，发布形态始终是单二进制。
2. **资源全内嵌**：默认资产编译进二进制（`go:embed`），运行时只写必须持久化的数据——存档（`world/`，M4 起采用 Anvil 兼容格式，可用原版客户端直接读取）、日志与操作员编辑的配置。
3. **高性能优先**：热路径（VarInt 编解码、分帧、缓冲区管理）零分配；`Reader` 对包负载做零拷贝视图、仅 `String` 拷贝（因为底层缓冲区会被池化复用）；后续里程碑将引入缓冲区池化、批量区块编码与分片世界 ticking。
4. **确定性优先于覆盖面**：每个里程碑的协议行为都对照无混淆的 26.2 官方 jar 与社区协议文档交叉验证，宁可慢也要对。

## 🚀 Roadmap

- [x] **M0** 工程骨架、社区文件、CI
- [x] **M1** 网络层 + 服务器列表 Ping
- [x] **M2** 登录流程（离线 + 正版双模、加密、压缩）+ 超平坦区块 + 进入世界行走
- [x] **M3** 方块破坏/放置、区块管理与简化光照
- [x] **M4** 完整 NBT + Anvil 存档读写（世界持久化）
- [x] **M5** 实体系统（物理、基础 AI）
- [x] **M6** 注册表数据管线：从原版 jar 提取 blocks/items/recipes 自动生成 Go 代码
- [x] **M7** 背包 / 合成 / 命令系统（Brigadier 移植）/ 基础红石
- [x] **M8** 生存基础（生命 / 饥饿 / 进食 / 摔落与虚空伤害 / 死亡重生 / `/kill`）+ 被动生物（猪 / 牛 / 羊 / 鸡：游走 AI、受击逃跑、击退、死亡掉落）
- [x] **M8.5** 多人可见性（玩家互视 / 标签页同步）+ 经验球 + 生物摔落伤害 + 兽群补充
- [x] **M9** 敌对生物（僵尸 / 骷髅 / 爬行者：追击 AI、近战 / 射箭 / 引信爆炸、白天燃烧、夜间生成）+ 昼夜循环（26.2 WorldClock）+ 实体音效
- [x] **M10** 容器方块（箱子 27 格 + 熔炉：燃料 / 熔炼 / 燃烧与烹饪进度 / 熄火回退、经验结算、破坏与爆炸掉落内容、Anvil 原版兼容持久化）；修复 M7 容器点击从未接线的断流 bug 与窗口 ID 冲突
- [x] **M11** 战斗与防御：盔甲减伤（26.2 官方护甲数值与 `bypasses_armor` 标签、原版减伤公式、钻石/下界合金韧性）、玩家弓箭（拉弓蓄力、原版威力曲线、命中生物与其他玩家、落地箭矢可拾取、生存消耗箭矢）、近战强化（按武器攻速冷却、下落暴击 1.5× 与暴击动画、PvP 近战与击退、26.2 全武器伤害表含铜质系列与重新平衡的斧类）、挥手动画广播
- [x] **M12** 世界动态与智能 GC：天气循环（晴/雨/雷暴状态机 + 26.2 Game Event 同步 + join 天气快照）、雷暴落雷（闪电实体、雷击伤害与音效、地表起火）、TNT（打火石点燃、引信实体、原版量级爆炸、弹坑连锁点燃，与爬行者共用通用爆炸路径）、床（双格放置、夜晚睡觉跳过昼夜、设置床重生点、破坏联动）、`/gc` 观测命令、智能 GC 调控器（runtime/metrics 无锁采样 GC CPU 占比 / 分配速率 / 暂停 P99 / 堆水位，阈值分档 + 步进 + 冷却防震荡动态调节 GOGC，GOMEMLIMIT 自动探测 cgroup/系统内存 ×90% 防 OOM）
- [x] **M13** 状态效果与药水：26.2 全 40 效果注册表（含新增 `breath_of_the_nautilus`）、效果引擎（原版升级/刷新规则、再生/中毒/凋零/饥饿按原版周期结算、瞬间治疗/伤害与亡灵反转）、战斗生存接线（力量/虚弱、抗性减伤、吸收心、火焰免疫、急迫/挖掘疲劳、跳跃提升/缓慢下落、速度/缓慢生物移速、隐身/发光标志同步）、`/effect give|clear`、金苹果/附魔金苹果/腐肉/蜘蛛眼/河豚/奶桶食用效果、药水 `potion_contents` 组件全链路（背包/掉落/拾取/Anvil NBT）、饮用（返还玻璃瓶）、喷溅/滞留药水投掷（原版距离折扣）、酿造台（400t/烈焰粉 20 次、原版配方全表含 start-mix、`has_bottle_*` 方块状态、进度数据槽、持久化）
- [x] **M14** 药水完整化（不做简化）：真实 **区域效果云实体**（`area_effect_cloud`，26.2 反编译语义全量移植：半径 3.0/时长 600t/等待 10t/每次施加半径 −0.5/每 tick 收缩 −3/600、victims 免疫窗 20t、每 5t 扫描云内实体、瞬时效果按 0.5 效力结算且亡灵反转、半径耗尽消散），元数据三段同步（半径/等待/`entity_effect` 粒子色），药水颜色表（40 效果按 (amp+1) 加权平均 = `PotionContents.getColor`）——**滞留药水落点生成效果云、持续施加完整时长效果，不再是一次性 0.25 折扣**；**药水箭**（发射保留 `potion_contents`、满蓄力暴击标志、命中施加完整时长效果、插墙 600t 褪色回普通箭、拾取还原药水箭、组件不与普通箭混叠）；修复投掷药水客户端渲染在原点的缺陷（补药水实体出生包与物品栈元数据）；背包/掉落/死亡掉落全链路携带 `potion_contents` 组件（此前热栏同步丢失药水组件）
- [x] **M15** 投掷物家族与粒子系统（26.2 反编译逐条核验）：通用投掷物实体（`ThrowableItemProjectile` 弹道移植：重力 0.03 / 空气阻力 0.99、`getMovementToShoot` 归一化→`triangle(0, 0.0172275)` 三角散布→威力 1.5、投掷音效音调 `0.4/(rand·0.4+0.8)`、消耗仅生存生效且修正创造可掷药水）、**雪球**（命中 `hurt(thrown, 0/3)`：0 伤害仍有受击动画+击退+音效，实体事件 3 触发客户端破碎粒子）、**鸡蛋**（1/8 孵雏鸡、其中 1/32 一次 4 只、幼年 `age −24000` 逐 tick 成长回成年并同步 baby 标志位）、**末影珍珠**（32 颗 `portal` 粒子逐颗下发、主人传送至 `oldPosition`、摔落清零、固定 5 点 `ender_pearl` 伤害、`entity.player.teleport` 音效、主人死亡即消散 = `ENDER_PEARLS_VANISH_ON_DEATH` 默认）、`level_particles` 包实现（26.2 字段序：count 为 int32 大端）；修复 M13 投掷药水创造模式既不能掷又要扣件的矛盾
- [x] **M16** 计分板/队伍与世界边界（26.2 反编译逐条核验）：目标（dummy/trigger/deathCount 等全套判据、只读判据拒绝写入、deathCount/playerKillCount/totalKillCount 死亡钩子自动计分）、队伍（颜色/前后缀/友伤/隐友可见/名字与碰撞规则，参数块按 `Parameters.STREAM_CODEC` 字段序）、19 个显示槽位（list/sidebar/below_name + 16 团队色）与 `ServerScoreboard` 追踪语义（未显示不广播、进出显示槽自动启停追踪、join 快照 `updateEntireScoreboard` 等价序列）、世界边界（static/moving 双 extent、lerp 客户端插值、`isWithinBounds`/`getDistanceToBorder`/越界伤害 `max(1, floor(-dist·damagePerBlock))` 与安全区缓冲、界内出不去界外可走回的钳制语义、`world_border.dat`/`scoreboard.dat` NBT 存档）；**接线修复**：`handleCommand` 自 M3 起从未挂进 Brigadier 树（所有指令回"未知命令"），补齐 signed 指令包（0x08，按 26.2 字段序消费签名与 lastseen）与 Tab 补全请求/应答（0x0F 双向）
- [x] **M17** 进度系统（26.2 反编译逐条核验）：内嵌原版 26.2 全量进度定义（126 条主树 JSON 提取自官方 datapack，`go:embed` 启动即载）、进度树布局算法逐行移植（`TreeNodePosition` Reingold–Tilford 三段走查：firstWalk/apportion/executeShifts/secondWalk/thirdWalk，坐标写回 display x/y 客户端按原版网格渲染）、可见性规则移植（`AdvancementVisibilityEvaluator`：完成节点与两代祖先窗内的 SHOW/HIDE 传播——26.2 根进度是真实进度（story/root=合成工作台、husbandry/root=任意进食），全新玩家收不到任何进度、窗口逐级展开与原版一致）、进度包 0x82/0x55/0x32 三向（Update Advancements 按 `AdvancementHolder`/`DisplayInfo`/`AdvancementRequirements`/`AdvancementProgress` 26.2 字段序：translate 组件 NBT、图标 `ItemStackTemplate`（26.1+ 形态：注册表原始 id 提到 count 之前）、frame 枚举、flags 位掩码、背景图、布局坐标、需求组、毫秒判据时间戳）、触发器引擎（tick/inventory_changed/player_killed_entity/killed_by_arrow/entity_killed_player/consume_item/placed_block/effects_changed/levitation/slept_in_bed/fall_from_height/player_hurt_entity/brewed_potion 判据条件求值：实体类型与 `#tag`、物品谓词与数量、伤害来源 `is_projectile` 标签、效果表、距离轴、生物群系与坐标范围；其余判据按"功能缺失即永久未完成"的原版语义静默挂起）、`advancements/<uuid>.json` 原版兼容存档（.criteria 时间戳 "yyyy-MM-dd HH:mm:ss Z" 格式、done 标志、登出与关停双落盘）、完成奖励（经验按原版等级曲线 `2L+7/5L-38/9L-158` 入账、toast 由客户端随进度同步自动弹出、聊天公告 `chat.type.advancement.<frame>` 带 hover 提示的方括号着色组件全量 NBT 编码）、`/advancement grant|revoke <目标> everything|from|through|until|only [判据]` 全套遍历语义；**顺带修复**：M10 燃料表 `tagMembers("#…")` 前缀不匹配导致原木/木板类燃料时长从未生效的潜在 bug、实体推进与同步在测试手动驱动下与 ticker 并发的无锁窗口（`entityTickMu`）、M16 死亡钩子测试无锁读 `scores` 的竞态
- [x] **M17.5** 网络协议全面体检（只修 bug 不加功能）：修复 `Declare Commands` 根节点被误标为 argument 类型（M7b 起存活 10 个里程碑的真实客户端崩溃根源——客户端 `getRoot()` 强转 `RootCommandNode` 抛 `ClassCastException`，即"网络协议错误，进不了服务器"）并在回归测试中断言"全树恰有一个类型 0 节点且其索引=根索引"；修复正版模式加密请求发错包号（客户端方向 0x00 是 Login Disconnect，应为 `PacketLoginHello` 0x01）；修复物品栈 wire 全面 off-by-one（26.2 Slot 与 26.1+ `ItemStackTemplate` 均用注册表**原始 id**，旧的 "holder id+1" 使背包/掉落物/进度图标全部错位一格——现行 wiki Slot data 与 Pumpkin 双源交叉验证）与 `DataComponentPatch` 计数顺序（新增/移除两个计数相邻在前、条目在后，旧交织写法会被客户端把组件类型读成移除计数）；status 阶段回完 pong 的正常关闭不再误报"会话异常"；连接关停先置写管线 dead 再关套接字、`net.ErrClosed` 静默收尾，不再误报"写入失败: use of closed network connection"；修复进度 flush 测试与 `net.Pipe` 拷贝协程的约 7% 假阴性竞态；其余协议层（VarInt 边界/帧长度/压缩炸弹上限/CFB8/AuthDigest/离线 UUID v3/登录配置状态机/join 序列/写超时）逐文件复核无恙
- [ ] **P**  Paper / Purpur 行为差异复刻

## 🧑‍💻 开发

```bash
make test      # go test ./...
make vet       # go vet ./...
make fmt       # gofmt 全仓库格式化
make build     # dist/gopherite
```

### 注册表数据管线（M6）

`server/blocks_gen.go`、`items_gen.go`、`recipes_gen.go` 由脚本从原版 26.2 数据生成，升级版本时重新执行一遍即可：

```bash
# 1) 从原版 server.jar 提取配方/item tag 并生成数据报告（需 JDK 21+）
python3 scripts/extract_vanilla_data.py --jar server.jar --out vanilla

# 2) 重新生成三张 Go 表（blocks / items / recipes）并 gofmt
make gen VANILLA=vanilla
```

- 生成器按确定性顺序输出（块名字典序、配方文件名排序），提交前 diff 无噪声；
- item→方块放置映射由名称匹配 + 少量人工校验例外表（`gen_items.py` 的 `ADD_BLOCK`/`DEL_BLOCK`）得出，升级版本后建议抽查；
- `recipes_gen.go` 在生成期递归解析 item tag，配料表达式为 `minecraft:物品`、`#minecraft:tag` 或 `|` 连接的备选项。

要求 Go 1.27+（推荐用 [g](https://github.com/voidint/g) 安装）。欢迎通过 Issue / PR 参与，请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 🤝 贡献

欢迎任何形式的贡献：协议验证、性能基准、bug 复现、文档翻译。提交 PR 前请确保 `make test`、`make vet` 与 `gofmt` 全部通过。

## 🛡️ 安全

发现安全漏洞请通过 [SECURITY.md](SECURITY.md) 的渠道私下报告，请勿直接公开 Issue。

## 📄 许可证

本项目以 [MIT License](LICENSE) 开源。

*Minecraft 是 Mojang Studios 的商标。Gopherite 与 Mojang Studios / Microsoft 无关，亦未获得其背书。*

## 🙏 致谢

- [ZeppelinMC/Zeppelin](https://github.com/ZeppelinMC/Zeppelin)（Apache-2.0）—— Go 原版服务端的先行者与架构参考
- [Tnze/go-mc](https://github.com/Tnze/go-mc)（MIT）—— Go 生态 MC 协议库
- [df-mc/dragonfly](https://github.com/df-mc/dragonfly)（MIT）—— Go Bedrock 服务端，工程实践范本
- [minekube/gate](https://github.com/minekube/gate)（Apache-2.0）—— 活跃的 Java 协议实现参考
- [minecraft.wiki](https://minecraft.wiki/w/Protocol) —— 协议文档
- Minecraft 社区中所有为开放实现铺路的探索者
