# ft-server 应用层 I/O 限速设计

- 日期：2026-11-16
- 状态：设计已确认，待评审
- 范围：仅 `ft-server` 应用层限速；RustFS 容器的 cgroup/blkio 限制和 md/内核调优由运维侧另行处理

## 1. 背景

RustFS 的本地存储是 md RAID6 机械盘，I/O 性能有限。当前 `ft-server` 对上传、下载和分片上传不做任何限速，5～20 个客户端并发时会打满磁盘，连带影响 RustFS 元数据操作、健康检查和 SSH 登录。

同时，README 允许用户和摆渡操作员通过 RustFS Console / Cyberduck 直连 RustFS，这部分流量不经过 `ft-server`，应用层限速覆盖不到。

## 2. 已确认决策

| 决策 | 结论 |
|---|---|
| 限速范围 | 仅应用层；系统层 Docker/cgroup 和 md 调优由运维侧处理 |
| 配置模型 | 简化 8 项：全局/单用户并发 + 四个 bps + 排队超时 + 总开关 |
| 带宽默认值 | `bps = 0`（不限），基线测量后再填 |
| 带宽口径 | 应用层 bps 是文件内容字节，不负责块设备物理字节 |
| 客户端改造 | 接受修改 `ft-client`，处理 429/503 重试 |
| 部署背景 | RustFS 使用 md 软 RAID6，作为预算测量的背景信息 |
| 设计文档位置 | `doc/specs/` |

## 3. 目标与非目标

### 3.1 目标

- `ft-client` 的上传、下载和分片上传不超过配置的并发和带宽预算。
- 5～20 个并发客户端公平分享预算，单个用户不能长期占满。
- 超过排队时间返回 429，客户端能自动退避重试。
- 限速行为可观测、可配置、可回滚，不改变文件语义和断点续传能力。

### 3.2 非目标

- 不配置 RustFS 容器的 cgroup/blkio，不调 md/内核参数；这些由运维侧负责。
- 不限制 Console / Cyberduck 直连流量；这部分由系统层兜底。
- 不做网络层整形（`tc`）。
- 不引入分布式限速；当前是单机单实例部署。
- 不改变 S3 桶结构、权限模型和 API 语义。

## 4. 现状与关键事实

### 4.1 数据路径

| 路径 | 是否经过 ft-server | 能否被应用层限速 |
|---|---|---|
| `ft-client` 下载 | 是 | 能 |
| `ft-client` 上传 | 是 | 能 |
| `ft-client` 分片上传 | 是 | 能 |
| Console / Cyberduck 直连 | 否 | 不能，只能靠 cgroup |
| `ft-admin clean` | 是 | 能，需要单独限速 |
| RustFS 后台任务 | 否 | 不能，只能错峰 |

### 4.2 代码事实

- `ft-server` 下载走 `io.Copy(w, obj)`，上传把 `c.Request.Body` 交给 minio-go `PutObject`。
- minio-go v7.4.1 对非 `io.ReaderAt` 的请求体走顺序分片上传，`ft-server` 内部不会自动产生多线程上传。
- `ft-client` 当前逐片串行上传，天然友好；并发压力来自客户端数量。
- 分片大小由服务端 `multipart.tiers` 决定，默认 16MiB～128MiB。
- `ft-admin clean` 通过 minio-go 批量删除对象，可能产生 I/O 峰值。

### 4.3 HDD + RAID6 的性能特征

- **并发流数量比总带宽更致命**：多条流同时读写会让磁头反复寻道，聚合吞吐可能远低于单流顺序吞吐。
- **RAID6 随机写惩罚约 6 倍**：非整条带写需要读旧数据、读旧校验、写新数据、写两个校验。
- **md 没有电池保护写缓存**：写性能受磁盘和 md stripe cache 限制，`fsync` 延迟直接影响小文件写入。
- **对象层 IOPS 低于裸盘**：RustFS 的 PUT 包含元数据、临时文件、rename、可能的 `fsync`，预算必须用 S3 层压测校准。

