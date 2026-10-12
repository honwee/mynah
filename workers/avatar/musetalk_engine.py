#!/usr/bin/env python
"""MuseTalk 1.5 avatar backend for cored.

Same duck-typed engine contract as FlashHeadEngine / Wav2LipLSEngine (see
avatar_common.py), so the generic AvatarSessionServicer drives it unchanged.
MuseTalk differs from the wav2lipLS backend (wav2lipls_engine.py):
  * audio features = whisper-tiny encoder hidden states, [50,384] per video
    frame (10 whisper frames x 5 layers), not HuBERT.
  * model = single-step latent inpainting: frozen sd-vae encode (baked) ->
    UNet2DConditionModel(timestep=0, audio as cross-attention) -> vae decode.
    Stateless per frame — no LSTM, nothing to reset across chunks.
  * paste-back = precomputed face-parsing mask blend (get_image_blending),
    baked per frame into the avatar dir (mask/ + mask_coords.pkl).

The avatar dir is a MuseTalk v1.5 bake (scripts.realtime_inference with
preparation=True): full_imgs/ coords.pkl latents.pt mask/ mask_coords.pkl.
The v1.5 mouth-opening knobs (extra_margin, parsing_mode=jaw, cheek widths)
are burned into that bake; the engine just replays it.

Whisper features need +-80ms of audio context around each video frame
(pad_left=pad_right=2 whisper frames per side x2 multiplier). Streaming is
handled wav2lipLS-style with a rolling buffer: each pushed chunk is held
until the next chunk supplies its 80ms right context (or a 0.15s timeout
zero-pads it at turn end), and the last 160ms of audio is kept as left
context. Whisper is re-run per chunk on the small window (padded to 30s
internally by the feature extractor — whisper-tiny encode is a few ms).
"""
import glob
import json
import logging
import os
import pickle
import sys
import threading
import time
from queue import Queue, Empty

import cv2
import numpy as np
import torch

log = logging.getLogger("avatar-worker")

SR = 16000
WHISPER_FRAME = 320          # samples per whisper feature frame (20ms)
SAMPLES_PER_VFRAME = 640     # 16000/25
AUDIO_PAD_LEFT = 2           # MuseTalk defaults (audio_padding_length_{left,right})
AUDIO_PAD_RIGHT = 2
FEAT_PER_FRAME = 2 * (AUDIO_PAD_LEFT + AUDIO_PAD_RIGHT + 1)   # 10
LCTX_F = 8                   # left-context whisper frames kept across chunks (>=4 needed)
RCTX_F = 4                   # right-context whisper frames needed past chunk end
LCTX = LCTX_F * WHISPER_FRAME     # 2560 samples (160ms)
RCTX = RCTX_F * WHISPER_FRAME     # 1280 samples (80ms)


def _device(dev=None):
    if dev:
        return dev
    return "cuda" if torch.cuda.is_available() else "cpu"


def _read_imgs(paths):
    return [cv2.imread(p) for p in paths]  # BGR uint8


def _sorted_imgs(d):
    gl = glob.glob(os.path.join(d, "*.[jpJP][pnPN]*[gG]"))
    return sorted(gl, key=lambda x: int(os.path.splitext(os.path.basename(x))[0]))


def load_avatar_data(avatar_dir):
    """Load a MuseTalk v1.5 bake. The cycle lists already contain the
    forward+reversed mirror, so plain modulo indexing ping-pongs for free."""
    with open(os.path.join(avatar_dir, "coords.pkl"), "rb") as f:
        coords = pickle.load(f)
    with open(os.path.join(avatar_dir, "mask_coords.pkl"), "rb") as f:
        mask_coords = pickle.load(f)
    frames = _read_imgs(_sorted_imgs(os.path.join(avatar_dir, "full_imgs")))
    masks = _read_imgs(_sorted_imgs(os.path.join(avatar_dir, "mask")))
    latents = torch.load(os.path.join(avatar_dir, "latents.pt"), map_location="cpu")
    info_path = os.path.join(avatar_dir, "avator_info.json")
    if os.path.exists(info_path):
        with open(info_path) as f:
            log.info("avatar info: %s", json.dumps(json.load(f), ensure_ascii=False))
    n = min(len(frames), len(coords), len(masks), len(mask_coords), len(latents))
    log.info("musetalk avatar loaded: %d frames %s, %d latents %s",
             len(frames), frames[0].shape, len(latents), tuple(latents[0].shape))
    return frames[:n], coords[:n], latents[:n], masks[:n], mask_coords[:n]


