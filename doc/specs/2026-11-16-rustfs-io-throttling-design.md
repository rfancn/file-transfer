# RustFS 本地存储 I/O 限速设计

- 日期：2026-11-16
- 状态：设计已确认，待评审
- 范围：`ft-server` 应用层限速 + RustFS 容器系统层兜底 + md RAID6 运维调优

## 1. 背景

RustFS 的本地存储是 md RAID6 机械盘，I/O 性能有限。当前 `ft-server` 对上传、下载和分片上传不做任何限速，5～20 个客户端并发时会打满磁盘，连带影响 RustFS 元数据操作、健康检查和 SSH 登录。

同时，README 允许用户和摆渡操作员通过 RustFS Console / Cyberduck 直连 RustFS，这部分流量不经过 `ft-server`，应用层限速覆盖不到。

## 2. 已确认决策

| 决策 | 结论 |
|---|---|
| 限速范围 | 应用层 + 系统层组合（方案 A） |
| 带宽默认值 | `bps = 0`（不限），基线测量后再填 |
| 客户端改造 | 接受修改 `ft-client`，处理 429/503 重试 |
| RustFS 存储 | md 软 RAID6 |
| 设计文档位置 | `doc/specs/` |

## 3. 目标与非目标

### 3.1 目标

- 任何时刻磁盘 I/O 不超过实测拐点的 70%～80%，为元数据和后台任务留余量。
- 5～20 个并发客户端公平分享读写预算，单个用户不能长期占满。
- Console / Cyberduck 直连流量也受硬上限约束。
- 限速行为可观测、可配置、可回滚，不改变文件语义和断点续传能力。

### 3.2 非目标

- 不做网络层整形（`tc`）。
- 不限制 RustFS 后台扫描/修复的内部 I/O；只能确认其行为并错峰。
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
                      │                          │
                      ├─ transfer/metadata 信号量  └─ cgroup io.max 硬上限
                      └─ 每用户 + 全局令牌桶
Console/Cyberduck ─────────S3──────────────────▶ RustFS（只受 cgroup 约束）
```

| 层 | 作用 | 生效范围 | 失效后的后果 |
|---|---|---|---|
| 应用层 limiter | 精细、公平、可观测 | 经过 ft-server 的流量 | 直连流量不受控 |
| cgroup `io.max` | 安全网、硬上限 | RustFS 容器全部流量 | 直连流量和后台任务可能打满盘 |
| OS / md 调优 | 提高拐点、降低毛刺 | 整机 | 同样的限速值下吞吐更低 |

## 6. 限速模型

### 6.1 并发池（第一优先级）

使用两个独立的池，避免大文件传输饿死元数据：

| 池 | 默认容量 | 获取者 | 权重 |
|---|---:|---|---:|
| transfer | 6 单位 | 普通上传 | 2 |
| transfer | 6 单位 | 下载 | 1 |
| transfer | 6 单位 | 分片上传、Complete | 1 |
| metadata | 8 | List、Stat、Multipart Init/Parts/Abort | 1 |
| clean（二期） | 1 | `ft-admin clean` | 1 + 速率限制 |

效果：

- 最多 3 个普通上传，或 6 个下载，或混合；
- 元数据永远有独立通道，浏览目录不会被大文件传输完全阻塞；
- clean 单独一个槽，避免批量删除和用户传输互相拖死。

选择理由：在 HDD 上，限制“同时在途的流数量”比限制“总 MB/s”更能保护寻道行为。字节级限速无法阻止 20 条低速率流同时打乱磁头。

### 6.2 两级令牌桶（第二优先级）

```text
请求/响应字节 ──▶ 每用户令牌桶 ──▶ 全局令牌桶 ──▶ 实际 I/O
```

- 先等待用户桶，再等待全局桶，避免用户桶等待期间白占全局配额。
- 每次最多读取 1MiB，按实际读到的字节数扣令牌，最多产生 1MiB 的读前缓冲。
- `bps = 0` 表示该维度不限速；四个 bps 独立配置。
- 令牌桶 burst 设为 `max(1MiB, bps × 1s)`，避免小块读取造成微停顿。

### 6.3 排队与拒绝

- 获取槽位最多等待 `acquire_timeout`（默认 5s）。
- 超时返回 `429 Too Many Requests`，错误码 `server_busy`，并带 `Retry-After`（秒）。
- 客户端断开导致 context 取消时，直接释放槽位，不写错误响应。
- 元数据请求只排队，不扣字节令牌。
- 不在一个请求内嵌套获取两个池，避免死锁。

### 6.4 默认值

| 参数 | 默认值 | 说明 |
|---|---:|---|
| `enabled` | `true` | 总开关，回滚时置 `false` |
| `max_transfer_units` | `6` | transfer 池容量 |
| `upload_weight` | `2` | 写比读贵 |
| `download_weight` | `1` | 读并发可以更高 |
| `max_concurrent_meta` | `8` | 元数据独立通道 |
| `global_upload_bps` | `0` | 测量后填 |
| `global_download_bps` | `0` | 测量后填 |
| `per_user_upload_bps` | `0` | 测量后填 |
| `per_user_download_bps` | `0` | 测量后填 |
| `acquire_timeout` | `5s` | 排队上限 |

默认开启并发闸门、关闭字节限速，避免在没有基线时把系统限死。

## 7. 配置设计

### 7.1 配置文件

```toml
[limits]
  enabled             = true
  max_transfer_units  = 6
  upload_weight       = 2
  download_weight     = 1
  max_concurrent_meta = 8
  global_upload_bps   = 0
  global_download_bps = 0
  per_user_upload_bps = 0
  per_user_download_bps = 0
  acquire_timeout     = "5s"