## 5. 总体架构

```text
ft-client ──HTTP──▶ ft-server ──S3(loopback)──▶ RustFS ──▶ md RAID6
                      │
                      ├─ 全局/单用户并发闸门
                      ├─ 每用户 + 全局令牌桶
                      └─ 429 + Retry-After

Console/Cyberduck ─────────S3──────────────────▶ RustFS（不在本设计范围）
```

| 层 | 作用 | 本设计范围 |
|---|---|---|
| 应用层 limiter | 精细、公平、可观测 | 是 |
| cgroup `io.max` | 系统层硬上限 | 否，运维侧处理 |
| OS / md 调优 | 提高拐点、降低毛刺 | 否，运维侧处理 |

## 6. 限速模型

### 6.1 并发闸门（第一优先级）

使用两个独立的计数器，避免大文件传输饿死元数据：

| 限制 | 默认 | 获取者 |
|---|---:|---|
| `max_concurrent` | 4 | 普通上传、下载、分片上传、Complete，每个请求占 1 槽 |
| `max_concurrent_per_user` | 1 | 同一用户的在途传输数 |
| 元数据并发（内部固定） | 8 | List、Stat、Multipart Init/Parts/Abort |

- 每个上传、下载、分片上传、Complete 都占 1 个传输槽，不区分上传/下载权重。
- 先检查单用户并发数，再检查全局槽位；任一超限则排队。
- 元数据使用独立固定池，不占传输槽，避免浏览目录被大文件传输完全阻塞。
- clean 二期单独一个槽，避免批量删除和用户传输互相拖死。

选择理由：在 HDD 上，限制“同时在途的流数量”比限制“总 MB/s”更能保护寻道行为。简化权重后配置更容易理解和运维。

### 6.2 两级令牌桶（第二优先级）

```text
请求/响应字节 ──▶ 每用户令牌桶 ──▶ 全局令牌桶 ──▶ 实际 I/O
```

- 先等待用户桶，再等待全局桶，避免用户桶等待期间白占全局配额。
- 每次最多读取 1MiB，按实际读到的字节数扣令牌，最多产生 1MiB 的读前缓冲。
- `bps = 0` 表示该维度不限速；全局读/写、单用户读/写四个值独立配置。
- 令牌桶 burst 设为 `max(1MiB, bps × 1s)`，避免小块读取造成微停顿。
- 这里的 bps 是 HTTP body / 对象内容字节，不是块设备物理 I/O 字节；换算见 10.3。

### 6.3 排队与拒绝

- 获取槽位最多等待 `queue_timeout`（默认 10s）。
- 超时返回 `429 Too Many Requests`，错误码 `server_busy`，并带 `Retry-After`（秒）。
- 客户端断开导致 context 取消时，直接释放槽位，不写错误响应。
- 元数据请求只排队，不扣字节令牌。
- 不在一个请求内嵌套获取两个池，避免死锁。

### 6.4 默认值

| 参数 | 默认值 | 说明 |
|---|---:|---|
| `enabled` | `true` | 总开关，回滚时置 `false` |
| `max_concurrent` | `4` | 全局同时传输数 |
| `max_concurrent_per_user` | `1` | 单用户同时传输数 |
| `global_read_bps` | `0` | 全局下载带宽，测量后填 |
| `global_write_bps` | `0` | 全局上传带宽，测量后填 |
| `per_user_read_bps` | `0` | 单用户下载带宽，测量后填 |
| `per_user_write_bps` | `0` | 单用户上传带宽，测量后填 |
| `queue_timeout` | `10s` | 排队上限 |

默认开启并发闸门、关闭字节限速，避免在没有基线时把系统限死。

## 7. 配置设计

### 7.1 配置文件

