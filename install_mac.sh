#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/install.yml"
if [[ ! -f "$COMPOSE_FILE" ]]; then
  COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"
fi

# Install Homebrew if missing
if ! command -v brew >/dev/null 2>&1; then
  NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

  # Load brew into current shell
  if [[ -x /opt/homebrew/bin/brew ]]; then
    eval "$(/opt/homebrew/bin/brew shellenv)"
  elif [[ -x /usr/local/bin/brew ]]; then
    eval "$(/usr/local/bin/brew shellenv)"
  fi
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Installing OrbStack..."

  # Try installing OrbStack
  if brew install --cask orbstack; then
    echo "OrbStack installed successfully"

    # Start OrbStack (background)
    open -a OrbStack || true

    echo "Waiting for OrbStack to initialize..."
    sleep 10

  else
    echo "OrbStack install failed. Falling back to Colima..."

    brew install docker colima

    # Start Colima if not running
    if ! colima status 2>/dev/null | grep -qi "running"; then
      colima start
    fi
  fi
fi

if ! docker info >/dev/null 2>&1; then
  open -a Docker || true
  open -a OrbStack || true
fi

echo "Waiting for Docker to become available..."
for _ in {1..60}; do
  if docker info >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

open_browser() {
  local url="$1"
  open "$url" >/dev/null 2>&1 || true
}

echo "Starting Towel..."
docker compose -f "$COMPOSE_FILE" up -d

echo "Waiting for http://localhost:3000 ..."
for _ in {1..60}; do
  if curl -fsS http://localhost:3000 >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

open_browser "http://localhost:3000"

# Final check
if command -v docker >/dev/null 2>&1; then
  echo "Docker is ready:"
  docker version || true
else
  echo "Docker installation failed"
  exit 1
fi