```

### 7.2 校验规则

- `max_transfer_units >= 1`。
- `upload_weight >= 1`、`download_weight >= 1`。
- `upload_weight <= max_transfer_units`、`download_weight <= max_transfer_units`。
- bps 为 `0` 或 `>= 64KiB`；拒绝负数和小到没有意义的限速。
- `acquire_timeout` 在 `1s`～`60s` 之间。
- `max_concurrent_meta >= 1`。
- 解析沿用现有 `config.ParseSize`，支持 `KiB/MiB/GiB/TiB`。

### 7.3 配置结构

```go
type LimitsSection struct {
    Enabled            bool   `toml:"enabled"`
    MaxTransferUnits   int    `toml:"max_transfer_units"`
    UploadWeight       int    `toml:"upload_weight"`
    DownloadWeight     int    `toml:"download_weight"`
    MaxConcurrentMeta  int    `toml:"max_concurrent_meta"`
    GlobalUploadBPS    string `toml:"global_upload_bps"`
    GlobalDownloadBPS  string `toml:"global_download_bps"`
    PerUserUploadBPS   string `toml:"per_user_upload_bps"`
    PerUserDownloadBPS string `toml:"per_user_download_bps"`
    AcquireTimeout     string `toml:"acquire_timeout"`
}
```

解析后的运行时结构使用 `int64` 字节/秒和 `time.Duration`，与现有 `Runtime` 模式一致。

`LoadServer` 以 `DefaultServer()` 为基底解码，因此必须在 `DefaultServer()` 中显式设置 `Limits.Enabled = true`；否则布尔零值会让没有 `[limits]` 的旧配置默认关闭限速。其余字段的默认值也在 `DefaultServer()` 中给出。

## 8. 数据流

### 8.1 下载

1. 中间件完成用户认证，设置 `username`。
2. 获取 transfer 槽（weight = `download_weight`），最多等待 `acquire_timeout`；失败返回 429。
3. 在同一个槽内完成 `Stat` 和 `Get`，不额外获取 metadata 槽。
4. 用 `limiter.WrapDownload(ctx, obj, username)` 包一层限速 reader。
5. `io.CopyBuffer(w, limited, 1MiB)` 写响应。
6. `defer` 释放槽位；客户端断开时 `WaitN` 返回 context 错误，停止复制并释放。

### 8.2 上传

1. 获取 transfer 槽（weight = `upload_weight`）。
2. 用 `limiter.WrapUpload(ctx, c.Request.Body, username)` 包一层限速 reader。
3. 交给 `h.Data.Put`，minio-go 顺序读取；TCP 背压自然传递到客户端。
4. `defer` 释放槽位。

### 8.3 分片上传

1. `MultipartInit`、`MultipartParts`、`MultipartAbort` 获取 metadata 槽。
2. `MultipartPart` 获取 transfer 槽（weight = `download_weight`），按上传字节限速。
3. `MultipartComplete` 获取 transfer 槽（weight = `download_weight`），因为服务端合并分片有实际 I/O。
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

## 10. 系统层兜底（md RAID6）

### 10.1 确认 cgroup v2

```sh
stat -fc %T /sys/fs/cgroup   # 期望输出 cgroup2fs
```

### 10.2 md 设备与 io.max

md 是 bio-based 设备，`io.max` 在 `/dev/md0` 上是否生效取决于内核和发行版。必须先验证，不能假设。

验证步骤：

```sh
# 1. 查设备号和成员盘
lsblk -o NAME,MAJ:MIN,SIZE,TYPE,MOUNTPOINT
cat /proc/mdstat

