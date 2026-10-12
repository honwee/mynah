# Pipecat：Mynah 当脸，或 Pipecat 当大脑

[Pipecat](https://github.com/pipecat-ai/pipecat) 是实时语音 Agent 的开源框架。Mynah 两个方向都能接。

## A. Mynah 作为 Pipecat 的数字人服务

`pip install pipecat-ai-mynah`（[仓库](https://github.com/honwee/pipecat-ai-mynah)）。把 `MynahVideoService` 放在 TTS 之后：

```python
from pipecat_mynah import MynahVideoService

pipeline = Pipeline([
    transport.input(), stt, context.user(), llm, tts,
    MynahVideoService(worker_addr="127.0.0.1:9415", avatar="/.../assets/nova.halfbody.png"),
    transport.output(), context.assistant(),
])
```

它吞掉 TTS 音频，经 worker 的 gRPC 契约送给 Mynah 数字人 worker，再把口型对齐的 RGB 画面和同一份音频同步推回去。Pipecat 自带的数字人服务都是云端 API；这个跑在你自己的 GPU 上，Apache-2.0，不按分钟计费。

## B. 用 Pipecat 机器人当 Mynah 的大脑

Mynah 的大脑模式 **`pipecat`**（控制台 → 配置中心 → 对话大脑）把访客麦克风送给一个 Pipecat 机器人，机器人合成的语音回来驱动口型。VAD、识别、大模型、合成、工具、Flows 都在机器人侧；Mynah 负责脸、控制台、频道和并发。

机器人侧：`WebsocketServerTransport` + `ProtobufFrameSerializer`，建议 16 kHz 进出。完整示例：pipecat-ai-mynah 仓库的 `examples/mynah_brain_bot.py`。把它的 `ws://host:port` 填进控制台的 Pipecat 地址，新会话生效。

线上传什么：

| Mynah → 机器人 | 机器人 → Mynah |
|---|---|
| `InputAudioRawFrame`（16 kHz 单声道 PCM） | `OutputAudioRawFrame`（TTS，任意采样率）→ 口型 |
| `TranscriptionFrame`（页面上打的字） | `TranscriptionFrame`（访客说了什么）→ 字幕 |
| `InterruptionFrame`（页面侧打断） | `TextFrame`（回复文本）→ 字幕 |
| | `InterruptionFrame`（机器人 VAD 听到访客）→ 切断播放 |

此模式的边界：Mynah 的知识库和本地音色不生效（在机器人侧配）；固定台词（频道问候语、`mynah_speak`）走 Mynah 本地 TTS。
