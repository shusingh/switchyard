#!/usr/bin/env bash
# Installs vLLM into a dedicated virtual environment inside WSL2 (or any
# Linux host with an NVIDIA GPU). Safe to re-run: existing installs are
# upgraded in place.
set -euo pipefail

VENV_DIR="${VENV_DIR:-$HOME/venvs/vllm}"
PYTHON_VERSION="${PYTHON_VERSION:-3.12}"

export PATH="$HOME/.local/bin:$PATH"

if ! command -v uv >/dev/null 2>&1; then
  curl -LsSf https://astral.sh/uv/install.sh | sh
fi

if [[ ! -d "$VENV_DIR" ]]; then
  uv venv "$VENV_DIR" --python "$PYTHON_VERSION"
fi

# shellcheck source=/dev/null
source "$VENV_DIR/bin/activate"

# --torch-backend=auto selects the PyTorch build that matches the installed
# NVIDIA driver.
uv pip install --upgrade vllm --torch-backend=auto

python - <<'PY'
import torch
import vllm

print(f"vllm {vllm.__version__}")
print(f"torch {torch.__version__} (cuda {torch.version.cuda})")
print(f"gpu available: {torch.cuda.is_available()}")
if torch.cuda.is_available():
    print(f"gpu: {torch.cuda.get_device_name(0)}")
PY
