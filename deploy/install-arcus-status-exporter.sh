#!/usr/bin/env bash
# Install monitoring only. Never starts/restarts the trading executor.
set -euo pipefail
if [[ ${EUID} -ne 0 ]]; then
  echo "Run as root on the Arcus host" >&2
  exit 1
fi
if [[ $# -ne 2 ]]; then
  echo "Usage: $0 /path/to/arcus-status-exporter EXPECTED_SHA256" >&2
  exit 1
fi
arcus_binary=$1
arcus_digest=$2
[[ "$arcus_digest" =~ ^[a-f0-9]{64}$ ]]
printf '%s  %s\n' "$arcus_digest" "$arcus_binary" | sha256sum --check --status
arcus_deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
id arcus >/dev/null
test -r /var/lib/debot-arcus/spot-execute-once/runtime_state.json
test -r /etc/arcus-spot/config.yaml
# Stage then rename so an already-running exporter retains its executable.
install -m 0755 "$arcus_binary" /usr/local/bin/arcus-status-exporter.new
mv /usr/local/bin/arcus-status-exporter.new /usr/local/bin/arcus-status-exporter
if [[ ! -e /etc/arcus-status-exporter.env ]]; then
  install -m 0600 /dev/null /etc/arcus-status-exporter.env
  cat > /etc/arcus-status-exporter.env <<'ENV'
ARCUS_STATUS_S3_BUCKET=debot-dashboard
ARCUS_STATUS_S3_KEY=arcus-archive/status/arcus-spot.json
ENV
fi
install -m 0644 "$arcus_deploy_dir/arcus-status-exporter.service" /etc/systemd/system/arcus-status-exporter.service
install -m 0644 "$arcus_deploy_dir/arcus-status-exporter.timer" /etc/systemd/system/arcus-status-exporter.timer
systemctl daemon-reload
systemctl enable --now arcus-status-exporter.timer
systemctl start arcus-status-exporter.service
systemctl is-active --quiet arcus-status-exporter.timer