# 2. 找到 RustFS 容器的 cgroup 路径
CID=$(docker inspect -f '{{.Id}}' rustfs)
ls /sys/fs/cgroup/system.slice/ | grep docker
# systemd cgroup driver 使用 /sys/fs/cgroup/system.slice/docker-$CID.scope/io.max
# cgroupfs cgroup driver 使用 /sys/fs/cgroup/docker/$CID/io.max

# 3. 写入限制（示例：md0 为 9:0）
echo "9:0 rbps=346030080 wbps=322961408 riops=2000 wiops=720" \
  > /sys/fs/cgroup/system.slice/docker-$CID.scope/io.max

# 4. 压测并观察是否被限住
iostat -x 1
```

如果 `/dev/md0` 上的 `io.max` 不生效，退回到成员盘：对每块 `sdX` 分别设置限制。

成员盘预算的近似换算：

```text
每盘写带宽 ≈ 逻辑写预算 / (N - 2)     # N 为 RAID6 成员盘数，写放大按整条带估算
每盘写 IOPS ≈ 逻辑写 IOPS × 6 / N      # RAID6 随机写 6 倍放大
每盘读带宽/IOPS 按实测每盘 %util 校准，取最热盘的峰值作为上限
```

示例：8 块盘 RAID6（6 数据盘），逻辑写预算 280MB/s，则每盘约 46.7MB/s。设置后用 `iostat -x 1` 观察每盘 `%util` 和 `await`，再上下调整。

### 10.3 Docker 配置

RustFS 容器不在本仓库的 compose 中，需要在它自己的 compose 或 `docker run` 参数里加：

```yaml
services:
  rustfs:
    # ...
    blkio_config:
      device_read_bps:
        - path: /dev/md0
          rate: "330mb"
      device_write_bps:
        - path: /dev/md0
          rate: "308mb"
      device_read_iops:
        - path: /dev/md0
          rate: "2000"
      device_write_iops:
        - path: /dev/md0
          rate: "720"
```

运行中的容器可以用 `docker update` 调整：

```sh
docker update --device-read-bps /dev/md0:330mb --device-write-bps /dev/md0:308mb \
  --device-read-iops /dev/md0:2000 --device-write-iops /dev/md0:720 rustfs
