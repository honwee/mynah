# syntax=docker/dockerfile:1
# Mynah MuseTalk avatar-BAKE image — a one-shot container, not a service.
#
# cored's bake processor runs this once per job (see internal/mtbake): it turns
# an uploaded talking-head video into the avatar directory the serving workers
# consume (full_imgs/, mask/, coords.pkl, mask_coords.pkl, latents.pt). The
# container exits when the bake finishes; there is no port and no compose
# service for it.
#
# Why a SEPARATE image from the serving one: face_alignment (FAN landmarks) and
# FaceParsing pull scikit-image + numba + llvmlite, roughly 400MB of wheels that
# the realtime worker never imports. requirements-musetalk.txt calls this out
# explicitly ("FaceParsing is a BAKE-time dep ... stays out of the serving
# image") — keeping that true means the engine pool's image stays small and its
# dependency surface stays exactly what the hot path needs.
#
# Build (context = repo root, needs the serving image to exist first):
#   docker compose -f docker-compose.full.yml build musetalk
#   docker build -t mynah/musetalk-bake:prod \
#     -f deploy/compose/worker-musetalk-bake.Dockerfile .
FROM mynah/musetalk:prod

# ffmpeg: the video -> PNG frames step runs INSIDE this container too, so a bake
# is one process boundary rather than two (cored never shells out to a host
# ffmpeg, which would reintroduce a host dependency the compose migration
# removed).
RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Pins mirror the host `flashhead` conda env — the environment every avatar in
# production was actually baked in. face-alignment's own dependency list asks
# for `opencv-python`, which would install a second, GUI-linked OpenCV next to
# the headless build the base image uses; --no-deps plus explicit pins keeps a
# single OpenCV and makes the version set auditable.
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --no-deps \
      face-alignment==1.4.1 \
      scikit-image==0.25.2 \
      numba==0.64.0 \
      llvmlite==0.46.0 \
      imageio==2.37.0 \
      tifffile==2025.5.10 \
      lazy_loader==0.4 \
      networkx==3.4.2 \
      packaging==25.0

# FAN's weights (s3fd detector + 2DFAN4, ~180MB) download from the internet on
# first use. The bake mounts the host's torch cache read-only at this path, so
# a normal bake never downloads anything; an unseeded host pays it once.
ENV TORCH_HOME=/root/.cache/torch

# Overridden per job by the bake processor; here so `docker run` on the image
# alone is still meaningful.
WORKDIR /app/workers/avatar
ENTRYPOINT ["python"]
CMD ["bake_musetalk_avatar.py", "--help"]
