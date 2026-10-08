# Mynah · AutoDL 镜像说明（发布到 codewithgpu 时用）

**Mynah 开源数字人：开箱即用，实时语音对话 + 管理控制台。** 本镜像已装好全部依赖和模型，开机后一条命令启动。

```bash
~/mynah/start.sh        # 启动 TTS / ASR / 数字人引擎 / cored
```

然后在 AutoDL 的「自定义服务」里映射 8443（访客页）和 9443（控制台）两个端口，浏览器打开
`https://<映射地址>:8443/`（自签证书，点"继续访问"），允许麦克风，开始对话。
控制台首次登录的管理员密码在 `~/mynah/logs-cored.log` 里（搜 `initial admin`），登录后请立即修改。

- 默认 TTS 为 EdgeTTS（免费，需实例能访问外网）；想要音色克隆请切 Qwen3-TTS，见文档站「TTS 三档」。
- 默认数字人引擎为 wav2lipLS（最省显存）；24G 显卡可在控制台「本地服务」切到 MuseTalk / FlashHead。
- 文档：https://honwee.github.io/mynah/zh/ · 源码：https://github.com/honwee/mynah（Apache-2.0）
