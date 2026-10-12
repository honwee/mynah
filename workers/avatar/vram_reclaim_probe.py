"""Does a one-shot empty_cache() after model load actually reclaim VRAM?

Loading unet.pth reads 3.4GB of fp32 weights and converts to fp16; the temporary
blocks go back to torch's pool but not to the driver, so the process keeps
holding them for its whole life. This measures how much a single post-load
empty_cache() gives back.

Unlike a per-session empty_cache(), this runs ONCE before serving starts, so it
cannot cost first-frame latency in steady state — the only question is whether
the reclaimed memory is real and whether it stays reclaimed after inference.

usage: python vram_reclaim_probe.py <models_dir> <musetalk_repo>
"""
import os
import subprocess
import sys

import torch

models_dir, repo = sys.argv[1], sys.argv[2]
sys.path.insert(0, repo)

from musetalk.models.unet import UNet, PositionalEncoding  # noqa: E402
from musetalk.models.vae import VAE  # noqa: E402
from transformers import WhisperModel  # noqa: E402


def smi_mib():
    out = subprocess.run(
        ["nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader"],
        capture_output=True, text=True).stdout
    for line in out.splitlines():
        pid, mem = [x.strip() for x in line.split(",")]
        if int(pid) == os.getpid():
            return mem
    return "n/a"


def report(tag):
    print(f"{tag:26} alloc={torch.cuda.memory_allocated()/2**20:7.1f}  "
          f"reserved={torch.cuda.memory_reserved()/2**20:7.1f}  smi={smi_mib()}")


device = torch.device("cuda")
vae = VAE(model_path=os.path.join(models_dir, "sd-vae"), use_float16=True)
unet = UNet(unet_config=os.path.join(models_dir, "musetalkV15/musetalk.json"),
            model_path=os.path.join(models_dir, "musetalkV15/unet.pth"),
            use_float16=True, device=device)
pe = PositionalEncoding(d_model=384).half().to(device)
whisper = WhisperModel.from_pretrained(os.path.join(models_dir, "whisper")).to(
    device=device, dtype=torch.float16).eval()
whisper.requires_grad_(False)
report("1. after full load")

before = torch.cuda.memory_reserved()
torch.cuda.empty_cache()
after = torch.cuda.memory_reserved()
report("2. after empty_cache")
print(f"   -> reclaimed {(before-after)/2**20:.0f} MiB from the allocator pool\n")

# Does it stay reclaimed once inference runs? Simulate a batch-8 UNet step
# with the real shapes: latents [B,8,32,32], audio feats [B,50,384].
lat = torch.randn(8, 8, 32, 32, device=device, dtype=unet.model.dtype)
aud = torch.randn(8, 50, 384, device=device, dtype=unet.model.dtype)
ts = torch.tensor([0], device=device)
with torch.inference_mode():
    for _ in range(3):
        out = unet.model(lat, ts, encoder_hidden_states=aud).sample
        dec = vae.decode_latents(out) if hasattr(vae, "decode_latents") else None
torch.cuda.synchronize()
report("3. after 3 inference steps")

torch.cuda.empty_cache()
report("4. after 2nd empty_cache")
print(f"\nVERDICT: steady-state floor is line 3 (post-inference). Compare to line 1:")
print(f"  load-time peak reserved : {before/2**20:.0f} MiB")
print(f"  post-inference reserved : {torch.cuda.memory_reserved()/2**20:.0f} MiB")
