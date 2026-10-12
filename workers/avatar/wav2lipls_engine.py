#!/usr/bin/env python
"""Wav2LipLS (high-quality LS variant) avatar backend for cored.

Same duck-typed engine contract as FlashHeadEngine / Wav2LipEngine (see
avatar_common.py), so the generic AvatarSessionServicer drives it unchanged.
LS differs from the plain wav2lip backend (wav2lip_engine.py):
  * audio features = HuBERT (ultralight Audio2Feature + hubert-large-ls960-ft),
    [10,1024] per video frame (audio_feat_length=[0,5]), not mel.
  * model = wav2lipls.models.Human(sr=False), fp16, with an LSTM that carries
    (hn,cn) across frames and resets every syncnet_T=12 frames.
  * _correction edge-feather + green-mask paste-back (cleaner than a hard paste).
  * face_size 256 or 384 — set via WAV2LIPLS_FACE_SIZE before importing Human;
    the checkpoint must match (256->checkpoint_step001430000, 384->...2130000).

Adapts the wav2lipLS reference inference + HuBERT audio-feature step into one
background thread fed by push_audio() / drained by read_video_frame().
"""
import glob
import logging
import os
import pickle
import threading
import time
from queue import Queue, Empty

import cv2
import numpy as np
import torch

log = logging.getLogger("avatar-worker")


def _device(dev=None):
    if dev:
        return dev
    return "cuda" if torch.cuda.is_available() else "cpu"


def _read_imgs(paths):
    return [cv2.imread(p) for p in paths]  # BGR uint8


def load_avatar_data(avatar_dir):
    full_dir = os.path.join(avatar_dir, "full_imgs")
    face_dir = os.path.join(avatar_dir, "face_imgs")
    with open(os.path.join(avatar_dir, "coords.pkl"), "rb") as f:
        coords = pickle.load(f)

    def _sorted(d):
        gl = glob.glob(os.path.join(d, "*.[jpJP][pnPN]*[gG]"))
        return sorted(gl, key=lambda x: int(os.path.splitext(os.path.basename(x))[0]))

    full = _read_imgs(_sorted(full_dir))
    face = _read_imgs(_sorted(face_dir))
    log.info("avatar loaded: %d full %s, %d face %s, %d coords",
             len(full), full[0].shape, len(face), face[0].shape, len(coords))
    return full, face, coords


def _mirror_index(size, index):
    turn = index // size
    res = index % size
    return res if turn % 2 == 0 else size - res - 1


def _correction(frame_to_save_, pred):
    """liplsreal._correction, vectorized: 20px edge feather on bottom/left/right
    + green-screen-ish mask (keep original where G-B>60) for a seamless paste.
    Same math as the original triple python loop but ~6x cheaper (the loops were
    ~13ms/frame at 384 — the single biggest CPU cost in paste-back)."""
    p = pred.astype(np.float32)
    f = frame_to_save_.astype(np.float32)
    H, W = p.shape[:2]
    # bottom 20 rows: row H-1-j -> j/20*pred + (20-j)/20*frame
    j = np.arange(min(20, H)); rows = H - 1 - j
    a = (j / 20.0)[:, None, None]
    p[rows] = a * p[rows] + (1.0 - a) * f[rows]
    # left cols 20..1: col 20-j -> (20-j)/20*pred + j/20*frame
    j = np.arange(min(20, W)); cols = 20 - j; ok = cols < W; cols = cols[ok]; jj = j[ok]
    a = ((20 - jj) / 20.0)[None, :, None]
    p[:, cols] = a * p[:, cols] + (1.0 - a) * f[:, cols]
    # right cols W-1..W-20: col W-1-j -> j/20*pred + (20-j)/20*frame
    j = np.arange(min(20, W)); cols = W - 1 - j
    a = (j / 20.0)[None, :, None]
    p[:, cols] = a * p[:, cols] + (1.0 - a) * f[:, cols]
    keep = ((f[:, :, 1] - f[:, :, 2]) > 60)[:, :, None]
    return np.where(keep, f, p).astype(np.uint8)


