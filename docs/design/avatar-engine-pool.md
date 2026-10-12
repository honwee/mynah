# 设计：可选数字人引擎 + 按显存自动定容的 worker 池

状态：**设计稿，待评审**。当前实现是固定引擎（MuseTalk）+ 固定池大小（启动参数写死两个地址）。

---

## 1. 现状与问题

```
cored --worker 127.0.0.1:9414,127.0.0.1:9417
```

- 引擎在**容器启动命令里写死**（`musetalk_server.py`），换引擎 = 改 compose + 重建容器
- 池大小 = `--worker` 的地址个数，**cored 启动时固定**，运行期不可变
- 并发总额 = `len(ListWorkers())`（`channel_handlers.go:40`），所以池大小直接决定所有频道的并发配额上限
- 加/减 worker 需要人工算显存、改 compose、重启 cored——没有任何护栏，算错就 OOM

目标：引擎可选（MuseTalk / FlashHead / wav2lipLS），池上限由**剩余显存**自动推导。

---

## 2. 引擎目录（Engine Catalog）

三个引擎已经都有 gRPC worker，共享同一份 `proto/avatarengine/v1` 契约，所以对 cored 完全等价——差别只在资源画像与素材要求。

| 引擎 | 启动脚本 | 素材 | 备注 |
|---|---|---|---|
| `musetalk` | `musetalk_server.py` | 烘焙 avatar 目录（latents/coords/mask/full_imgs） | 当前生产 |
| `flashhead` | `server.py` | 单张肖像图即可（`--avatar`），可选 idle 目录 | 有 torch.compile 冷启动开销 |
| `wav2lipls` | `wav2lipls_server.py` | 烘焙 avatar 目录 + ckpt | 最省显存 |

**关键：显存画像必须实测，不能照抄论文或镜像大小。** 已有的教训（`docker-compose.full.yml` 里已写）：
- MuseTalk 刚起时读数 ~4.6GB，跑满一天并经历并发峰值后稳定在 **7.7GB**
- 稳态才是容量规划依据；用冷启动读数定容会**超配**

所以目录里记的是**稳态峰值**，且必须标注来源：

```go
// internal/avatarengine/catalog.go
type EngineSpec struct {
    Name         string // "musetalk" | "flashhead" | "wav2lipls"
    Label        string
    Script       string // musetalk_server.py
    Image        string // mynah/musetalk:prod
    VRAMSteadyMB int    // 稳态峰值，实测值
    VRAMSource   string // "measured 2026-07-29, 24h prod + concurrent peak"
    ColdStartSec int
    NeedsBake    bool   // 是否需要烘焙素材（影响能否直接切换）
}
```

已实测：`musetalk` = 7700MB（RTX 4090 参考机，双 worker 稳态各 7652MiB）。
**未实测：`flashhead` / `wav2lipls`** —— 落地前必须用 `workers/avatar/vram_probe.py` 同法测一遍，在此之前它们不进目录。

---

## 3. 容量计算

```
可用显存 = GPU总量 − 非池占用 − 安全余量
池上限   = floor(可用显存 / 引擎稳态显存)
```

三个细节决定这个公式对不对：

**(a) 非池占用要动态读，不能假设。** GPU0 现在 19.5GB 被 TTS 占着，GPU1 有 ASR（decode-only 后为 0）。读 `nvidia-smi` 的 per-process 占用，减去属于本池 worker 的那部分——否则「现有 worker 占的显存」会被当成不可用，池永远只能缩不能扩。

**(b) 安全余量不是可选项。** 建议 `max(2GB, 10% × GPU总量)`。理由：显存碎片、CUDA context、以及推理峰值高于稳态。4090 上即 2.4GB。

**(c) 跨 GPU 要分别算。** 池可以跨卡（`MUSETALK_GPU` 目前是单值，需扩成按 worker 指定）。上限 = 各卡容量之和，但**单个 worker 不能跨卡**。

按 RTX 4090 参考机实测代入：

| GPU | 总量 | 非池占用 | 余量 | 可用 | MuseTalk 上限 |
|---|---|---|---|---|---|
| 0 | 24.5G | 19.5G（TTS） | 2.4G | 2.6G | **0** |
| 1 | 24.5G | 0（ASR 已精简） | 2.4G | 22.1G | **2**（7.7×2=15.4，第3个需23.1G）|

结论与现状一致：**上限就是 2**。这验证了公式没跑偏——如果算出 3，说明余量取小了。

> 附带发现：ASR 切 decode-only 后 GPU1 多出 1.4G，但离第三个 worker 还差 1G。若要 3 个，需把 TTS 挪到 GPU1 之外或接受更小余量——不建议。

---

## 4. 池的生命周期

这是最难的部分，因为 **cored 的 `--worker` 是启动参数**。

### 4.1 supervisor 需要新增「创建」能力

现在只有 `Start/Stop/Restart`（作用于已存在的容器）。扩池要能**凭空造出**一个 worker 容器：

```go
type Supervisor interface {
    List(ctx) ([]Service, error)
    Start/Stop/Restart(ctx, name) error
    // 新增：按规格确保容器存在且符合期望（不存在则创建，规格变了则重建）
    Ensure(ctx context.Context, spec ContainerSpec) error
    Remove(ctx context.Context, name string) error
}
```

`ContainerSpec` 由引擎目录 + 池配置生成（镜像、argv、GPU、挂载）。Docker 实现走 `/containers/create` + `/start`；将来 k8s 实现改成调整 Deployment 的 replicas——**这正是当初把 Supervisor 做成编排器形状的收益**。

