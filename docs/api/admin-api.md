# Mynah 管理 API（admin API v1）

控制面 REST API,供管理控制台前端对接。由 `cored --db <dsn> --admin-listen <addr>` 启用,与访客端口(8020/8443)网络隔离。

- **Base URL**: `http://<admin-listen>/api/v1`(默认 `127.0.0.1:9080`;前端远程开发时可起 `--admin-listen 0.0.0.0:9080`)
- **HTTPS**: `--admin-tls-listen :9443` 在管理面加开 HTTPS(复用 --tls-cert/--tls-key 证书),公网远程访问控制台用这个
- **前端托管**: `--admin-web <dir>` 可让管理端口同源托管控制台静态产物(生产部署方案,免 CORS/dev proxy);`/api/` 前缀之外的路径走文件服务
- **认证**: 除 `POST /auth/login` 外全部需要 `Authorization: Bearer <token>`(JWT,24h 有效)
- **响应格式**: 成功 `{"code": 0, "data": ...}`;失败 `{"code": <http status>, "msg": "..."}`,HTTP 状态码语义化(400 参数错 / 401 未认证 / 404 不存在 / 409 冲突 / 5xx 服务端)
- **首次登录**: 首次启动自动创建 `admin` 账号,随机密码打印在 cored 日志(仅一次),`must_change_password=true` 提示改密

## 端点总览

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /auth/login | 登录,返回 token |
| GET | /auth/me | 当前管理员信息 |
| POST | /auth/password | 修改密码 |
| GET | /health | 各依赖服务并行探活 |
| GET | /features | 版本与能力开关(社区版/企业版) |
| GET | /config | 全部配置(合并视图) |
| GET | /config/{group} | 单组配置(llm/tts/rag/system) |
| PUT | /config/{group} | 改配置(部分字段即可) |
| POST | /config/llm/test | 对话引擎测试连接 |
| GET | /tts/voices | 音色列表(代理 TTS 服务) |
| POST | /playground/chat | 对话调试(单轮,无状态) |
| POST | /playground/chat/stream | 对话调试流式版(SSE) |
| POST | /playground/avatar/offer | 数字人调试:WebRTC offer→answer |
| POST | /playground/avatar/human | 数字人调试:文本驱动(echo/chat) |
| POST | /playground/avatar/interrupt | 数字人调试:打断当前回合 |
| GET | /kb | 知识库列表 |
| POST | /kb | 创建知识库 |
| PATCH | /kb/{id} | 改名/描述/启停 |
| DELETE | /kb/{id} | 删除(级联文档+向量) |
| GET | /kb/{id}/documents | 文档列表 |
| POST | /kb/{id}/documents | 上传文档(multipart) |
| GET | /documents/{id} | 文档详情(轮询摄取状态) |
| DELETE | /documents/{id} | 删除文档 |
| POST | /documents/{id}/reindex | 重新分块+嵌入 |
| POST | /kb/search-test | 检索调参测试 |
| GET | /sessions | 在线会话列表 |
| GET | /sessions/{id} | 会话详情 |
| DELETE | /sessions/{id} | 踢下线 |

## auth

```bash
# 登录
curl -X POST $BASE/auth/login -d '{"username":"admin","password":"<pw>"}'
# -> {"code":0,"data":{"token":"eyJ...","must_change_password":true}}

# 当前身份
curl $BASE/auth/me -H "Authorization: Bearer $TOKEN"
# -> {"code":0,"data":{"id":1,"username":"admin"}}

# 改密(新密码 >= 8 位)
curl -X POST $BASE/auth/password -H "Authorization: Bearer $TOKEN" \
  -d '{"old_password":"...","new_password":"..."}'
```

## health

```bash
curl $BASE/health -H "Authorization: Bearer $TOKEN"
```

返回每个组件 `{status: ok|down|disabled, latency_ms, error}`,组件:`avatar`(数字人引擎)/`asr`/`tts`/`llm`/`ollama`(embedding)/`postgres`。整体 `status: ok|degraded`。

## features

```bash
curl $BASE/features -H "Authorization: Bearer $TOKEN"
# -> {"code":0,"data":{"edition":"oss","features":{"rerank":false,"multi_tenant":false,"training":false,"scheduling":false}}}
```

前端按此显示/隐藏企业版能力入口。社区版(edition=oss)所有高级特性为 false;企业版构建按授权返回 true。

## config(三层合并:默认值 < 启动 flag < 数据库)

