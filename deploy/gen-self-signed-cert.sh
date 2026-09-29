#!/usr/bin/env bash
# Generate the dashboard's self-signed TLS certificate.
#
#   gen-self-signed-cert.sh [--if-missing] [--force] [DIR]
#
# DIR defaults to /opt/debot-dashboard/tls. The certificate names the
# instance's current public IPv4 / public DNS name (from IMDS) plus
# localhost, so a browser that has been told to trust it also matches the
# address it is reached on. The public IP is not an Elastic IP: after a
# stop/start it changes, and the certificate has to be regenerated with
# --force (and re-trusted) or the browser will reject the name.
#
# --if-missing  exit 0 without touching anything when both files exist,
#               the certificate is valid for at least 30 more days and
#               it still names the current public IP; otherwise renew it
#               (deploy.yml runs it this way on every deploy, and restarts
#               the service afterwards). A renewed certificate has to be
#               re-trusted in the browser.
# --force       overwrite an existing pair.
set -euo pipefail

if_missing=0
force=0
dir=/opt/debot-dashboard/tls
for arg in "$@"; do
  case "$arg" in
    --if-missing) if_missing=1 ;;
    --force) force=1 ;;
    -*) echo "unknown option: $arg" >&2; exit 2 ;;
    *) dir="$arg" ;;
  esac
done

crt="$dir/dashboard.crt"
key="$dir/dashboard.key"
owner="${DASHBOARD_USER:-ec2-user}"

imds() {
  local token
  token=$(curl -sf -m 2 -X PUT http://169.254.169.254/latest/api/token \
    -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' 2>/dev/null) || return 0
  curl -sf -m 2 -H "X-aws-ec2-metadata-token: $token" \
    "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null || true
}

public_ip=$(imds public-ipv4)
public_dns=$(imds public-hostname)

if [ -f "$crt" ] && [ -f "$key" ]; then
  if [ "$if_missing" = 1 ]; then
    if ! openssl x509 -in "$crt" -noout -checkend $((30 * 86400)) >/dev/null 2>&1; then
      echo "$crt expires within 30 days (or is unreadable); renewing" >&2
    elif [ -n "$public_ip" ] && ! openssl x509 -in "$crt" -noout -ext subjectAltName 2>/dev/null \
        | grep -qE "IP Address:${public_ip//./\\.}(,|\$)"; then
      echo "$crt does not name the current public IP $public_ip; renewing" >&2
    else
      exit 0
    fi
  elif [ "$force" != 1 ]; then
    echo "$crt already exists; pass --force to replace it" >&2
    exit 1
  fi
fi

san="DNS:localhost,IP:127.0.0.1"
[ -n "$public_ip" ] && san="$san,IP:$public_ip"
[ -n "$public_dns" ] && san="$san,DNS:$public_dns"

sudo mkdir -p "$dir"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -days 825 -subj "/CN=debot-dashboard" \
  -addext "subjectAltName=$san" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "extendedKeyUsage=serverAuth" \
  -keyout "$tmp/dashboard.key" -out "$tmp/dashboard.crt" 2>/dev/null

sudo install -m 0600 -o "$owner" -g "$owner" "$tmp/dashboard.key" "$key"
sudo install -m 0644 -o "$owner" -g "$owner" "$tmp/dashboard.crt" "$crt"

echo "wrote $crt (SAN: $san)"
openssl x509 -in "$crt" -noout -fingerprint -sha256
