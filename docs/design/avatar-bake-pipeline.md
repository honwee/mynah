# 形象烘焙管线（MuseTalk）

把一段「正在说话」的视频变成 MuseTalk 引擎能驱动的形象素材，全程在控制台完成。

## 为什么要做

在此之前烘焙是一条 ssh 手工流程：

1. scp 视频上服务器
2. `ffmpeg` 抽帧（还得记得改成 25fps）
3. `conda activate flashhead`，`cd` 到 MuseTalk 仓库根目录（**cwd 是有意义的**）
4. 跑 `bake_musetalk_avatar.py`
5. 重启 cored，让它重扫形象目录

每一步都能做错，控制台里一点都看不见，而且第 5 步会打断线上会话。

## 现在的形状

```
上传视频 ──[ffmpeg 容器]──> frames/*.png ──[烘焙容器]──> <名字>/ ──> 形象列表
```

两个一次性容器，由 cored 的 `internal/mtbake` 驱动。作业排队落库
（`avatar_mt_bake_jobs`），**一次只跑一个**——烘焙抢的是引擎池同一张卡的显存。

### 为什么烘焙镜像和服务镜像分开

`face_alignment`（FAN 关键点）和 `FaceParsing` 会拖进 scikit-image + numba +
llvmlite，将近 400MB，而实时 worker 一行都不 import。
`requirements-musetalk.txt` 里本来就写着「FaceParsing 是烘焙期依赖」——分开是让
这句话继续为真。

### 几个不显然的约束

- **cwd 必须是 MuseTalk 仓库根目录**。`FaceParsing` 用相对路径
  （`./models/face-parse-bisent/...`）找权重，上游从没让它可配置。
  `--musetalk-repo` 只解决 `import musetalk`，不解决这个。
- **`models/` 在参考部署里是符号链接**，指向仓库树之外。必须单独 bind-mount
  它，否则容器里链接指向不存在的路径。
- **时长硬上限 20 秒**。这是内存上限不是耐心上限：烘焙脚本把每一帧解码后
  全留在内存里，再镜像一份；720×1280×3 一帧约 2.7MB，60 秒需要约 16GB 宿主
  内存。形象是循环播放的，更长也不会更生动。
- **抽帧固定重采样到 25fps**。MuseTalk 按 25fps 播放，而素材常见是 24（Kling）
  或 30——不重采样形象就会整体偏快或偏慢。

### 发布是 rename，不是拷贝

产物先写进形象目录内部的 `.staging-<作业号>`，再 `rename` 到最终名字：同一个
文件系统，原子，不可能留下半个形象。空产物（脚本 0 退出但一张脸都没检测到）
直接拒绝，旧形象原样保留。

## 运维

```bash
# 烘焙镜像（服务镜像 + ffmpeg + face_alignment），仅需构建一次
docker build -t mynah/musetalk-bake:prod \
  -f deploy/compose/worker-musetalk-bake.Dockerfile .
```

相关开关见 `.env.prod.example` 的 `AVATAR_BAKE_*` 段。缺任何一项，控制台
**不显示**上传面板，而不是给一个点了报错的按钮。

烘焙完成后形象直接出现在「形象与声音」列表（目录是读取时重扫的），点「预热」
再去频道里绑定。**重新烘焙同名形象**会替换素材，但已经把它加载进内存的 worker
需要重新预热才会看到新素材。

## 素材要求

和 `.claude/skills/kling-avatar` 的铁律一致：**待机与说话必须同源**。用 Kling
生成的待机视频配实拍说话视频，切换的瞬间一定会跳。
