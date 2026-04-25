# GoSilver-Core

### 目标：打造一款快速地局域网内文件同传分发工具

本项目寄希望于模仿 BT-P2P 模式，在局域网内打造高可用、高性能的大文件分发同传工具。

**应用场景：**
- 需要从一台电脑快速分发文件到其他的多台电脑
- 例如：学校的机房分发软件、会议分发文件等

---

## 愿景

传统的局域网文件传输（如 SMB、FTP）都是点对点单线程传输，当需要将一个大文件分发给多台机器时，源机器会成为带宽瓶颈。

GoSilver-Core 的愿景是：**在局域网中，只要有一台机器拥有完整的文件，其他所有机器都能以最优化的方式快速获取这份文件**。当越来越多的机器完成下载后，它们会变成新的"种子节点"，共同承担分发任务，从而实现真正的 P2P 分布式文件分发。

---

## 核心特性

- **P2P 分发**：下载完成的节点自动成为新的提供者，分担源节点压力
- **多线程并发下载**：支持多个 chunk 同时下载，加速传输
- **连接池复用**：TCP 连接池减少连接建立开销
- **内存池复用**：8MB chunk 缓冲区池化，避免频繁内存分配
- **Checksum 校验**：每个 chunk 使用 CRC32 校验，确保数据完整性
- **GSP 自定义协议**：轻量级二进制协议，5 字节头部 + payload，简洁高效

---

## 架构

```
                           ┌──────────────────────────────────────────────────┐
                           │                    main.go                      │
                           │              程序入口，支持两种模式               │
                           └──────────────────────┬───────────────────────────┘
                                                  │
                              ┌───────────────────┴───────────────────┐
                              │                                        │
                              ▼                                        ▼
                    ┌─────────────────┐                    ┌─────────────────┐
                    │   Sender 模式    │                    │  Receiver 模式   │
                    │  internal/sender │                    │ internal/receiver│
                    └────────┬─────────┘                    └────────┬────────┘
                             │                                        │
                             ▼                                        ▼
                    ┌─────────────────┐                    ┌─────────────────┐
                    │     Session     │                    │     GspSdk      │
                    │  session.go     │                    │    sdk.go       │
                    │  • TCP 监听     │                    │  • GetFileStatus│
                    │  • 管理 chunk   │                    │  • WantChunk    │
                    │  • 跟踪对等方   │                    │  • GetChunk     │
                    └────────┬────────┘                    │  • ReportChunk  │
                             │                             └────────┬────────┘
                             │                                       │
                             ▼                                       │ TCP
                    ┌─────────────────-┐                             │
                    │   Handle 处理    │                             │
                    │ handle/sender.go │                             │
                    │  • GetFileStatus │                             │
                    │  • WantChunk     │                             │
                    │  • ReportChunk   │                             │
                    │  • GetChunk      │                             │
                    └────────┬─────────┘                             │
                             │                                       │
                             ▼                                       ▼
                    ┌─────────────────┐                    ┌─────────────────┐
                    │  Chunk 系统      │                    │   ConnPool      │
                    │  internal/chunk  │                    │ internal/conn_pool│
                    │  • 分块读写      │                    │  • 连接池复用    │
                    │  • 8MB/chunk    │                    │  • 每地址 3 连接 │
                    └─────────────────┘                    └─────────────────┘
                             │
                             ▼
                    ┌─────────────────┐
                    │  Memory Pool     │
                    │   pkg/mempool    │
                    │  • 8MB 缓冲池化  │
                    └─────────────────┘
```

---

## 项目结构

