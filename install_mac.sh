#!/usr/bin/env bash
set -euo pipefail

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

# If docker already exists, exit
if command -v docker >/dev/null 2>&1; then
  echo "Docker already installed. Skipping..."
  exit 0
fi

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

# Final check
if command -v docker >/dev/null 2>&1; then
  echo "Docker is ready:"
  docker version || true
else
  echo "Docker installation failed"
  exit 1
fi