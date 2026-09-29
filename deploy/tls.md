# HTTPS with a private CA

The dashboard is protected by basic auth, and basic auth over plain HTTP sends
the password in the clear on every request. Port 8080 is open to `0.0.0.0/0`,
so the dashboard serves HTTPS itself: `-tls-cert` and `-tls-key` switch the
listener to TLS (1.2 minimum). Setting only one of the two is a startup error,
never a quiet fall back to plain HTTP.

The host has no domain name and no Elastic IP, so no public CA will issue a
certificate for it. `deploy/gen-self-signed-cert.sh` runs a small private CA
instead, in `/opt/debot-dashboard/tls/`:

| file | what | lifetime |
|---|---|---|
| `ca.crt` / `ca.key` | private CA — **this is what devices and error-watch trust** | 10 years |
| `dashboard.crt` / `dashboard.key` | server certificate signed by the CA; SANs = IMDS public IPv4 + public DNS name (when it is a `*.compute.amazonaws.com` name the CA permits) + localhost | 825 days |

Keys are `0600`; `dashboard.key` is owned by `ec2-user` (the service user), `ca.key` by root, so a compromise of the service cannot read the signing key. `deploy.yml` installs the script next to
the binary and runs it with `--if-missing` on every deploy: it creates what is
missing and reissues the server certificate when it expires within 30 days,
does not match its key, is not signed by the CA, or no longer names the current
public IP. **None of that needs anything re-trusted**, because devices trust the
CA, not the server certificate. Only replacing the CA (`--new-ca`, or the CA
itself nearing expiry) does.

Why a CA and not a self-signed server certificate: iOS only offers "full
trust" for CA certificates, so a self-signed server certificate can never be
trusted on an iPhone, and some mobile browsers do not offer a click-through at
all. Trusting a CA is also what makes reissues invisible.

The CA is **name-constrained** to IPv4 addresses, `localhost` and
`*.compute.amazonaws.com`. Its key lives on the host, so without the
constraint anyone who got that key could mint a certificate for any site your
phone would accept; with it they cannot go beyond bare IPs and EC2 hostnames.

## Trusting the CA

Get the CA certificate and check its fingerprint:

```bash
scp debot:/opt/debot-dashboard/tls/ca.crt ~/debot-dashboard-ca.crt
openssl x509 -in ~/debot-dashboard-ca.crt -noout -fingerprint -sha256
```

Compare that fingerprint with what the device shows before trusting it. Do not
add HSTS: a browser that has seen HSTS refuses to let you click through a
certificate error.

- **macOS**: open the file → Keychain Access (System) → double-click the
  certificate → Trust → "When using this certificate: Always Trust".
- **Windows**: `certmgr.msc` → Trusted Root Certification Authorities →
  Import.
- **Linux (Chrome)**: Settings → Privacy and security → Security → Manage
  certificates → Authorities → Import, tick "Trust this certificate for
  identifying websites".
- **iPhone / iPad**: send `ca.crt` to the phone (AirDrop, or mail it to
  yourself and tap the attachment, or open it from Files/iCloud Drive).
  Settings → "Profile Downloaded" → Install. Then **Settings → General →
  About → Certificate Trust Settings** → switch on full trust for
  "debot-dashboard private CA". Installing the profile alone is not enough.
- **Android**: copy `ca.crt` to the phone, then Settings → Security (and
  privacy) → More security settings → Encryption & credentials → Install a
  certificate → **CA certificate** (Chrome trusts user-installed CAs).

Open `https://<public-ip>:8080/`. The address must be one the server
certificate names (the public IPv4 or the `ec2-…compute.amazonaws.com` name).

## error-watch

`.github/workflows/error-watch.yml` polls the dashboard from GitHub Actions and
trusts exactly the CA, taken from the `DASHBOARD_CA_CERT` repo secret
(`--cacert`, never `-k`). An `https://` `DASHBOARD_URL` without that secret
fails the run. Update the secret only when the CA is replaced:

```bash
ssh debot cat /opt/debot-dashboard/tls/ca.crt \
  | gh secret set DASHBOARD_CA_CERT --repo shigeo-nakamura/debot-dashboard
```

`DASHBOARD_URL` names the public IP, so after a stop/start it has to be updated
(`https://<new-ip>:8080/api/status`); the next deploy reissues the server
certificate for the new address.

## When the public IP changes

Either deploy, or reissue right away:

```bash
/opt/debot-dashboard/gen-self-signed-cert.sh --if-missing   # installed by deploy.yml
sudo systemctl restart debot-dashboard
```

then update `DASHBOARD_URL`. Devices keep working (at the new address) without
re-trusting anything.

## Existing hosts

`deploy.yml` installs `debot-dashboard.service` only when it is missing. A host
still on plain HTTP is switched over once the TLS-capable binary is deployed:

```bash
/opt/debot-dashboard/debot-dashboard -h 2>&1 | grep -q -- -tls-cert   # new binary?
sudo sed -i 's|-listen :8080$|-listen :8080 -tls-cert /opt/debot-dashboard/tls/dashboard.crt -tls-key /opt/debot-dashboard/tls/dashboard.key|' \
  /etc/systemd/system/debot-dashboard.service
sudo systemctl daemon-reload && sudo systemctl restart debot-dashboard
curl -sk -o /dev/null -w '%{http_code}\n' https://localhost:8080/   # 401 = HTTPS + auth up
```

A host that already serves the earlier self-signed server certificate is
migrated by the first deploy of the CA version: it creates the CA and reissues
the server certificate from it. Set `DASHBOARD_CA_CERT` to the new `ca.crt`
right after that deploy, and trust `ca.crt` on your devices (the old
self-signed certificate can be removed from them).
