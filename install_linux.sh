#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/install.yml"
if [[ ! -f "$COMPOSE_FILE" ]]; then
  COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"
fi

if [[ "${EUID}" -ne 0 ]]; then
  exec sudo bash "$0" "$@"
fi

export DEBIAN_FRONTEND=noninteractive

# Install Docker only if needed
if ! command -v docker >/dev/null 2>&1; then
  # Install prerequisites
  apt-get update
  apt-get install -y ca-certificates curl gnupg

  # Add Docker official GPG key and repo if missing
  install -m 0755 -d /etc/apt/keyrings
  if [[ ! -f /etc/apt/keyrings/docker.asc ]]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi

  ARCH="$(dpkg --print-architecture)"
  . /etc/os-release

  if [[ ! -f /etc/apt/sources.list.d/docker.list ]]; then
    echo \
      "deb [arch=${ARCH} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
      ${VERSION_CODENAME} stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
  fi

  # Install Docker Engine + CLI + containerd + Buildx + Compose
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi

# Enable and start Docker
systemctl enable docker
systemctl start docker

# Add invoking user to docker group if script was run via sudo
if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
  usermod -aG docker "${SUDO_USER}" || true
fi

open_browser() {
  local url="$1"
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    sudo -u "${SUDO_USER}" env DISPLAY="${DISPLAY:-}" XAUTHORITY="${XAUTHORITY:-}" xdg-open "$url" >/dev/null 2>&1 || true
  else
    xdg-open "$url" >/dev/null 2>&1 || true
  fi
}

docker compose -f "$COMPOSE_FILE" up -d

echo "Waiting for http://localhost:3000 ..."
for _ in {1..60}; do
  if curl -fsS http://localhost:3000 >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

open_browser "http://localhost:3000"

# Verify
docker version || true
docker compose version || true