```

如果 md 层不生效，把上面的 `path` 换成每块成员盘，并按 10.2 的公式拆分额度。

### 10.4 调度器、脏页与 readahead

- 调度器：HDD 成员盘使用 `mq-deadline`；md 设备本身可能没有调度器，设置成员盘即可。
  ```sh
  echo mq-deadline > /sys/block/sdX/queue/scheduler
  ```
- 脏页：降低 writeback 尖峰，代价是突发吞吐下降，需实测。
  ```sh
  sysctl -w vm.dirty_background_bytes=536870912   # 512MiB
  sysctl -w vm.dirty_bytes=2147483648              # 2GiB
  ```
- readahead：顺序读为主可调大，混合随机则降低。
  ```sh
  blockdev --setra 8192 /dev/md0   # 4MiB（512B 扇区）
  ```
- 文件系统挂载：`noatime,nodiratime`；XFS 可加 `logbsize=256k`，不要为小文件设置过大的 `allocsize`。

### 10.5 md 专有调优

- `stripe_cache_size`：默认通常偏小，RAID6 写吞吐可能受 stripe cache 限制。测试 1024～8192，观察写吞吐和内存占用。
  ```sh
  echo 4096 > /sys/block/md0/md/stripe_cache_size
  ```
- `group_thread_cnt`：RAID5/6 的多线程校验计算，多核服务器可测试 2～4。
  ```sh
  echo 4 > /sys/block/md0/md/group_thread_cnt
  ```
- 重建限速：重建会打满磁盘，必须限制并错峰。
  ```sh
  sysctl -w dev.raid.speed_limit_max=50000   # KB/s
  ```
- md 没有 BBU，不存在“开写缓存”的选项；不要按硬件 RAID 的经验期待写缓存收益。

### 10.6 其他运维约束

- RustFS 日志、临时目录、`ft-server` 日志放到系统盘，不要和数据盘抢 I/O。
- 关闭或错峰：`updatedb`、备份、杀毒扫描、镜像同步。
- 确认 RustFS 当前版本是否有后台扫描/修复任务；若有，配置到低峰或降低并发。
- 1GbE 环境下客户端侧总带宽约 110MB/s，网络可能先于磁盘成为瓶颈；10GbE 才是磁盘瓶颈。用 `ip -br link` 或 `ethtool` 确认。

## 11. 基线测量与预算推导

### 11.1 fio 裸盘测试

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
W_budget       = 顺序写拐点吞吐 × 0.70
R_budget       = 顺序读拐点吞吐 × 0.75
transfer_units = max(2, K - 1)
global_upload_bps   = W_budget
global_download_bps = R_budget
per_user_upload_bps   = W_budget / 2
per_user_download_bps = R_budget / 2
cgroup 写上限 = W_budget × 1.1
cgroup 读上限 = R_budget × 1.1
cgroup IOPS 上限 = 实测 IOPS × 0.9
```

单用户上限取全局的一半：两个用户就能跑满磁盘，更多用户自然分享；如果业务要求至少 3 个用户同时满速，改为 `W_budget / 3`、`R_budget / 3`。

### 11.4 示例（必须以实测替换）

| 指标 | 示例值 | 推导结果 |
|---|---:|---|
| 顺序写拐点 | 400MB/s @ K=1 | 写预算 280MB/s |
| 顺序读拐点 | 400MB/s @ K=1 | 读预算 300MB/s |
| 4K 随机写 | 800 IOPS @ K=4 | cgroup wiops 720 |
| 小对象 PUT | 40/s | clean 和元数据需错峰 |
| transfer 槽位 | 6 | 最多 3 上传或 6 下载 |

示例仅说明计算方式，不代表当前磁盘的真实性能。

## 12. 代码改动清单

| 文件 | 改动 |
|---|---|
| `internal/config/config.go` | 新增 `LimitsSection`；解析、校验、默认值、环境变量覆盖 |
| `internal/config/config_test.go` | 默认值、非法值、大小解析、超时解析 |
| `internal/limiter/limiter.go` | 加权信号量、每用户/全局令牌桶、用户表清理、Close |
| `internal/limiter/reader.go` | `rateLimitedReader`，1MiB 缓冲，context 取消 |
| `internal/limiter/limiter_test.go` | 权重、超时、限速精度、ctx 取消、用户隔离、清理 |
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
| `README.md` | 限速配置、cgroup 兜底、测量和调优说明 |

### 12.1 limiter API

```go
type Class int

const (
    ClassUpload Class = iota
    ClassDownload
    ClassMultipart
)

type Limiter struct { /* ... */ }

func New(cfg config.Limits) *Limiter
func (l *Limiter) AcquireTransfer(ctx context.Context, class Class, user string) (release func(), err error)
func (l *Limiter) AcquireMetadata(ctx context.Context) (release func(), err error)
func (l *Limiter) AcquireClean(ctx context.Context) (release func(), err error)
func (l *Limiter) WrapUpload(ctx context.Context, r io.Reader, user string) io.Reader
func (l *Limiter) WrapDownload(ctx context.Context, r io.Reader, user string) io.Reader
func (l *Limiter) Close()
```