```toml
[limits]
  enabled                 = true
  max_concurrent          = 4
  max_concurrent_per_user = 1
  global_read_bps         = "0"
  global_write_bps        = "0"
  per_user_read_bps       = "0"
  per_user_write_bps      = "0"
  queue_timeout           = "10s"
```

### 7.2 校验规则

- `max_concurrent >= 1`。
- `max_concurrent_per_user >= 1` 且 `<= max_concurrent`。
- bps 为 `0` 或 `>= 64KiB`；拒绝负数和小到没有意义的限速。
- `queue_timeout` 在 `1s`～`60s` 之间。
- 元数据并发池固定为 8，不暴露配置。
- 解析沿用现有 `config.ParseSize`，支持 `KiB/MiB/GiB/TiB`。

### 7.3 配置结构

```go
type LimitsSection struct {
    Enabled              bool   `toml:"enabled"`
    MaxConcurrent        int    `toml:"max_concurrent"`
    MaxConcurrentPerUser int    `toml:"max_concurrent_per_user"`
    GlobalReadBPS        string `toml:"global_read_bps"`
    GlobalWriteBPS       string `toml:"global_write_bps"`
    PerUserReadBPS       string `toml:"per_user_read_bps"`
    PerUserWriteBPS      string `toml:"per_user_write_bps"`
    QueueTimeout         string `toml:"queue_timeout"`
}
```

解析后的运行时结构使用 `int64` 字节/秒和 `time.Duration`，与现有 `Runtime` 模式一致。

`LoadServer` 以 `DefaultServer()` 为基底解码，因此必须在 `DefaultServer()` 中显式设置 `Limits.Enabled = true`；否则布尔零值会让没有 `[limits]` 的旧配置默认关闭限速。其余字段的默认值也在 `DefaultServer()` 中给出。

## 8. 数据流

### 8.1 下载

1. 中间件完成用户认证，设置 `username`。
2. 获取传输槽（全局 1 槽 + 该用户 1 槽），最多等待 `queue_timeout`；失败返回 429。
3. 在同一个槽内完成 `Stat` 和 `Get`，不额外获取 metadata 槽。
4. 用 `limiter.WrapDownload(ctx, obj, username)` 包一层限速 reader。
5. `io.CopyBuffer(w, limited, 1MiB)` 写响应。
6. `defer` 释放槽位；客户端断开时 `WaitN` 返回 context 错误，停止复制并释放。

### 8.2 上传

1. 获取传输槽（全局 1 槽 + 该用户 1 槽）。
2. 用 `limiter.WrapUpload(ctx, c.Request.Body, username)` 包一层限速 reader。
3. 交给 `h.Data.Put`，minio-go 顺序读取；TCP 背压自然传递到客户端。
4. `defer` 释放槽位。

### 8.3 分片上传

1. `MultipartInit`、`MultipartParts`、`MultipartAbort` 获取 metadata 槽。
2. `MultipartPart` 获取传输槽（全局 1 槽 + 该用户 1 槽），按上传字节限速。
3. `MultipartComplete` 获取传输槽（全局 1 槽 + 该用户 1 槽），因为服务端合并分片有实际 I/O。
4. 每片请求独立获取/释放，不跨请求持有槽位。

### 8.4 元数据操作

- `List`：metadata 槽；递归 List 大前缀时仍只占一个槽。
- `Stat`：只在下载流程内出现，复用 transfer 槽，避免嵌套。
- 管理接口和认证接口不进入 limiter，保持可管理性。

### 8.5 清理（二期）

- `ft-admin clean` 获取 clean 槽，并给批量删除加请求速率限制。
- 建议 cron 安排在业务低峰；清理速率可通过配置进一步降低。

## 9. 错误处理与可观测性

### 9.1 429 响应

```json
{"error":"server_busy","message":"服务器繁忙，请稍后重试"}
```

响应头：

```text
Retry-After: 5
```

### 9.2 ft-client 重试

