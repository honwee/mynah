"""FlashHeadEngine — 长驻流式 Avatar 引擎，封装 SoulX-FlashHead Lite。

实现 PersonaLive 技术方案 §7.4 的 AvatarEngine 接口：
    load_avatar / warmup / start_session / push_audio / read_video_frame / interrupt / set_action / close_session

- 一个 engine = 一个常驻 GPU pipeline = 一路会话（pipeline.generate 跨 chunk 有状态：motion frames）。
- push_audio 入队 → 后台单线程顺序跑 chunk（CUDA 单线程，防 libcuda 段错误）→ 帧出队。
- 冷启动首 chunk 触发 torch.compile（约 2 分钟）；warmup() 先付掉，稳态 ~0.43s/chunk（24 帧）≈ 2.2x realtime。
- av_chunks=True：输出粒度为「每 chunk 一个 (gen, chunk_idx, frames[24], audio_slice)」，
  供 LiveKit 发布端按音频主时钟把音视频同步推出（§13.2）。
- interrupt：generation counter，旧 gen 的音频/帧全部丢弃。
"""
import time
import threading
import queue
from collections import deque

import numpy as np
import torch
from loguru import logger

import os as _os, sys as _sys
_sys.path.insert(0, _os.path.dirname(_os.path.dirname(_os.path.abspath(__file__))))
from flash_head.inference import (
    get_pipeline, get_base_data, get_infer_params,
    get_audio_embedding, run_pipeline,
)


class FlashHeadEngine:
    def __init__(self, ckpt_dir, wav2vec_dir, model_type="lite"):
        t0 = time.time()
        self.pipeline = get_pipeline(world_size=1, ckpt_dir=ckpt_dir,
                                     wav2vec_dir=wav2vec_dir, model_type=model_type)
        p = get_infer_params()
        self.sample_rate = p["sample_rate"]
        self.tgt_fps = p["tgt_fps"]
        self.frame_num = p["frame_num"]
        self.motion_frames_num = p["motion_frames_num"]
        self.slice_len = self.frame_num - self.motion_frames_num
        self.cached_audio_duration = p["cached_audio_duration"]
        self.chunk_samples = self.slice_len * self.sample_rate // self.tgt_fps
        self.cached_samples = self.sample_rate * self.cached_audio_duration
        self.audio_end_idx = self.cached_audio_duration * self.tgt_fps
        self.audio_start_idx = self.audio_end_idx - self.frame_num
        self._avatar = None
        self._session = None
        logger.info(f"[engine] pipeline ready in {time.time()-t0:.1f}s "
                    f"slice_len={self.slice_len} chunk_samples={self.chunk_samples} "
                    f"chunk_dur={self.chunk_samples/self.sample_rate:.3f}s")

    def load_avatar(self, cond_image_path, use_face_crop=True, seed=42):
        self._avatar = dict(path=cond_image_path, crop=use_face_crop, seed=seed)
        self._apply_avatar()

    def _apply_avatar(self):
        a = self._avatar
        get_base_data(self.pipeline, cond_image_path_or_dir=a["path"],
                      base_seed=a["seed"], use_face_crop=a["crop"])

    @torch.no_grad()
    def warmup(self, n_chunks=2):
        if self._avatar is None:
            raise RuntimeError("call load_avatar() before warmup()")
        t0 = time.time()
        dq = deque([0.0] * self.cached_samples, maxlen=self.cached_samples)
        for _ in range(n_chunks):
            dq.extend([0.0] * self.chunk_samples)
            emb = get_audio_embedding(self.pipeline, np.array(dq),
                                      self.audio_start_idx, self.audio_end_idx)
            v = run_pipeline(self.pipeline, emb)
            _ = v[self.motion_frames_num:].cpu()
        self._apply_avatar()
        logger.info(f"[engine] warmup {n_chunks} chunks in {time.time()-t0:.1f}s (compile paid)")

    def start_session(self, session_id="s", av_chunks=False):
        self.close_session()
        if self._avatar is None:
            raise RuntimeError("call load_avatar() before start_session()")
        self._apply_avatar()
        self._session = _Session(self, session_id, av_chunks=av_chunks)
        self._session.start()
        return self._session

    def push_audio(self, pcm_f32_16k):
        if self._session:
            self._session.push_audio(pcm_f32_16k)

    def read_video_frame(self, timeout=None):
        if self._session:
            return self._session.read_frame(timeout)
        return None

    def interrupt(self):
        if self._session:
            self._session.interrupt()

    def set_action(self, action):
        if action == "idle" and self._session:
            self._session.push_audio(np.zeros(self.chunk_samples, dtype=np.float32))

    def close_session(self):
        if self._session:
            self._session.stop()
            self._session.join(timeout=30)
            self._session = None


class _Session(threading.Thread):
    def __init__(self, engine, session_id, av_chunks=False):
        super().__init__(daemon=True, name=f"flashhead-{session_id}")
        self.e = engine
        self.av_chunks = av_chunks
        self.audio_in = queue.Queue()
        self.frame_out = queue.Queue(maxsize=1024)
        self._buf = np.zeros(0, dtype=np.float32)
        self._dq = deque([0.0] * engine.cached_samples, maxlen=engine.cached_samples)
        self._gen = 0
        self._frame_idx = 0
        self._chunk_idx = 0
        self._stop_evt = threading.Event()

    def push_audio(self, pcm):
        self.audio_in.put((self._gen, np.asarray(pcm, dtype=np.float32)))

    def interrupt(self):
        self._gen += 1
        for q in (self.audio_in, self.frame_out):
            try:
                while True:
                    q.get_nowait()
            except queue.Empty:
                pass
        self._buf = np.zeros(0, dtype=np.float32)
        self._dq.extend([0.0] * self.e.cached_samples)
        logger.info(f"[session] interrupt -> gen={self._gen}")

    def read_frame(self, timeout=None):
        try:
            return self.frame_out.get(timeout=timeout)
        except queue.Empty:
            return None

    def stop(self):
        self._stop_evt.set()
        self.audio_in.put((None, None))

    def run(self):
        while not self._stop_evt.is_set():
            try:
                gen, pcm = self.audio_in.get(timeout=0.5)
            except queue.Empty:
                continue
            if pcm is None:
                break
            if gen != self._gen:
                continue
            self._buf = np.concatenate([self._buf, pcm])
            while len(self._buf) >= self.e.chunk_samples and not self._stop_evt.is_set():
                if gen != self._gen:
                    break
                slice_pcm = self._buf[:self.e.chunk_samples]
                self._buf = self._buf[self.e.chunk_samples:]
                frames = self._run_chunk(slice_pcm)
                if self.av_chunks:
                    # 每 chunk 一个包：(gen, chunk_idx, 帧[24,H,W,3], 该chunk音频[chunk_samples] float32)
                    self.frame_out.put((self._gen, self._chunk_idx, frames, slice_pcm.copy()))
                    self._chunk_idx += 1
                else:
                    for i in range(frames.shape[0]):
                        if gen != self._gen:
                            break
                        self.frame_out.put((self._gen, self._frame_idx, frames[i]))
                        self._frame_idx += 1

    @torch.no_grad()
    def _run_chunk(self, slice_pcm):
        self._dq.extend(slice_pcm.tolist())
        audio_array = np.array(self._dq)
        emb = get_audio_embedding(self.e.pipeline, audio_array,
                                  self.e.audio_start_idx, self.e.audio_end_idx)
        video = run_pipeline(self.e.pipeline, emb)
        video = video[self.e.motion_frames_num:]
        return video.cpu().numpy().astype(np.uint8)