- `AcquireTransfer` 返回 `ErrBusy` 时，handler 映射为 429。
- `WrapUpload`/`WrapDownload` 在 `bps = 0` 时原样返回 reader，避免额外分配。
- 每用户状态用 `lastUsed` 记录最后使用时间；用户表超过 256 项或每 10 分钟清理一次。清理协程由 `Close` 停止。

### 12.2 依赖

- `golang.org/x/time/rate`：新增直接依赖。
- `golang.org/x/sync/semaphore`：`x/sync` 已是间接依赖，改为直接依赖。

## 13. 测试计划

### 13.1 单元测试

- 加权信号量：容量 6，3 个上传（weight 2）占满，第 4 个在超时后返回 `ErrBusy`。
- metadata 池独立：transfer 占满时，`AcquireMetadata` 仍成功。
- 令牌桶：1MiB/s 读取 256KiB，耗时约 250ms（允许 ±50% 误差）。
- context 取消：等待令牌或槽位时取消，函数立即返回。
- 用户隔离：两个用户各自有独立桶，一个被限速不影响另一个。
- 用户表清理：过期用户被移除，活跃用户保留。
- 配置校验：所有非法组合返回错误。

### 13.2 handler 测试

- 预先占满 transfer 池，调用 Download，断言 429、`Retry-After` 和 `server_busy`。
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
- 同时用 Console/Cyberduck 直连上传大文件，验证 cgroup 生效。
- 观察 `iostat -x 1`、RustFS 健康检查、SSH 登录延迟。
- 验证 429 只在设计预期内出现，且客户端能自动恢复。

## 14. 上线与回滚

| 阶段 | 内容 | 验证 |
|---|---|---|
| 0 | fio + warp 基线，确认 cgroup v2 和 md 设备 | 得到 W_budget/R_budget/K |
| 1 | 调度器、脏页、readahead、md 参数、cgroup 兜底 | `iostat` 无持续 100% |
| 2 | limiter 代码上线，bps=0，只开并发闸门 | 20 并发无 5xx，429 可恢复 |
| 3 | 填入实测 bps，客户端重试上线 | 吞吐接近预算，延迟可控 |
| 4 | clean 限速、后台任务错峰 | 清理不再造成 I/O 峰值 |

回滚：

- 应用层：配置 `enabled = false`，重启 `ft-server`。
- 系统层：`docker update` 把 bps/IOPS 设为 `max`，或删除 `blkio_config` 后重建容器。
- md/内核参数：保留调优前的值，逐项恢复。

## 15. 验收标准

- 20 并发混合大小文件时，成员盘 `%util` 持续低于 80%～85%，`await` 不持续翻倍。
- `ft-server` 无 5xx；429 只出现在排队超过 `acquire_timeout` 时，且客户端能自动重试成功。
- 同时进行 Console 直连大文件传输时，cgroup 生效，`ft-client` 不被完全饿死。
- SSH 登录、RustFS 健康检查、`ft-admin` 管理操作在压力下仍可响应。
- 单个大文件、单用户、20 用户三种场景的吞吐和延迟记录在案，作为后续调参基线。

## 16. 风险与缓解

| 风险 | 缓解 |
|---|---|
| md 层 `io.max` 不生效 | 按 10.2 验证，退回成员盘限制 |
| 429 引发重试风暴 | 指数退避 + 抖动，最大 6 次，服务端 `Retry-After` |
| bps 设得过低导致磁盘闲置 | 默认 0，测量后填；上线后持续观察 `%util` |
| 小文件 fsync 成为瓶颈，限带宽无效 | 增加请求速率/元数据并发限制，业务侧尽量打包传输 |
| RustFS 后台任务不可控 | 确认当前版本行为，错峰或降低其并发 |
| 每用户桶在用户表很大时内存增长 | 懒清理 + 定期清理，超过 256 项触发 |
| 限速 reader 增加延迟 | 1MiB 块 + burst 1MiB，延迟在百毫秒级 |

## 17. 已确认决策记录

1. 采用方案 A：应用层 + 系统层组合。
2. `bps` 默认 0，基线测量后再填。
3. 接受改造 `ft-client`，实现 429/503 重试。
4. RustFS 使用 md 软 RAID6，系统层先验证 md 设备，必要时退回成员盘。
5. 设计文档存放于 `doc/specs/`。