```
go-silver-core/
├── cmd/
│   └── main.go                 # 程序入口
├── internal/
│   ├── chunk/                  # 文件分块系统
│   │   ├── file.go             # FileChunk 结构体
│   │   ├── read.go             # ReadChunk - 读取指定块
│   │   └── write.go            # Save - 写入数据到块
│   ├── conn_pool/              # TCP 连接池
│   │   └── pool.go             # ConnPool 实现
│   ├── const/
│   │   └── const.go            # 常量定义（ChunkSize = 8MB）
│   ├── gsp/                    # GSP 传输协议
│   │   ├── codec.go            # 封包/解包（5字节头 + payload）
│   │   ├── error.go            # 错误定义
│   │   └── protocol.go         # 协议常量（TypeJSON, TypeFileChunk）
│   ├── gsp_sdk/                # 核心 SDK
│   │   ├── handle/
│   │   │   └── sender.go       # Sender 端请求处理器
│   │   ├── model/
│   │   │   └── type.go         # JSON 消息结构体
│   │   ├── operation.go        # 操作路由分发
│   │   ├── sdk.go              # GspSdk（Receiver 端 API）
│   │   └── session.go          # Session（Sender 端状态管理）
│   ├── receiver/               # Receiver 模式入口
│   │   └── receiver.go         # 下载协调逻辑
│   └── sender/                  # Sender 模式入口
│       └── sender.go            # 发送服务启动
├── pkg/
│   ├── mempool/                # 内存池
│   │   └── mempool.go          # sync.Pool 实现
│   └── queue/                  # 下载队列
│       ├── queue.go            # DownloadQueue 接口
│       └── README.md
└── test/                       # 测试文件
```

---

## GSP 协议

### 数据包结构

```
+--------+--------+--------+--------+--------+----------------+
|  Type  |         Length (uint32, little-endian)          | Payload...
| (1B)   |                                           | (Length bytes)
+--------+--------+--------+--------+--------+--------+
```

- **Type 0x01 (TypeJSON)**：控制消息，Payload 为 JSON 格式
- **Type 0x02 (TypeFileChunk)**：文件块数据，Payload 为原始二进制

### JSON 消息类型

| 操作 | 方向 | 说明 |
|------|------|------|
| `getFileStatus` | Receiver → Sender | 请求文件状态（名称、大小、chunk 数） |
| `getChunk` | Receiver → Sender | 请求指定 chunk 数据 |
| `wantChunk` | Receiver → Sender | 查询谁拥有指定 chunk |
| `reportChunk` | Receiver → Sender | 报告自己已拥有某个 chunk |

---

## 工作流程

### Sender 模式（源节点）

1. 启动 TCP 监听（默认端口 48080）
2. 打开文件，按 8MB 分块
3. 等待 Receiver 连接
4. 处理 `getFileStatus`、`wantChunk`、`getChunk`、`reportChunk` 请求
5. 记录哪个对等方拥有哪个 chunk（`ChunkBlockOwner`）

### Receiver 模式（下载节点）

1. 连接 Sender 获取文件状态
2. 创建本地文件，按 chunk 数预分配空间
3. 启动 goroutine 池（默认 5 个并发）：
   - 通过 `WantChunk(i)` 询问谁有 chunk `i`
   - 从返回的地址通过 `GetChunk()` 下载 chunk
   - 使用 CRC32 校验数据完整性
   - 调用 `ReportChunk()` 告知自己已拥有该 chunk
4. 下载完成的节点同时成为新的 Sender，可为其他节点提供 chunk

### P2P 分发示例

```
第 1 步：Sender 拥有完整文件，监听端口 48080
第 2 步：Receiver A 连接 Sender，开始下载 chunk 1, 2, 3...
第 3 步：Receiver A 下载完 chunk 2 后，调用 ReportChunk 报告"我有 chunk 2 了"
第 4 步：Receiver B 连接 Sender，想要 chunk 2
第 5 步：Sender 返回地址 192.168.1.8:48081（Receiver A），让 B 去 A 那下载
第 6 步：Receiver B 从 Receiver A 获取 chunk 2，分担 Sender 压力
```

---

## 使用方法

```bash
# 作为 Sender（源节点，拥有文件的那台机器）
go run cmd/main.go -mode=sender -file=/path/to/largefile.apk

# 作为 Receiver（需要下载文件的机器）
go run cmd/main.go -mode=receiver -senderAddr=192.168.1.10:48080
```

---

## 技术细节

| 项目 | 值 |
|------|-----|
| 分块大小 | 8MB |
| 单地址最大连接数 | 3 |
| 并发下载数 | 5（可配置） |
| 默认 Sender 端口 | 48080 |
| 默认 Receiver 端口 | 48081 |
| 校验算法 | CRC32 |
| 协议类型 | TCP 二进制 |