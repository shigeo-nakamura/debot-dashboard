#!/usr/bin/env bash
# Issue the dashboard's TLS certificate from a private CA.
#
#   gen-self-signed-cert.sh [--if-missing] [--force] [--new-ca] [--no-public-ip] [DIR]
#
# DIR defaults to /opt/debot-dashboard/tls and ends up holding:
#   ca.crt / ca.key              the private CA (10 years). ca.crt is what
#                                browsers, phones and error-watch trust.
#   dashboard.crt / dashboard.key  the server certificate the CA signs.
#
# Devices trust the CA, not the server certificate, so the server
# certificate can be reissued (public IP change, expiry) without anything
# being re-trusted. Phones need this: iOS only offers "full trust" for a CA
# certificate, never for a self-signed leaf.
#
# The CA is name-constrained to IPv4 addresses, localhost and
# *.compute.amazonaws.com, so its key (which lives on this host) cannot be
# used to impersonate any other site to a device that trusts it.
#
# The server certificate names the instance's current public IPv4 / public
# DNS name (from IMDS) plus localhost. The public IP is not an Elastic IP;
# after a stop/start the next run reissues it for the new address.
#
# --if-missing  exit 0 without touching anything when the CA and the
#               server pair are in place, the certificate is valid for at
#               least 30 more days, signed by the CA, matches its key and
#               still names the current public IP; otherwise (re)issue what
#               is needed (deploy.yml runs it this way on every deploy, and
#               restarts the service afterwards).
# --force       reissue the server certificate (the CA is kept).
# --new-ca      replace the CA too. Every device has to trust the new
#               ca.crt again, and the DASHBOARD_CA_CERT secret must be
#               updated.
# --no-public-ip  allow a localhost-only certificate. Without it, failing
#               to read the public IPv4 from IMDS is an error, also for
#               --if-missing on an existing pair: a cert that cannot match
#               the address the dashboard is reached on would be restarted
#               into and break browser access.
set -euo pipefail

if_missing=0
force=0
new_ca=0
no_public_ip=0
dir=/opt/debot-dashboard/tls
for arg in "$@"; do
  case "$arg" in
    --if-missing) if_missing=1 ;;
    --force) force=1 ;;
    --new-ca) new_ca=1 ;;
    --no-public-ip) no_public_ip=1 ;;
    -*) echo "unknown option: $arg" >&2; exit 2 ;;
    *) dir="$arg" ;;
  esac
done

crt="$dir/dashboard.crt"
key="$dir/dashboard.key"
ca_crt="$dir/ca.crt"
ca_key="$dir/ca.key"
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

# A key must be its certificate's own: a pair left mismatched by an
# interrupted replacement would pass the other checks and then make the
# service fail tls.LoadX509KeyPair on restart.
pair_matches() {
  local c k
  c=$(sudo openssl x509 -in "$1" -noout -pubkey 2>/dev/null) || return 1
  k=$(sudo openssl pkey -in "$2" -pubout 2>/dev/null) || return 1
  [ -n "$c" ] && [ "$c" = "$k" ]
}

valid_30d() {
  sudo openssl x509 -in "$1" -noout -checkend $((30 * 86400)) >/dev/null 2>&1
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

# --- CA -----------------------------------------------------------------
issue_ca=0
if [ "$new_ca" = 1 ]; then
  issue_ca=1
elif ! sudo test -f "$ca_crt" || ! sudo test -f "$ca_key"; then
  echo "no CA in $dir; creating one" >&2
  issue_ca=1
elif ! valid_30d "$ca_crt"; then
  echo "$ca_crt expires within 30 days (or is unreadable); replacing the CA — devices must trust the new one" >&2
  issue_ca=1
elif ! pair_matches "$ca_crt" "$ca_key"; then
  echo "$ca_key is unreadable or does not match $ca_crt; replacing the CA — devices must trust the new one" >&2
  issue_ca=1
fi

# --- server certificate ---------------------------------------------------
issue_leaf=0
if [ "$issue_ca" = 1 ] || [ "$force" = 1 ]; then
  issue_leaf=1
elif ! sudo test -f "$crt" || ! sudo test -f "$key"; then
  issue_leaf=1
elif [ "$if_missing" != 1 ]; then
  echo "$crt already exists; pass --force to reissue it" >&2
  exit 1
elif ! valid_30d "$crt"; then
  echo "$crt expires within 30 days (or is unreadable); reissuing" >&2
  issue_leaf=1
elif ! pair_matches "$crt" "$key"; then
  echo "$key is unreadable or does not match $crt; reissuing" >&2
  issue_leaf=1
elif ! sudo openssl verify -CAfile "$ca_crt" "$crt" >/dev/null 2>&1; then
  echo "$crt is not signed by $ca_crt; reissuing" >&2
  issue_leaf=1
elif [ -n "$public_ip" ] && ! sudo openssl x509 -in "$crt" -noout -ext subjectAltName 2>/dev/null \
    | grep -qE "IP Address:${public_ip//./\\.}(,|\$)"; then
  echo "$crt does not name the current public IP $public_ip; reissuing" >&2
  issue_leaf=1
fi

if [ "$issue_ca" = 0 ] && [ "$issue_leaf" = 0 ]; then
  exit 0
fi

sudo mkdir -p "$dir"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if [ "$issue_ca" = 1 ]; then
  openssl req -x509 -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
    -days 3650 -subj "/CN=debot-dashboard private CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "nameConstraints=critical,permitted;IP:0.0.0.0/0.0.0.0,permitted;DNS:localhost,permitted;DNS:compute.amazonaws.com" \
    -keyout "$tmp/ca.key" -out "$tmp/ca.crt" 2>/dev/null
  sudo install -m 0600 -o "$owner" -g "$owner" "$tmp/ca.key" "$ca_key"
  sudo install -m 0644 -o "$owner" -g "$owner" "$tmp/ca.crt" "$ca_crt"
  echo "wrote $ca_crt"
fi

san="DNS:localhost,IP:127.0.0.1"
[ -n "$public_ip" ] && san="$san,IP:$public_ip"
[ -n "$public_dns" ] && san="$san,DNS:$public_dns"

cat > "$tmp/leaf.ext" <<EXT
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=$san
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid
EXT
# No CN: a hostname-looking CN is checked against the CA's name
# constraints like a SAN, and browsers only match on the SAN anyway.
openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -subj "/O=debot-dashboard" -keyout "$tmp/dashboard.key" -out "$tmp/dashboard.csr" 2>/dev/null
# 825 days is the longest validity iOS/macOS accept for a TLS server
# certificate, including one issued by a user-trusted CA.
sudo openssl x509 -req -in "$tmp/dashboard.csr" -CA "$ca_crt" -CAkey "$ca_key" \
  -set_serial "0x$(openssl rand -hex 16)" -days 825 -extfile "$tmp/leaf.ext" \
  -out "$tmp/dashboard.crt" 2>/dev/null

sudo install -m 0600 -o "$owner" -g "$owner" "$tmp/dashboard.key" "$key"
sudo install -m 0644 -o "$owner" -g "$owner" "$tmp/dashboard.crt" "$crt"

echo "wrote $crt (SAN: $san)"
echo "trust this CA on your devices (compare the fingerprint):"
sudo openssl x509 -in "$ca_crt" -noout -fingerprint -sha256