class Wav2LipLSEngine:
    def __init__(self, avatar_dir, ckpt, face_size, hubert_dir=None,
                 batch_size=16, l=10, r=10, fps=25, device=None,
                 infer_silence=False):
        os.environ["WAV2LIPLS_FACE_SIZE"] = str(int(face_size))
        if hubert_dir:
            os.environ["WAV2LIPLS_HUBERT_DIR"] = hubert_dir
        self.device = _device(device)
        self.dtype = torch.float16
        self.face_size = int(face_size)
        self.batch_size = int(batch_size)
        self.l = int(l)
        self.r = int(r)
        self.audio_fps = 50
        self.chunk = 16000 // self.audio_fps      # 320
        self.fps = int(fps)
        self.SR = 16000
        self.chunk_samples = self.batch_size * 2 * self.chunk   # 10240
        self.slice_len = self.batch_size                        # 16
        self.syncnet_T = 12
        self.audio_feat_length = [0, 5]
        # infer_silence: run the model on silent chunks too instead of emitting
        # raw base frames. Needed for ARTICULATED-mouth bakes (the base frames
        # show speaking mouths): the model hears silence and closes the mouth,
        # so mid-turn pauses stay closed without requiring a sealed-lips bake.
        # Idle (between turns) is unaffected — cored plays baked idle locally.
        self.infer_silence = bool(infer_silence)

        # model (face_size env already set) + hubert feature extractor
        from wav2lipls.models import Human
        from wav2lipls.audio2feature import Audio2Feature
        log.info("loading wav2lipLS Human (face_size=%d) ckpt=%s", self.face_size, ckpt)
        model = Human(sr=False)
        sd = torch.load(ckpt, map_location="cpu")
        sd = sd.get("state_dict", sd)
        model.load_state_dict({k.replace("module.", "").replace("_orig_mod.", ""): v for k, v in sd.items()})
        self.model = model.to(self.device, dtype=self.dtype).eval()
        self.audio_proc = Audio2Feature()

        self.frame_list, self.face_list, self.coord_list = load_avatar_data(avatar_dir)
        self.modelres = self.face_list[0].shape[0]
        if self.modelres != self.face_size:
            raise SystemExit(f"face_imgs are {self.modelres}px but --face-size={self.face_size}; "
                             f"bake the avatar with --img_size {self.face_size}")
        h, w = self.frame_list[0].shape[:2]
        self.H, self.W = h - (h % 2), w - (w % 2)

        self._gen = 0
        self._index = 0
        self._chunk_idx = 0
        self._infernum = 0
        self._hn = None
        self._cn = None
        self._in_q = Queue()
        self._feat_q = Queue()      # stage1 HuBERT -> stage2 GPU forward
        self._paste_q = Queue()     # stage2 GPU forward -> stage3 CPU paste-back
        self._out_q = Queue()
        self._quit = threading.Event()
        self._feat_thread = None
        self._gpu_thread = None
        self._paste_thread = None

    @property
    def width(self):
        return self.W

    @property
    def height(self):
        return self.H

    def warmup(self, n_chunks=2):
        hn = torch.zeros(2, 1, 512, device=self.device, dtype=self.dtype)
        cn = torch.zeros(2, 1, 512, device=self.device, dtype=self.dtype)
        feat = torch.ones(1, 10, 1024, device=self.device, dtype=self.dtype)
        img = torch.ones(1, 6, self.face_size, self.face_size, device=self.device, dtype=self.dtype)
        with torch.no_grad():
            for _ in range(max(1, n_chunks)):
                self.model(feat, img, hn, cn)
        self.audio_proc.get_hubert_from_16k_speech(np.zeros(16000, dtype=np.float32))
        if self.device.startswith("cuda"):
            torch.cuda.synchronize()
        log.info("wav2lipLS warmup done (batch=%d res=%d)", self.batch_size, self.face_size)

    def load_avatar(self, avatar_dir, **_):
        self.frame_list, self.face_list, self.coord_list = load_avatar_data(avatar_dir)
        self.modelres = self.face_list[0].shape[0]
        h, w = self.frame_list[0].shape[:2]
        self.H, self.W = h - (h % 2), w - (w % 2)

    def start_session(self, session_id="s", av_chunks=True):
        self._quit.clear()
        self._gen = 0
        self._index = 0
        self._chunk_idx = 0
        self._infernum = 0
        self._hn = None
        self._cn = None
        self._drain(self._in_q)
        self._drain(self._feat_q)
        self._drain(self._paste_q)
        self._drain(self._out_q)
        # Three-stage pipeline. Profiling (640ms budget per 16-frame chunk) showed
        # the work splits as HuBERT 28ms | GPU forward 311ms | CPU paste-back 291ms.
        # GPU forward and paste-back are nearly equal and were the real cost, so
        # they get their own threads: the GPU forward of chunk N overlaps the (GIL-
        # releasing-where-it-can) paste-back of chunk N-1. Bottleneck drops from
        # 311+291 serial to max(311,291) -> ~2x realtime headroom at 384.
        self._feat_thread = threading.Thread(target=self._feat_loop, name="wav2lipls-feat", daemon=True)
        self._gpu_thread = threading.Thread(target=self._gpu_loop, name="wav2lipls-gpu", daemon=True)
        self._paste_thread = threading.Thread(target=self._paste_loop, name="wav2lipls-paste", daemon=True)
        self._feat_thread.start()
        self._gpu_thread.start()
        self._paste_thread.start()
        log.info("wav2lipLS session start: %s", session_id)

    def push_audio(self, chunk):
        self._in_q.put((np.asarray(chunk, dtype=np.float32), self._gen))

    def read_video_frame(self, timeout=0):
        try:
            if timeout and timeout > 0:
                return self._out_q.get(timeout=timeout)
            return self._out_q.get_nowait()
        except Empty:
            return None

    def interrupt(self):
        # Bump gen and flush all four queues. Each stage resets its own state
        # (rolling audio buffers in feat; _hn/_cn/_infernum in gpu) when it sees
        # the gen change, so there's no cross-thread race on the LSTM state.
        self._gen += 1
        self._drain(self._in_q)
        self._drain(self._feat_q)
        self._drain(self._paste_q)
        self._drain(self._out_q)

    def close_session(self):
        self._quit.set()
        for attr in ("_feat_thread", "_gpu_thread", "_paste_thread"):
            t = getattr(self, attr)
            if t is not None:
                t.join(timeout=2.0)
                setattr(self, attr, None)

    @staticmethod
    def _drain(q):
        try:
            while True:
                q.get_nowait()
        except Empty:
            pass

    def _base_rgb(self, idx):
        full = self.frame_list[idx][:self.H, :self.W]
        return np.ascontiguousarray(cv2.cvtColor(full, cv2.COLOR_BGR2RGB))

    def _feat_loop(self):
        """Stage 1: rolling HuBERT feature extraction. Owns frames_buf/out_audio/
        _index/_chunk_idx. Emits one work item per audio chunk into _feat_q, so it
        can race ahead while stage 2 is still inferring the previous chunk."""
        sil = lambda: np.zeros(self.chunk, dtype=np.float32)  # noqa: E731
        frames_buf = [sil() for _ in range(self.l + self.r)]
        out_audio = [(sil(), 1) for _ in range(self.r)]
        length = len(self.face_list)
        last_gen = self._gen

        while not self._quit.is_set():
            try:
                chunk, gen = self._in_q.get(timeout=0.3)
            except Empty:
                continue
            if gen != self._gen:
                continue
            if gen != last_gen:
                # interrupt: restart the rolling audio context for the new turn
                frames_buf = [sil() for _ in range(self.l + self.r)]
                out_audio = [(sil(), 1) for _ in range(self.r)]
                last_gen = gen

            af = [chunk[i * self.chunk:(i + 1) * self.chunk] for i in range(self.batch_size * 2)]
            for f in af:
                t = 0 if float(np.abs(f).max()) > 1e-4 else 1
                frames_buf.append(f)
                out_audio.append((f, t))

            # ---- HuBERT features (port of HubertASR.run_step) ----
            inputs = np.concatenate(frames_buf)
            feat = self.audio_proc.get_hubert_from_16k_speech(inputs)          # [T,1024] cpu tensor
            feat_chunks = self.audio_proc.feature2chunks(
                feature_array=feat, fps=self.audio_fps / 2, batch_size=self.batch_size,
                audio_feat_length=self.audio_feat_length, start=self.l / 2)    # batch_size x [10,1024]
            frames_buf = frames_buf[-(self.l + self.r):]

            batch_audio = [out_audio.pop(0) for _ in range(self.batch_size * 2)]
            is_silence = all(t == 1 for _, t in batch_audio)
            pcm_out = np.concatenate([f for f, _ in batch_audio]).astype(np.float32)

            idxs = [_mirror_index(length, self._index + i) for i in range(self.batch_size)]
            self._index += self.batch_size

            self._feat_q.put((gen, self._chunk_idx, feat_chunks, idxs, is_silence, pcm_out))
            self._chunk_idx += 1

    def _gpu_loop(self):
        """Stage 2: Human GPU forward only. Sole owner of the LSTM state
        (_hn/_cn/_infernum); resets on gen change (interrupt) and on silence.
        Emits the raw predicted face crop (BGR float) downstream; paste-back is
        done in stage 3 so it overlaps the next chunk's GPU forward."""
        last_gen = self._gen
        while not self._quit.is_set():
            try:
                gen, chunk_idx, feat_chunks, idxs, is_silence, pcm_out = self._feat_q.get(timeout=0.3)
            except Empty:
                continue
            if gen != self._gen:
                continue
            if gen != last_gen:
                self._hn = None
                self._cn = None
                self._infernum = 0
                last_gen = gen

            if is_silence and not self.infer_silence:
                self._infernum = 0
                self._paste_q.put((gen, chunk_idx, None, idxs, True, pcm_out))
            else:
                pred = self._gpu_forward(feat_chunks, idxs)
                self._paste_q.put((gen, chunk_idx, pred, idxs, False, pcm_out))

    def _paste_loop(self):
        """Stage 3: CPU paste-back (resize + _correction feather + BGR->RGB), or
        base idle frames on silence. Pure CPU, runs concurrently with stage 2's
        GPU work on the next chunk."""
        while not self._quit.is_set():
            try:
                gen, chunk_idx, pred, idxs, is_silence, pcm_out = self._paste_q.get(timeout=0.3)
            except Empty:
                continue
            if gen != self._gen:
                continue
            if is_silence:
                frames_rgb = np.stack([self._base_rgb(idx) for idx in idxs])
            else:
                frames_rgb = self._paste(pred, idxs)
            self._out_q.put((gen, chunk_idx, frames_rgb, pcm_out))

    @torch.inference_mode()
    def _gpu_forward(self, feat_chunks, idxs):
        """Human GPU forward for one batch -> predicted face crop [B,res,res,3]
        BGR float (0..255). Heavy conv encode/decode batched over all B frames;
        only the stateful LSTM (audioBlocks) stays serial."""
        face = np.asarray([self.face_list[i] for i in idxs])            # [B,res,res,3] BGR
        masked = face.copy()
        masked[:, self.face_size // 2:] = 0
        img = np.concatenate((masked, face), axis=3) / 255.0           # [B,res,res,6]
        img_t = torch.from_numpy(img.transpose(0, 3, 1, 2)).to(self.device, dtype=self.dtype)
        feat_t = torch.from_numpy(np.asarray(feat_chunks)).to(self.device, dtype=self.dtype)  # [B,10,1024]
        m = self.model

        feats = m.face_encoder(m.conv_in(img_t))                       # list of [B,...]
        audio_list = []
        for k in range(self.batch_size):
            if self._infernum % self.syncnet_T == 0:
                self._hn = torch.zeros(2, 1, 512, device=self.device, dtype=self.dtype)
                self._cn = torch.zeros(2, 1, 512, device=self.device, dtype=self.dtype)
            a, self._hn, self._cn = m.audioBlocks(feat_t[k:k + 1], self._hn, self._cn)
            audio_list.append(a)
            self._infernum += 1
        audio = m.fusionBlocks(torch.cat(audio_list, dim=0), feats[-1])
        x = m.face_decoder(feats[:-1], audio)
        x = m.conv_out_sr(x) if getattr(m, "sr", False) else m.conv_out(x)
        return x.float().cpu().numpy().transpose(0, 2, 3, 1) * 255.0   # [B,res,res,3] BGR float

    def _paste(self, pred, idxs):
        """Paste each predicted face crop back into its full frame with
        _correction edge-feather, returning RGB full frames [B,H,W,3]."""
        out = []
        for k, idx in enumerate(idxs):
            full = self.frame_list[idx].copy()                          # BGR
            y1, y2, x1, x2 = self.coord_list[idx]
            res_frame = cv2.resize(pred[k].astype(np.uint8), (x2 - x1, y2 - y1))
            res_frame = _correction(full[y1:y2, x1:x2], res_frame)
            full[y1:y2, x1:x2] = res_frame
            full = full[:self.H, :self.W]
            out.append(cv2.cvtColor(full, cv2.COLOR_BGR2RGB))
        return np.stack(out)