- 在 `internal/client/api.go` 的统一请求函数中识别 `429` 和 `503`。
- 优先使用 `Retry-After`（秒或 HTTP 日期），缺失时指数退避 + 抖动。
- 最大尝试次数 6 次，总等待上限 5 分钟；超过后返回原错误。
- 只对可重放的请求重试：请求必须设置 `GetBody`；分片上传用 `io.SectionReader` 重建请求体，单文件 PUT 用文件句柄重建。
- 下载在响应体开始前收到 429/503 时重试；已经开始写文件后的中断沿用现有行为，不在此次范围内扩大改造。
- 重试期间在命令行输出“服务器繁忙，等待 N 秒后重试”，避免用户误以为卡死。

### 9.3 日志

- 记录字段：`request_id`、`username`、`class`、`wait_ms`、`limited_bytes`、`reason`。
- 只在以下情况打日志，避免每 1MiB 刷屏：
  - 排队时间超过 1s；
  - 返回 429；
  - 限速 reader 因 context 取消退出。
- 不记录文件内容、S3 凭据和 token。

## 10. 系统层边界

RustFS 容器的 cgroup/blkio 限制、md/内核参数调优由运维侧另行处理，不在本设计范围内。

应用层需要知道的边界：

- 应用层 bps 限的是 HTTP body / 对象内容字节，不是块设备物理字节。
- 上传路径的物理写通常约为应用层写的 `N/(N-2)` 倍（整条带顺序写），随机写更高；因此应用层写预算要低于系统层写上限。
- 系统层已有硬上限时，应用层软限应低于它，避免两边同时触发造成不可预测的排队。
- 系统层限速覆盖 Console/Cyberduck 直连；应用层覆盖不到这部分流量。
- 系统层的测量数据和调优参数由运维侧维护；本文只保留应用层预算需要的测量口径。

## 11. 基线测量与预算推导

### 11.1 块设备背景测量（运维侧）

在业务低峰执行，使用专用测试文件，确认路径后再运行。写测试会覆盖该文件。

```sh
# 顺序写
fio --name=seq-write --rw=write --bs=1M --direct=1 --numjobs=1 --iodepth=8 \
    --size=8G --filename=/mnt/raid6/fio.test --group_reporting

# 顺序读
fio --name=seq-read --rw=read --bs=1M --direct=1 --numjobs=1 --iodepth=8 \
    --size=8G --filename=/mnt/raid6/fio.test --group_reporting

# 4K 随机写
fio --name=rand-write --rw=randwrite --bs=4k --direct=1 --numjobs=4 --iodepth=32 \
    --size=4G --filename=/mnt/raid6/fio.test --group_reporting

# 并发梯度：分别用 numjobs=1,2,4,8,16 重复上面的顺序读写
```

同时开一个窗口观察：

```sh
iostat -x 1
```

关注 `%util`、`await`、`aqu-sz` 和每块成员盘的表现。

### 11.2 S3 对象层测试

使用 warp（MinIO 的 S3 压测工具）压真实 RustFS，分别测大对象和小对象：

```sh
# 大对象 PUT
warp put --host=127.0.0.1:9000 --access-key="$AK" --secret-key="$SK" \
  --bucket=file-transfer --concurrent=4 --duration=60s --obj.size=64MiB

# 大对象 GET
warp get --host=127.0.0.1:9000 --access-key="$AK" --secret-key="$SK" \
  --bucket=file-transfer --concurrent=4 --duration=60s --obj.size=64MiB

# 小对象 PUT（观察请求速率和 fsync 影响）
warp put --host=127.0.0.1:9000 --access-key="$AK" --secret-key="$SK" \
  --bucket=file-transfer --concurrent=8 --duration=60s --obj.size=4KiB
```

并发分别取 1、4、8、16，记录吞吐、请求速率和延迟。warp 参数以当前安装版本的 `--help` 为准。

### 11.3 预算公式

