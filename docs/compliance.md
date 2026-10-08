# 深度合成合规指引 / Deep-Synthesis Compliance Guide

Mynah 能克隆真人的**形象**(形象训练)与**声音**(音色克隆),并实时驱动
它们说话。这类"深度合成"能力在多数司法辖区受到专门监管——在中国大陆对应
《互联网信息服务深度合成管理规定》(2023-01-10 施行)与《生成式人工智能服务
管理暂行办法》。**作为部署者/运营者,合规义务在你**;本文说明产品内建的合规
支点,以及你还需要自行落实的部分。

> This project can clone a real person's likeness (avatar training) and voice
> (voice cloning). Deploying such "deep synthesis" capabilities is regulated in
> many jurisdictions (in mainland China: the Deep Synthesis Provisions, in the
> EU: the AI Act's transparency rules). Compliance obligations fall on the
> deployer — this document lists what the product gives you and what you must
> add.

## 产品内建的合规支点

### 1. 强制授权确认(consent)

以下三个上传入口都**强制要求 `consent` 字段**,缺失即 400 拒绝:

| 入口 | 路由 | 克隆对象 |
|---|---|---|
| 形象训练 | `POST /api/v1/avatars/training` | 真人形象 |
| 音色克隆 | `POST /api/v1/tts/voices` | 真人声音 |
| 动作骨架提取 | `POST /api/v1/motions` | 动作轨迹(不携带肖像) |

`consent` 是自由文本——把它当作**授权留档**:建议填入被克隆者姓名、授权
方式(书面协议编号/录音录像存证)、授权范围与期限。该字段仅存数据库,
不会出现在任何 API 响应中。

**你需要落实的**:与被克隆者签署书面授权(深度合成规定第十四条要求"单独
同意");未成年人形象/声音不得克隆;留存授权文件以备监管核查。

### 2. 显式标识(第十六/十七条)

深度合成规定要求对生成内容进行**显著标识**,让用户知道对话对象是合成的
数字人而非真人。落实建议(由浅入深):

1. **页面文案**:在嵌入页/访客页(`web/user/`)加"AI 数字人"角标或开场
   自我介绍——改动 HTML 即可,零代码成本;
2. **系统提示词**:在配置中心的人设 Prompt 中要求数字人在被问及身份时
   如实说明自己是 AI(默认提示词已是"数字人助手"口吻);
3. **视频水印**:当前管线不内置烧录水印。如需帧级标识,集成点在 avatar
   worker 的帧输出处(`workers/avatar/avatar_common.py` 的编码前一步)——
   在那里叠加半透明角标即可覆盖所有引擎输出。

### 3. 数据留存与删除

- 训练/克隆的源视频、参考音频落在服务器磁盘(`PL_TRAIN_UPLOAD_DIR` 等),
  删除任务/音色时同步删除源文件;
- 建议:为被克隆者提供撤回授权的通道,撤回后删除对应 job + 产物 + 上传源。

## 你还需要自行评估的

- **算法备案**:在中国大陆面向公众提供深度合成服务,可能触发算法备案义务
  (深度合成规定第十九条),以你的法务意见为准;
- **内容审核**:LLM 回复直接播报。接入你所在平台要求的内容安全审核
  (`ChatProvider` 是可插拔接缝,可在代理层加审核);
- **日志留存**:网络安全法体系下的日志留存要求(≥6个月)由部署方落实;
- **其他司法辖区**:EU AI Act 对 AI 系统交互透明度有类似要求(Art. 50);
  美国部分州(如加州 AB 602/1836 族)限制未经同意的数字复制品商用。

## 免责声明

本文是工程侧的合规支点说明,不构成法律意见。上线前请咨询你的法务。
