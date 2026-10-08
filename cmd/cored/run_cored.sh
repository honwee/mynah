#!/bin/bash
# Run cored (P2): pion signaling + media egress, driving the AvatarEngine worker.
#   ./run_cored.sh            # defaults: :8020, worker 127.0.0.1:9401, tts :8091
# Start the worker first (workers/avatar/run_avatar_worker.sh) and let it warm up.
cd "$(cd "$(dirname "$0")/../.." && pwd)" # repo root, wherever this is checked out
exec ./bin/cored \
  --listen :8020 \
  --worker 127.0.0.1:9401 \
  --tts http://127.0.0.1:8091 \
  --voice vivian \
  --web web/user \
  --stun ""