class MuseTalkEngine:
    def __init__(self, avatar_dir, models_dir, musetalk_repo, batch_size=8,
                 fps=25, device=None, infer_silence=False, max_avatars=6):
        """avatar_dir: a single path, or a {avatar_id: path} mapping to
        PRELOAD several avatars in one worker.

        Preloading is what makes per-session avatar switching free. The cost is
        almost entirely HOST RAM (decoded frames + masks, ~2.7GB per avatar);
        the GPU side is just the latents, which are ~8MB per avatar. So a worker
        can hold many avatars without eating into the VRAM budget that decides
        how many workers fit on a card.

        All avatars MUST share one resolution: cored is told the frame size once
        at Ready and builds its encoders from it, so a mid-worker switch to a
        different size would desynchronize the video pipeline."""
        # musetalk package (models/, utils/) lives in the upstream repo tree
        if musetalk_repo and musetalk_repo not in sys.path:
            sys.path.insert(0, musetalk_repo)
        from musetalk.models.vae import VAE
        from musetalk.models.unet import UNet, PositionalEncoding
        from musetalk.utils.blending import get_image_blending
        from musetalk.utils.audio_processor import AudioProcessor
        from transformers import WhisperModel
        self._blend = get_image_blending

        self.device = _device(device)
        self.dtype = torch.float16
        self.batch_size = int(batch_size)
        self.fps = int(fps)
        self.SR = SR
        self.chunk_samples = self.batch_size * SAMPLES_PER_VFRAME
        self.slice_len = self.batch_size
        self.infer_silence = bool(infer_silence)

        unet_dir = os.path.join(models_dir, "musetalkV15")
        log.info("loading MuseTalk v1.5: unet=%s vae=%s whisper=%s",
                 unet_dir, os.path.join(models_dir, "sd-vae"), os.path.join(models_dir, "whisper"))
        self.vae = VAE(model_path=os.path.join(models_dir, "sd-vae"), use_float16=True)
        self.unet = UNet(unet_config=os.path.join(unet_dir, "musetalk.json"),
                         model_path=os.path.join(unet_dir, "unet.pth"),
                         use_float16=True, device=self.device)
        self.pe = PositionalEncoding(d_model=384).half().to(self.device)
        self.timesteps = torch.tensor([0], device=self.device)
        self.audio_proc = AudioProcessor(feature_extractor_path=os.path.join(models_dir, "whisper"))
        self.whisper = WhisperModel.from_pretrained(os.path.join(models_dir, "whisper"))
        self.whisper = self.whisper.to(device=self.device, dtype=self.dtype).eval()
        self.whisper.requires_grad_(False)

        if isinstance(avatar_dir, dict):
            avatar_map = dict(avatar_dir)
        else:
            avatar_map = {os.path.basename(os.path.normpath(avatar_dir)): avatar_dir}

        self.avatars = {}
        for aid, adir in avatar_map.items():
            frames, coords, lat, masks, mask_coords = load_avatar_data(adir)
            self.avatars[aid] = {
                "frames": frames, "coords": coords, "masks": masks,
                "mask_coords": mask_coords,
                # latents are [1,8,32,32] each; keep on GPU (~8MB per avatar)
                "latents": [t.to(self.device, dtype=self.unet.model.dtype) for t in lat],
            }
        if not self.avatars:
            raise SystemExit("no avatars loaded")

        dims = {aid: a["frames"][0].shape[:2] for aid, a in self.avatars.items()}
        if len(set(dims.values())) > 1:
            raise SystemExit(
                "preloaded avatars must all share one resolution (cored builds its "
                f"encoders from the size reported once at Ready): {dims}")

        # Preloaded entries are pinned: on-demand caching may evict others but
        # never the ones the operator explicitly asked to keep hot.
        self._preloaded = set(self.avatars)
        self._lru = list(self.avatars)
        self.max_avatars = max(len(self.avatars), int(max_avatars))
        self.default_avatar = next(iter(self.avatars))
        self._activate(self.default_avatar)
        h, w = self.frame_list[0].shape[:2]
        self.H, self.W = h - (h % 2), w - (w % 2)
        log.info("musetalk avatars preloaded: %s (default=%s)",
                 list(self.avatars), self.default_avatar)

        self._gen = 0
        self._index = 0
        self._chunk_idx = 0
        self._in_q = Queue()
        self._feat_q = Queue()      # stage1 whisper -> stage2 GPU forward
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
        pcm = np.zeros(LCTX + self.chunk_samples + RCTX, dtype=np.float32)
        with torch.no_grad():
            for _ in range(max(1, n_chunks)):
                feats = self._whisper_window(pcm, self.batch_size)
                idxs = list(range(min(self.batch_size, len(self.latent_list))))
                self._forward(feats, idxs)
        if self.device.startswith("cuda"):
            torch.cuda.synchronize()
        log.info("musetalk warmup done (batch=%d)", self.batch_size)

    def load_avatar(self, avatar_dir, **_):
        (self.frame_list, self.coord_list, lat, self.mask_list,
         self.mask_coord_list) = load_avatar_data(avatar_dir)
        self.latent_list = [t.to(self.device, dtype=self.unet.model.dtype) for t in lat]
        h, w = self.frame_list[0].shape[:2]
        self.H, self.W = h - (h % 2), w - (w % 2)

    def _load_on_demand(self, path):
        """Load a bake cored asked for that was not preloaded.

        This is what makes cored the owner of avatar selection: it can point a
        session at ANY bake directory, not only the ones named on the worker's
        command line. The cost is real — ~15s to decode 484 PNGs — so it stalls
        the session that triggers it. Preloading (--avatar-dir) is the cache
        that keeps the common cases instant; this is the fallback that keeps
        the system correct when the cache misses.

        Returns the cache key, or None when the path is unusable."""
        if not os.path.isdir(path):
            return None
        try:
            frames, coords, lat, masks, mask_coords = load_avatar_data(path)
        except Exception as e:  # noqa: BLE001
            log.warning("on-demand avatar load failed for %s: %s", path, e)
            return None

        h, w = frames[0].shape[:2]
        want = (self.H, self.W)
        got = (h - (h % 2), w - (w % 2))
        if want != got:
            # cored built its encoders from the size reported at Ready; a
            # different one here would desynchronize the video pipeline.
            log.warning("on-demand avatar %s is %s but this worker serves %s — refusing",
                        path, got, want)
            return None

        # Bound memory: each avatar costs ~1.4GB of host RAM (frames + masks).
        # Evict the least recently used non-preloaded entry when over budget.
        while len(self.avatars) >= self.max_avatars:
            victim = None
            for aid in self._lru:
                if aid not in self._preloaded:
                    victim = aid
                    break
            if victim is None:
                log.warning("avatar cache full of preloaded entries; not caching %s", path)
                break
            self._lru.remove(victim)
            self.avatars.pop(victim, None)
            log.info("evicted cached avatar %s", victim)

        self.avatars[path] = {
            "frames": frames, "coords": coords, "masks": masks,
            "mask_coords": mask_coords,
            "latents": [t.to(self.device, dtype=self.unet.model.dtype) for t in lat],
        }
        log.info("on-demand avatar loaded: %s", path)
        return path

    def preload_avatar(self, avatar):
        """Make an avatar resident without starting a session.

        Returns True when it was already loaded (no work done). Called by the
        control plane at avatar-binding time so the first visitor never pays
        the ~15s decode.
        """
        avatar = (avatar or "").strip()
        if not avatar:
            raise ValueError("empty avatar")
        if avatar in self.avatars:
            if avatar in self._lru:
                self._lru.remove(avatar)
            self._lru.append(avatar)
            return True
        key = self._load_on_demand(avatar)
        if key is None:
            raise ValueError(f"cannot load avatar {avatar!r}")
        self._lru.append(key)
        return False

    def _activate(self, avatar_id):
        """Point the render lists at one preloaded avatar. This is the whole
        cost of switching: rebinding references, no I/O and no GPU work."""
        a = self.avatars[avatar_id]
        self.frame_list = a["frames"]
        self.coord_list = a["coords"]
        self.mask_list = a["masks"]
        self.mask_coord_list = a["mask_coords"]
        self.latent_list = a["latents"]
        self.active_avatar = avatar_id

    def start_session(self, session_id="s", av_chunks=True, avatar=None):
        # Per-session avatar switch. Safe to rebind here because a worker serves
        # exactly one session at a time, so no render thread is running yet.
        want = (avatar or "").strip()
        if want and want != getattr(self, "active_avatar", None):
            key = want if want in self.avatars else None
            if key is None and os.path.isdir(want):
                # cored sent a bake path this worker has not preloaded.
                t0 = time.time()
                key = self._load_on_demand(want)
                if key:
                    log.info("avatar %s loaded on demand in %.1fs (preload it via "
                             "--avatar-dir to make this instant)", want, time.time() - t0)
            if key:
                self._activate(key)
                if key in self._lru:
                    self._lru.remove(key)
                self._lru.append(key)
                log.info("musetalk avatar -> %s", key)
            else:
                log.warning("unknown avatar %r; keeping %s (available: %s)",
                            want, self.active_avatar, list(self.avatars))
        self._quit.clear()
        self._gen = 0
        self._index = 0
        self._chunk_idx = 0
        for q in (self._in_q, self._feat_q, self._paste_q, self._out_q):
            self._drain(q)
        self._feat_thread = threading.Thread(target=self._feat_loop, name="musetalk-feat", daemon=True)
        self._gpu_thread = threading.Thread(target=self._gpu_loop, name="musetalk-gpu", daemon=True)
        self._paste_thread = threading.Thread(target=self._paste_loop, name="musetalk-paste", daemon=True)
        self._feat_thread.start()
        self._gpu_thread.start()
        self._paste_thread.start()
        log.info("musetalk session start: %s", session_id)

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
        # Bump gen and flush; the feat stage resets its rolling audio context
        # when it sees the gen change. GPU stage is stateless.
        self._gen += 1
        for q in (self._in_q, self._feat_q, self._paste_q, self._out_q):
            self._drain(q)

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

    # ---- stage 1: whisper features ----

    @torch.inference_mode()
    def _whisper_window(self, window_pcm, n_frames):
        """Whisper features for one chunk window [LCTX | chunk | RCTX] ->
        [n_frames, 50, 384] fp16. Port of AudioProcessor.get_whisper_chunk
        restricted to a short window: the feature extractor zero-pads to 30s,
        the encoder runs once, and per-frame clips are sliced with the same
        [2j-4, 2j+6) context math (offsets shifted by the LCTX_F prefix)."""
        input_features = self.audio_proc.feature_extractor(
            window_pcm, return_tensors="pt", sampling_rate=SR).input_features
        input_features = input_features.to(self.device, dtype=self.dtype)
        hs = self.whisper.encoder(input_features, output_hidden_states=True).hidden_states
        feats = torch.stack(hs, dim=2)                       # [1, 1500, 5, 384]
        actual = len(window_pcm) // WHISPER_FRAME
        feats = feats[:, :actual]
        clips = []
        for j in range(n_frames):
            s = LCTX_F + 2 * j - 2 * AUDIO_PAD_LEFT
            clips.append(feats[:, s:s + FEAT_PER_FRAME])     # [1, 10, 5, 384]
        out = torch.cat(clips, dim=0)                        # [B, 10, 5, 384]
        return out.reshape(out.shape[0], -1, out.shape[-1])  # [B, 50, 384]

    def _feat_loop(self):
        """Rolling whisper features. Holds each chunk until the next one
        supplies its 80ms right context (zero-padded on 0.15s starvation, i.e.
        turn tail). Keeps the last 160ms as left context across chunks."""
        left_hist = np.zeros(LCTX, dtype=np.float32)
        held = None                          # (chunk_pcm, gen) awaiting right ctx
        last_gen = self._gen

        def process(chunk, right_ctx, gen):
            nonlocal left_hist
            window = np.concatenate([left_hist, chunk, right_ctx])
            is_silence = float(np.abs(chunk).max()) <= 1e-4
            idxs = [(self._index + i) % len(self.frame_list) for i in range(self.batch_size)]
            self._index += self.batch_size
            if is_silence and not self.infer_silence:
                feats = None
            else:
                feats = self._whisper_window(window, self.batch_size)
            left_hist = np.concatenate([left_hist, chunk])[-LCTX:]
            self._feat_q.put((gen, self._chunk_idx, feats, idxs, is_silence, chunk))
            self._chunk_idx += 1

        while not self._quit.is_set():
            try:
                chunk, gen = self._in_q.get(timeout=0.05)
            except Empty:
                if held is not None and held[1] == self._gen:
                    if time.monotonic() - held[2] > 0.15:
                        process(held[0], np.zeros(RCTX, dtype=np.float32), held[1])
                        held = None
                continue
            if gen != self._gen:
                continue
            if gen != last_gen:
                left_hist = np.zeros(LCTX, dtype=np.float32)
                held = None
                last_gen = gen
            if held is not None:
                process(held[0], chunk[:RCTX], held[1])
            held = (chunk, gen, time.monotonic())

    # ---- stage 2: GPU forward ----

    @torch.inference_mode()
    def _forward(self, feats, idxs):
        """UNet single-step + VAE decode -> [B,256,256,3] BGR uint8."""
        audio = self.pe(feats.to(self.device, dtype=self.dtype))
        latents = torch.cat([self.latent_list[i] for i in idxs], dim=0)
        pred = self.unet.model(latents, self.timesteps,
                               encoder_hidden_states=audio).sample
        pred = pred.to(dtype=self.vae.vae.dtype)
        return self.vae.decode_latents(pred)

    def _gpu_loop(self):
        while not self._quit.is_set():
            try:
                gen, chunk_idx, feats, idxs, is_silence, pcm = self._feat_q.get(timeout=0.3)
            except Empty:
                continue
            if gen != self._gen:
                continue
            if feats is None:
                self._paste_q.put((gen, chunk_idx, None, idxs, True, pcm))
            else:
                recon = self._forward(feats, idxs)
                self._paste_q.put((gen, chunk_idx, recon, idxs, False, pcm))

    # ---- stage 3: CPU paste-back ----

    def _paste_loop(self):
        while not self._quit.is_set():
            try:
                gen, chunk_idx, recon, idxs, is_silence, pcm = self._paste_q.get(timeout=0.3)
            except Empty:
                continue
            if gen != self._gen:
                continue
            if is_silence:
                frames_rgb = np.stack([self._base_rgb(i) for i in idxs])
            else:
                frames_rgb = self._paste(recon, idxs)
            self._out_q.put((gen, chunk_idx, frames_rgb, pcm))

    def _paste(self, recon, idxs):
        out = []
        for k, idx in enumerate(idxs):
            out.append(self._paste_one(recon[k], idx))
        return np.stack(out)

    def _paste_one(self, face, idx):
        """Vectorized port of get_image_blending: alpha-blend the predicted
        face into the (precomputed) parsing-mask crop region only. The PIL
        original converted the full 1280x720 body to/from Image objects per
        frame (~83ms); operating on the crop region alone is ~10x cheaper."""
        full = self.frame_list[idx].copy()                    # BGR
        x1, y1, x2, y2 = self.coord_list[idx]
        xs, ys, xe, ye = self.mask_coord_list[idx]
        H, W = full.shape[:2]
        cxs, cys = max(0, xs), max(0, ys)
        cxe, cye = min(W, xe), min(H, ye)

        res = cv2.resize(face.astype(np.uint8), (x2 - x1, y2 - y1))
        region = full[cys:cye, cxs:cxe]
        faced = region.copy()
        fy1, fy2 = y1 - cys, y2 - cys
        fx1, fx2 = x1 - cxs, x2 - cxs
        faced[fy1:fy2, fx1:fx2] = res

        mask = self.mask_list[idx]
        if mask.ndim == 3:
            mask = mask[:, :, 0]
        # mask is sized to the un-clamped crop box; slice to the clamped view
        m = mask[cys - ys:cye - ys, cxs - xs:cxe - xs].astype(np.float32)[..., None] / 255.0
        full[cys:cye, cxs:cxe] = (m * faced + (1.0 - m) * region).astype(np.uint8)
        full = full[:self.H, :self.W]
        return cv2.cvtColor(full, cv2.COLOR_BGR2RGB)
