# 异地对象存储复制垂直切片验证记录（2026-11-12）

## 范围

`tooling/backup/objectstore.py` 纯标准库 S3 SigV4 客户端、`tooling/backup/replicate.py` 异地复制编排器、调度器可选复制接线、systemd 复制单元与配置样例。目标是把「本地备份」推进为「经过校验的异地副本」，覆盖生产恢复工程剩余的对象存储项。

## 签名正确性的权威交叉验证

环境无法拉取 MinIO 镜像（Docker Hub 与镜像源均拒绝），因此签名正确性不依赖自证，而是用临时安装到 `/tmp` 的 `botocore 1.44.0` 做字节级比对（验证后删除，未进入项目依赖）：

- 空载荷 GET 向量（AKIDEXAMPLE、20150830T123600Z、us-east-1/service）：本实现与 botocore `SigV4Auth` 生成的 Authorization 完全一致，签名 `5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31`（修正了一处仅凭记忆写错的期望值）。
- 带 `x-amz-content-sha256` 的 PUT 向量：以 botocore 实际发出的时间戳反喂本实现，双方签名逐字节相同。
- AWS 文档的签名密钥派生向量（iam/us-east-1/20150830）保持通过：`c4afb1cc…a4b9`。

## 自动化测试证据

`python3 -m unittest discover -s tooling/backup`：74 项全绿，其中新增 18 项。

- `test_objectstore.py`（10 项）：AWS 签名/密钥向量、查询串排序编码、桶名/对象键安全校验、凭据缺失、loopback S3 mock 上的 PUT→HEAD→GET→DELETE 全生命周期、ListObjectsV2 XML 解析、下载校验和不匹配时删除 scratch 并报错、S3 XML 错误码结构化映射。mock 服务端对每个请求重算 SigV4 并常量时间比较，签名或载荷哈希错误直接 403/400。
- `test_replicate.py`（8 项）：配置边界、首跑上传+下载校验、二跑零网络写入的幂等、变更内容重新复制、远端保留期按文件名模式与本地窗口对齐（含远端删除与状态清理）、损坏下载 fail-closed 且不写状态、畸形状态拒绝。
- `test_schedule.py` 新增复制钩子 2 项：`replication_config` 存在时 `run-once` 串联复制阶段并在报告中记录；对象存储不可达时整体退出码为 1，即使本地 drill 全部成功。

联调修复的真实问题：

1. 客户端签名后未把 `X-Amz-Date` 挂到实际请求头，导致服务端收不到签名时间——由 loopback mock 的签名重算捕获。
2. 复制器最初按合并文件列表共享一个保留计数，会把同组 manifest 提前删掉；修正为与调度器一致的按模式各自保留。
3. botocore 验证过程中发现记忆中的 AWS 测试向量期望值有误，以 SDK 实际输出为准并在测试注释中说明来源。

## 真实 CLI 端到端证据

在本机启动 stdlib 实现的 loopback S3 服务（真实 HTTP、真实 SigV4 校验），通过命令行运行 `replicate.py`：

1. `validate` 正确读取环境凭据并返回桶名。
2. 首次 `run-once`：2 组 PG 备份（dump+manifest 共 4 文件）全部上传，逐对象下载回算 sha256 通过，状态文件记录每个对象的 sha256、字节数与时间。
3. 第二次 `run-once`：4 个对象全部 `skipped`，无上传请求，证明幂等。
4. 本地保留窗口收紧到 1 并删除最旧文件后再次运行：远端对应两个对象被签名 DELETE 删除，状态同步清理，仅保留最新一组。
5. 负例（服务端 GET 注入篡改字节）：进程退出码 1，报告 `checksum mismatch` 并给出期望/实际哈希，新对象不进入状态文件；报告不含密钥材料。

## 边界与诚实声明

- 本地无法运行真实 MinIO/S3；loopback mock 覆盖了线路协议、签名重算与错误路径，botocore 交叉验证覆盖了签名数学，但与特定厂商的行为差异（如分段上传、KMS、虚拟主机风格桶名）需在部署环境用 `validate` 与一次手工 `run-once` 复核。
- 当前上传为单次 PUT，适合夜间备份量级；多段上传（multipart）与服务端加密（SSE-KMS）未实现，已在代码与 runbook 的能力边界内明示。
- systemd 复制单元为部署资产，未在容器环境实际启动；其行为由单测与 CLI 证据保证。