```text
K              = 拐点并发（聚合吞吐不再上升、await 开始翻倍前的并发数）
W_obj          = warp 大对象 PUT 吞吐 × 0.70   # 应用层文件内容字节
R_obj          = warp 大对象 GET 吞吐 × 0.75

max_concurrent      = max(2, K - 1)
global_write_bps    = W_obj
global_read_bps     = R_obj
per_user_write_bps  = W_obj / 2
per_user_read_bps   = R_obj / 2
```

应用层只使用对象层数据。fio 的块设备数据用于判断系统层上限和写放大，由运维侧使用；应用层 bps 不要直接填块设备吞吐。

如果没有 warp 数据，可按 `W_obj ≈ W_block ÷ (N/(N-2) × 元数据开销系数)` 估算，开销系数先用 1.2～1.5。

单用户上限取全局的一半：两个用户就能跑满磁盘，更多用户自然分享；如果业务要求至少 3 个用户同时满速，改为 `W_obj / 3`、`R_obj / 3`。

### 11.4 示例（必须以实测替换）

| 指标 | 示例值 | 推导结果 |
|---|---:|---|
| warp 大对象 PUT | 220MB/s | 应用层写预算 150MB/s |
| warp 大对象 GET | 300MB/s | 应用层读预算 225MB/s |
| 并发拐点 | K=5 | `max_concurrent = 4` |
| 小对象 PUT | 40/s | 元数据/clean 错峰（运维侧参考） |
| 顺序写拐点（背景） | 400MB/s | 系统层上限由运维侧设置 |

示例仅说明计算方式，不代表当前磁盘的真实性能。

### 11.5 测量数据采集清单（应用层预算与验证）

| 类别 | 指标 | 采集方式 |
|---|---|---|
| 吞吐/IOPS | `rkB/s`、`wkB/s`、`r/s`、`w/s` | `iostat -x 1 /dev/md0 /dev/sd[a-h]` |
| 延迟 | `await`、`r_await`、`w_await`、p95/p99 | `iostat`、`fio --lat_percentiles=1`、`biolatency` |
| 队列 | `aqu-sz`、`rrqm/s`、`wrqm/s` | `iostat -x 1` |
| 请求形态 | `rareq-sz`、`wareq-sz` | `iostat -x 1` |
| 写放大 | md `wkB/s` vs 成员盘 `wkB/s` 之和 | `iostat -x 1 /dev/md0 /dev/sd[a-h]` |
| 容器归因 | `rbytes/wbytes/rios/wios`、`io.pressure` | cgroup `io.stat`、`io.pressure` |
| 进程归因 | 每进程读写速率 | `pidstat -d 1`、`iotop -o` |
| CPU | `%iowait`、md 校验线程 CPU | `mpstat -P ALL 1` |
| 脏页 | `Dirty`、`Writeback`、`bi/bo`、`wa` | `vmstat 1`、`/proc/meminfo` |
| 阵列状态 | 重建/同步进度、降级 | `cat /proc/mdstat`、`mdadm --detail /dev/md0` |
| 磁盘健康 | 重映射扇区、待处理扇区、ERC/TLER | `smartctl -a /dev/sdX`、`smartctl -l scterc /dev/sdX` |
| 网络 | 吞吐、错误、重传 | `sar -n DEV 1`、`ss -tin`、`nstat -az` |
| 对象层 | 吞吐、请求速率、p50/p95/p99 | `warp put/get` |

基线采集顺序：

1. 确认 `/proc/mdstat` 无重建/降级，关闭备份、`updatedb`、杀毒扫描。
2. fio 顺序读写，并发 1、2、4、8、16，`--direct=1`，每轮 60s 以上。
3. fio 4K 随机读写，队列深度 1、8、32。
4. warp 大对象和小对象，并发 1、4、8、16。
5. 20 个 `ft-client` 混合大小文件，同时观察 Console 直连。
6. 每轮同时记录 `iostat`、`mpstat`、`vmstat`、容器 `io.stat` 和网络吞吐。