组:`llm`(provider/base_url/model/api_key/bot_id/stream/system_prompt)、`tts`(base_url/voice)、`rag`(enabled/embed_url/embed_model/embed_dim/chunk_size/chunk_overlap/top_k/threshold/timeout_ms/rerank_url/rerank_model/rerank_api_key/rerank_candidates)、`system`(listen/worker_addr/video_codec/...)。

`llm.stream`(默认 true):流式回复。开=边生成边逐句送 TTS,首响最快;关=等完整回复再合成,只为不支持 SSE 的 OpenAI 兼容后端准备(coze 忽略此开关,始终流式)。
`rag.rerank_*`(企业版):精排服务配置,rerank_url 留空=关闭;OSS 二进制无精排客户端,静默忽略。rerank_candidates=0 为自动(top_k×4,12~20)。

### 对话引擎 provider

`llm.provider` 选对话协议,傻瓜式契约:下拉选 provider → 填 base_url + api_key → 测试连接 → 保存热生效。

| provider | 覆盖 | 字段 |
|---|---|---|
| `openai`(默认) | ollama / vLLM / FastGPT / RAGFlow / OneAPI / 云端 | base_url + api_key(可选) + model + system_prompt |
| `dify` | Dify 应用(API Key 已绑定应用) | base_url + api_key |
| `coze` | 扣子 bot(Chat API v3) | api_key + bot_id(base_url 留空默认 api.coze.cn) |

选 dify/coze 时人设与知识库归平台侧:`system_prompt` 不生效,建议 `rag.enabled=false` 避免双重检索(Dify conversation 由 cored 按会话自动映射)。

```bash
# 测试连接(用提交值发一轮真实对话,不落库)
curl -X POST $BASE/config/llm/test -H "Authorization: Bearer $TOKEN" \
  -d '{"provider":"dify","base_url":"https://api.dify.ai","api_key":"app-..."}'
# -> {"code":0,"data":{"reply":"连接正常","latency_ms":830}}
# 失败返回 502,msg 为人话提示(Key 无效/地址不可达/接口不存在)+原始错误
```

- PUT 支持**部分字段**,未提交的字段保持现值;未知字段返回 400。
- `llm`/`tts`/`rag` 组**热生效**(在线会话下一轮对话即用新值),响应 `applied: "hot"`。
- `system` 组返回 `applied: "restart_required"`,重启 cored 后生效。

```bash
# 在线改人设(立即生效,无需重启)
curl -X PUT $BASE/config/llm -H "Authorization: Bearer $TOKEN" \
  -d '{"system_prompt":"你是售前顾问小灵..."}'
# -> {"code":0,"data":{"applied":"hot","config":{...}}}

# 开关 RAG
curl -X PUT $BASE/config/rag -H "Authorization: Bearer $TOKEN" -d '{"enabled":true}'
```

## kb / documents(RAG 知识库)

文档上传后异步摄取:`pending → processing → ready / failed`(失败原因在 `error` 字段)。摄取 = 分块(默认 500 字/50 字重叠)→ bge-m3 嵌入(1024 维)→ pgvector 入库。知识库创建时锁定当时的 embedding 模型与维度。

```bash
# 建库
curl -X POST $BASE/kb -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"产品手册","description":"售前知识"}'

# 上传(.txt/.md,<=10MB,multipart 字段名 file)
curl -X POST $BASE/kb/1/documents -H "Authorization: Bearer $TOKEN" \
  -F "file=@manual.md"
# -> data.id=1, status=pending

# 轮询至 ready
curl $BASE/documents/1 -H "Authorization: Bearer $TOKEN"
# -> {"status":"ready","chunk_count":3,...}

# 检索调参(不影响线上配置;top_k/threshold 可选,缺省用当前 rag 配置)
curl -X POST $BASE/kb/search-test -H "Authorization: Bearer $TOKEN" \
  -d '{"query":"企业版多少钱","top_k":5,"threshold":0.5}'
# -> {"latency_ms":...,"reranked":false,"chunks":[{"doc_id":1,"filename":"manual.md","seq":0,"content":"...","score":0.78}]}
```

企业版精排(rerank):部署了 reranker 时 search-test 默认启用精排,可传 `"rerank":false` 对比向量原序;`reranked:true` 时每个 chunk 额外带 `rerank_score`。社区版无此能力,`reranked` 恒为 false,请求里的 `rerank` 字段被忽略(不报错)。

