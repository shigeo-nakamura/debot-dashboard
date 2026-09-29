#!/usr/bin/env bash
set -euo pipefail

APP_DIR="/opt/debot-dashboard"
SERVICE_NAME="debot-dashboard"

sudo mkdir -p "${APP_DIR}"

if [ -f ./debot-dashboard ]; then
  sudo install -m 0755 ./debot-dashboard "${APP_DIR}/debot-dashboard"
fi

if [ ! -f "${APP_DIR}/config.yaml" ]; then
  sudo install -m 0644 ./config.example.yaml "${APP_DIR}/config.yaml"
  echo "Created ${APP_DIR}/config.yaml - edit it before starting."
fi

# The unit serves HTTPS from this pair; without it the service would
# refuse to start.
./deploy/gen-self-signed-cert.sh --if-missing "${APP_DIR}/tls"

sudo install -m 0644 ./deploy/debot-dashboard.service "/etc/systemd/system/${SERVICE_NAME}.service"
sudo systemctl daemon-reload
sudo systemctl enable --now "${SERVICE_NAME}"