## 12. 代码改动清单

| 文件 | 改动 |
|---|---|
| `internal/config/config.go` | 新增 `LimitsSection`；解析、校验、默认值、环境变量覆盖 |
| `internal/config/config_test.go` | 默认值、非法值、大小解析、超时解析 |
| `internal/limiter/limiter.go` | 全局/每用户并发计数、每用户/全局令牌桶、用户表清理、Close |
| `internal/limiter/reader.go` | `rateLimitedReader`，1MiB 缓冲，context 取消 |
| `internal/limiter/limiter_test.go` | 并发上限、单用户隔离、超时、限速精度、ctx 取消、清理 |
| `internal/server/app/app.go` | `App` 增加 `Limits *limiter.Limiter` |
| `internal/bootstrap/bootstrap.go` | 根据配置创建 limiter，挂到 `App.Shutdown` |
| `internal/server/handler/file.go` | Upload/Download 获取槽位并包装 reader |
| `internal/server/handler/multipart.go` | Part/Complete 走 transfer；Init/Parts/Abort 走 metadata |
| `internal/server/handler/clean.go` | 二期：clean 槽 + 批量删除速率限制 |
| `internal/server/handler/file_test.go` | 槽位耗尽返回 429 的测试（不依赖 S3） |
| `internal/server/response/response.go` | 增加带 `Retry-After` 的错误响应辅助函数 |
| `internal/client/api.go` | 429/503 重试、`Retry-After` 解析、`GetBody` 重放 |
| `internal/client/upload.go` | 分片重试时重建 `SectionReader`；重试提示 |
| `internal/client/download.go` | 响应体开始前的 429/503 重试 |
| `internal/client/api_test.go` | 429 重试、Retry-After、最大次数、不可重放请求不重试 |
| `config/ft-server.example.toml` | 增加 `[limits]` 示例 |
| `README.md` | 限速配置、测量方法和 429 重试说明 |

### 12.1 limiter API

```go
type Limiter struct { /* ... */ }

func New(cfg config.Limits) *Limiter
func (l *Limiter) AcquireTransfer(ctx context.Context, user string) (release func(), err error)
func (l *Limiter) AcquireMetadata(ctx context.Context) (release func(), err error)
func (l *Limiter) AcquireClean(ctx context.Context) (release func(), err error)
func (l *Limiter) WrapUpload(ctx context.Context, r io.Reader, user string) io.Reader
func (l *Limiter) WrapDownload(ctx context.Context, r io.Reader, user string) io.Reader
func (l *Limiter) Close()
```

- `AcquireTransfer` 同时占用全局槽位和该用户的槽位，返回 `ErrBusy` 时 handler 映射为 429。
- `AcquireMetadata` 使用内部固定容量 8 的池，不暴露配置。
- `WrapUpload`/`WrapDownload` 在四个 bps 都为 0 时原样返回 reader，避免额外分配。
- 每用户状态用 `lastUsed` 记录最后使用时间；用户表超过 256 项或每 10 分钟清理一次。清理协程由 `Close` 停止。

### 12.2 依赖

- `golang.org/x/time/rate`：新增直接依赖。
- 并发计数用带缓冲 channel 实现，不引入 `x/sync/semaphore`。

## 13. 测试计划

### 13.1 单元测试

- 全局并发：容量 4，占满后第 5 个请求在超时后返回 `ErrBusy`。
- 单用户并发：用户 A 占满自己的 1 个槽后，第 2 个请求排队，但用户 B 仍可获取槽位。
- metadata 池独立：传输池占满时，`AcquireMetadata` 仍成功。
- 令牌桶：1MiB/s 读取 256KiB，耗时约 250ms（允许 ±50% 误差）。
- context 取消：等待令牌或槽位时取消，函数立即返回。
- 用户隔离：两个用户各自有独立桶，一个被限速不影响另一个。
- 用户表清理：过期用户被移除，活跃用户保留。
- 配置校验：所有非法组合返回错误。

