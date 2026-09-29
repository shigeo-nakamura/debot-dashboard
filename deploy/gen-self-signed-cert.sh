#!/usr/bin/env bash
# Generate the dashboard's self-signed TLS certificate.
#
#   gen-self-signed-cert.sh [--if-missing] [--force] [--no-public-ip] [DIR]
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
# --no-public-ip  allow a localhost-only certificate. Without it, failing
#               to read the public IPv4 from IMDS is an error, also for
#               --if-missing on an existing pair: a cert that cannot match
#               the address the dashboard is reached on would be restarted
#               into and break browser access.
set -euo pipefail

if_missing=0
force=0
no_public_ip=0
dir=/opt/debot-dashboard/tls
for arg in "$@"; do
  case "$arg" in
    --if-missing) if_missing=1 ;;
    --force) force=1 ;;
    --no-public-ip) no_public_ip=1 ;;
    -*) echo "unknown option: $arg" >&2; exit 2 ;;
    *) dir="$arg" ;;
  esac
done

crt="$dir/dashboard.crt"
key="$dir/dashboard.key"
owner="${DASHBOARD_USER:-ec2-user}"

imds_once() {
  local token
  token=$(curl -sf -m 2 -X PUT http://169.254.169.254/latest/api/token \
    -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' 2>/dev/null) || return 1
  curl -sf -m 2 -H "X-aws-ec2-metadata-token: $token" \
    "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null
}

# Empty output means "not available after retries"; callers decide
# whether that is fatal.
imds() {
  local out attempt
  for attempt in 1 2 3; do
    if out=$(imds_once "$1") && [ -n "$out" ]; then
      printf '%s' "$out"
      return 0
    fi
    sleep 1
  done
  return 0
}

# The key must be the certificate's own: a pair left mismatched by an
# interrupted replacement would pass the certificate checks and then
# make the service fail tls.LoadX509KeyPair on restart.
pair_matches() {
  local c k
  c=$(openssl x509 -in "$crt" -noout -pubkey 2>/dev/null) || return 1
  k=$(openssl pkey -in "$key" -pubout 2>/dev/null) || return 1
  [ -n "$c" ] && [ "$c" = "$k" ]
}

public_ip=$(imds public-ipv4)
public_dns=$(imds public-hostname)

# Checked before an existing pair is accepted too: without the current
# address there is no way to tell whether the pair still matches it (a
# stop/start moves the public IP).
if [ -z "$public_ip" ] && [ "$no_public_ip" != 1 ]; then
  echo "could not read the public IPv4 from IMDS; refusing to write or accept a certificate that may not match the dashboard's address (pass --no-public-ip for a localhost-only one)" >&2
  exit 1
fi

if [ -f "$crt" ] && [ -f "$key" ]; then
  if [ "$if_missing" = 1 ]; then
    if ! openssl x509 -in "$crt" -noout -checkend $((30 * 86400)) >/dev/null 2>&1; then
      echo "$crt expires within 30 days (or is unreadable); renewing" >&2
    elif ! pair_matches; then
      echo "$key is unreadable or does not match $crt; renewing" >&2
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
