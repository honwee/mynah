"""Compare MuseTalk VRAM between the container image and the host conda env.

Same weights, same dtype, same device — so any gap is environment, not workload.
Prints torch's own accounting (allocated = live tensors, reserved = allocator
pool) next to the process's nvidia-smi footprint, which additionally includes
the CUDA context and cuDNN/cuBLAS workspaces that torch does not count.

usage: python vram_probe.py <models_dir> <musetalk_repo>
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
    """This process's VRAM as the driver sees it (context + workspaces + pool)."""
    out = subprocess.run(
        ["nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader"],
        capture_output=True, text=True).stdout
    for line in out.splitlines():
        pid, mem = [x.strip() for x in line.split(",")]
        if int(pid) == os.getpid():
            return mem
    return "n/a"


def report(tag):
    print(f"{tag:22} torch_alloc={torch.cuda.memory_allocated()/2**20:8.1f} MiB  "
          f"torch_reserved={torch.cuda.memory_reserved()/2**20:8.1f} MiB  "
          f"nvidia_smi={smi_mib()}")


print(f"torch {torch.__version__}  cuda {torch.version.cuda}  "
      f"cudnn {torch.backends.cudnn.version()}")
print(f"cudnn.benchmark={torch.backends.cudnn.benchmark}  "
      f"tf32_matmul={torch.backends.cuda.matmul.allow_tf32}")

torch.cuda.init()
report("after cuda init")

vae = VAE(model_path=os.path.join(models_dir, "sd-vae"), use_float16=True)
report("after VAE")

unet = UNet(unet_config=os.path.join(models_dir, "musetalkV15/musetalk.json"),
            model_path=os.path.join(models_dir, "musetalkV15/unet.pth"),
            use_float16=True, device=torch.device("cuda"))
report("after UNet")

pe = PositionalEncoding(d_model=384).half().to("cuda")  # noqa: F841
whisper = WhisperModel.from_pretrained(os.path.join(models_dir, "whisper")).to(
    device="cuda", dtype=torch.float16).eval()
whisper.requires_grad_(False)
report("after Whisper")

print(f"\nSUMMARY  reserved={torch.cuda.memory_reserved()/2**20:.0f} MiB  "
      f"smi={smi_mib()}  (smi - reserved = CUDA context + workspaces)")