### 13.2 handler 测试

- 预先占满全局传输池，调用 Download，断言 429、`Retry-After` 和 `server_busy`。
- 关闭 limiter（`enabled=false`）时，handler 行为与当前一致。
- 不在 handler 测试中依赖真实 S3；成功路径由集成测试覆盖。

### 13.3 客户端测试

- `httptest.Server` 第一次返回 429 + `Retry-After: 1`，第二次返回 200，断言重试成功。
- 无 `Retry-After` 时使用指数退避。
- 达到最大尝试次数后返回错误。
- 没有 `GetBody` 的请求不重试。
- 上传分片时，重试使用同一 part number 和重建后的 reader。

### 13.4 集成与压力测试

- 20 个 `ft-client` 并发，混合大文件和小文件，持续 10 分钟。
- 同时用 Console/Cyberduck 直连上传大文件，确认应用层不覆盖这部分流量，由系统层处理。
- 观察 `iostat -x 1`、RustFS 健康检查、SSH 登录延迟。
- 验证 429 只在设计预期内出现，且客户端能自动恢复。

## 14. 上线与回滚

| 阶段 | 内容 | 验证 |
|---|---|---|
| 0 | warp 基线（fio 块设备数据由运维侧提供） | 得到 K、W_obj、R_obj |
| 1 | limiter 代码上线，bps=0，只开并发闸门 | 20 并发无 5xx，429 可恢复 |
| 2 | 填入实测 bps，客户端重试上线 | 吞吐接近预算，延迟可控 |
| 3 | clean 限速、后台任务错峰（可选） | 清理不再造成 I/O 峰值 |

系统层的 cgroup/blkio 和 md/内核调优由运维侧并行处理，不阻塞应用层上线。

回滚：

- 应用层：配置 `enabled = false`，重启 `ft-server`。
- 系统层：由运维侧按自己的回滚流程处理。

## 15. 验收标准

- `ft-server` 无 5xx；429 只出现在排队超过 `queue_timeout` 时，且客户端能自动重试成功。
- 20 并发混合大小文件时，应用层吞吐不超过配置预算，延迟可控。
- 单个用户不能长期占用全部传输槽位；其他用户仍能获得槽位。
- 同时进行 Console 直连大文件传输时，应用层不覆盖这部分流量，系统层表现由运维侧验收。
- 系统层指标（`%util`、`await`、`io.pressure`）由运维侧监控。
- 单个大文件、单用户、20 用户三种场景的吞吐和延迟记录在案，作为后续调参基线。

## 16. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 429 引发重试风暴 | 指数退避 + 抖动，最大 6 次，服务端 `Retry-After` |
| bps 设得过低导致磁盘闲置 | 默认 0，测量后填；上线后持续观察吞吐 |
| 小文件 fsync 成为瓶颈，限带宽无效 | 增加元数据并发限制，业务侧尽量打包传输 |
| 每用户桶在用户表很大时内存增长 | 懒清理 + 定期清理，超过 256 项触发 |
| 限速 reader 增加延迟 | 1MiB 块 + burst 1MiB，延迟在百毫秒级 |
| 应用层限不到 Console/Cyberduck 直连 | 由运维侧系统层兜底 |

## 17. 已确认决策记录

1. 本设计只处理应用层限速；系统层 cgroup/blkio 和 md/内核调优由运维侧处理。
2. 配置采用简化 8 项：`enabled`、`max_concurrent`、`max_concurrent_per_user`、四个 bps、`queue_timeout`。
3. `bps` 默认 0，基线测量后再填；应用层 bps 用 warp 对象层数据。
4. 接受改造 `ft-client`，实现 429/503 重试。
5. 设计文档存放于 `doc/specs/`。