⚠️ 安全边界要跟着扩：现在白名单是**固定容器名**。动态池的名字是 `mynah-musetalk-{1..N}`，白名单要改成**前缀 + 序号上限**校验，绝不能退化成「调用方给什么名字就创建什么容器」——docker socket 等于 root。

### 4.2 cored 的 worker 列表要能变

三个选项，我的推荐是 **B**：

| | 做法 | 优点 | 代价 |
|---|---|---|---|
| A | 改池后重启 cored | 实现最简 | 断所有在线会话；cored 不在控制台可控列表里，得走 compose |
| **B** | **预留端口段 + 运行期 reconcile** | 不断会话；扩缩容秒级 | cored 需支持运行期增删 worker |
| C | 服务发现（etcd/k8s） | 最通用 | 单机上纯属过度设计 |

**B 的具体做法**：cored 启动时给一个池规格而非地址列表——

```
--worker-pool 127.0.0.1:9414-9419   # 端口段，最多 6 槽
```

cored 周期性（或收到控制台通知时）对端口段做健康探测：探通的进池、探不通的移出。这样扩容 = supervisor 起一个新容器占用段内下一个端口，cored 下一轮探测自动纳入；缩容 = 停容器，cored 自动移出。

要处理的边界：
- **缩容不能砍掉正在服务的 worker**——先标记 draining、等会话结束再停（`w.busy` 已有）
- **探测不能太频繁**，否则每次探测都建 gRPC 连接；建议 5s，且复用连接
- 已知坑（memory 里记着）：`waitHealthy` 对已死 worker 的 gRPC channel 有粘性缓存，reconcile 时必须显式关闭旧连接，否则会一直报 "worker not up yet"

### 4.3 与并发配额的联动

`checkConcurrencyQuota` 用 `len(ListWorkers())` 当总额。池缩容会让**已分配的配额超过新总额**：

- 缩容前必须校验：`∑(在线频道 max_concurrent) ≤ 新池大小`，否则拒绝并提示先调低频道配额
- 扩容无此问题（总额变大）
- 现有的「调低/持平已启用频道豁免」逻辑要保留，否则旧超配状态会把系统锁死

---

## 5. 换引擎

换引擎 = 整池重建，比扩缩容更重：

1. **前置校验**：目标引擎的素材是否就绪（MuseTalk/wav2lipLS 需要烘焙目录，FlashHead 只需一张图）。缺素材直接拒绝，不要建了容器再失败
2. **容量重算**：不同引擎显存画像不同，换引擎后池上限可能变小 → 走 §4.3 的配额校验
3. **滚动替换**：逐个 worker 替换（停一个→起新引擎的→等 READY→下一个），全程保持至少一个可用。这正是之前 MuseTalk 归位时验证过的模式
4. **回滚点**：记录换之前的引擎+池大小，失败可一键回退

⚠️ **不建议做成「频道级引擎选择」**。同一个池里混引擎会让并发配额失去意义（不同引擎的 worker 不等价），且 avatar 素材与引擎强绑定。引擎应是**部署级**选择。

---

## 6. 配置与界面

配置组 `avatar`（新增，DB settings）：

```json
{
  "engine": "musetalk",
  "pool_size": 2,          // 或 "auto"
  "gpu_assignment": [1, 1] // 每个 worker 落在哪张卡
}
```

控制台「本地服务」页增加**引擎池**卡片：

```
数字人引擎池
  引擎  [MuseTalk 1.5 ▾]   素材 ✓ leiya_mt
  池大小 [2] / 上限 2       ← 上限由显存算出，超过则禁用
  
  显存预算  GPU1: 22.1G 可用 ÷ 7.7G/worker = 2
            GPU0: 2.6G 可用（TTS 占用中）= 0
  
  ⚠ 当前在线频道已分配 2 路并发，缩容至 1 会超配
```

关键 UX 原则：**把「为什么是这个上限」摊开给用户看**，而不是给一个不能点的输入框。上面那个「22.1G ÷ 7.7G」的算式就是答案。

---

## 7. 落地顺序（每步独立可用、可回滚）

| 步 | 内容 | 风险 |
|---|---|---|
| 1 | 实测 flashhead / wav2lipls 稳态显存，建引擎目录 | 无（只读） |
| 2 | 容量计算 + 控制台**只读**展示（显存预算、当前上限） | 无（不改行为） |
| 3 | supervisor 加 `Ensure/Remove` + 前缀白名单 | 中（docker 写操作） |
| 4 | cored `--worker-pool` 端口段 + 运行期 reconcile | **高**（动实时链路） |
| 5 | 控制台扩缩容 + 配额联动校验 | 中 |
| 6 | 换引擎（滚动替换 + 前置校验 + 回滚） | 中 |

**建议先做 1–2**：零风险，且立刻回答「我现在到底能开几路」这个当前完全靠人肉算的问题。第 4 步是真正的分水岭，动它之前应先把 1–3 跑稳。

---

## 8. 明确不做

- **频道级引擎选择**（见 §5）
- **自动扩缩容**（按负载自动增减 worker）：冷启动 40–150 秒，等它起来高峰已经过去；固定池 + 明确上限更适合这个量级
- **跨机调度**：那是 k8s 的活，等有第二台 GPU 机器再说（Supervisor 接口已为此留好位置）