对话注入:`rag.enabled=true` 时,每轮对话先用用户问题检索(命中分数 ≥ threshold 的 top_k 条),拼成一条临时 system 消息随该轮请求注入 LLM(不进会话历史);检索超时(timeout_ms)或出错则**自动降级**为无知识库回答,绝不阻塞对话。

## tts voices

```bash
curl $BASE/tts/voices -H "Authorization: Bearer $TOKEN"
# -> {"code":0,"data":[{"id":"vivian","name":"vivian"},...]}
```

代理 TTS 服务的 `GET /v1/audio/voices`,跟随当前 tts.base_url 配置(热生效)。上传的自定义音色 name 带"(自定义)"后缀。TTS 服务不可达时返回 502,前端应退化为手填文本框。

## playground(对话调试)

控制台内验证人设/RAG 改动,走与访客回合相同的 LLM+RAG 链路(无 TTS/数字人)。**无状态**:历史由前端持有、每轮全量提交,不产生会话、不影响在线访客。

```bash
curl -X POST $BASE/playground/chat -H "Authorization: Bearer $TOKEN" \
  -d '{"history":[{"role":"user","content":"企业版多少钱"}]}'
# -> {"code":0,"data":{"reply":"...","chunks":[{"doc_id":1,"filename":"manual.md","score":0.78,...}],"latency_ms":1200}}
```

- `history` 必须以 user 消息收尾;多轮就把之前的 user/assistant 都带上(建议只留最近 10 轮)。
- `chunks` 为该轮实际注入的知识片段(rag.enabled=false / 无命中 / 检索降级时为空数组),与对话注入语义一致;开精排时带 `rerank_score`。
- LLM 不可用返回 502;未配置 `--llm` 时返回 502("chat not configured")。

### 流式版(打字效果)

`POST /playground/chat/stream`:同请求体,响应为 SSE。`event: delta` 携带 `{"content":"片段"}`(llm.stream=false 时整段一次到达);收尾 `event: result` 携带与非流式版相同的完整对象;中途失败发 `event: error` + `{"message":"..."}`。旧端点保留,脚本冒烟用。

### 数字人调试(实时画面)

控制台在操练场里直连一路**真实**访客管线会话(走 admin 端口、带 JWT,不暴露访客端口):

```bash
# 1. WebRTC 信令(非 trickle,answer 已含 ICE candidates)
curl -X POST $BASE/playground/avatar/offer -H "Authorization: Bearer $TOKEN" \
  -d '{"sdp":"<browser offer sdp>"}'
# -> {"code":0,"data":{"sdp":"<answer>","type":"answer","sessionid":"100001"}}

# 2. 文本驱动一回合
curl -X POST $BASE/playground/avatar/human -H "Authorization: Bearer $TOKEN" \
  -d '{"sessionid":"100001","text":"你好","type":"chat","interrupt":true}'
#    type: "chat"=过 LLM+RAG;"echo"(默认)=原文直接 TTS 播报(调试口型/音色链路)
#    interrupt: true 时先打断在播回合再开新回合

# 3. 打断
curl -X POST $BASE/playground/avatar/interrupt -H "Authorization: Bearer $TOKEN" \
  -d '{"sessionid":"100001"}'
```

- 浏览器若在 offer 前创建 DataChannel(任意 label),回复文本将以 JSON 流推送:`{"status":"start"}` 回合开始、`{"status":"ing","text":"句子"}` 逐句字幕(与 TTS 切句同步)、`{"status":"end"}` 结束;麦克风识别结果以 `{"status":"user","text":...}` 回显。发 `ping` 文本会收到 `pong`(应用层心跳)。
- **会话是真实会话**:占用(单会话)worker、出现在会话列表里,与访客互斥;断开 WebRTC 即释放。
- offer 里带麦克风轨(sendrecv)且 ASR 已配置时,语音输入/抢话打断与访客页行为一致。

## sessions

```bash
curl $BASE/sessions -H "Authorization: Bearer $TOKEN"
# -> [{"id":"100001","created_at":"...","turns":3,"speaking":false,"voice":true}]

curl -X DELETE $BASE/sessions/100001 -H "Authorization: Bearer $TOKEN"   # 踢下线
```

## 降级行为

`cored` 不传 `--db` 时:admin API、RAG、所有数据库依赖整体禁用,行为与 P3 完全一致(最小部署不需要 Postgres)。
