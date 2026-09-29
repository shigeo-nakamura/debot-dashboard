# HTTPS with a self-signed certificate

The dashboard is protected by basic auth, and basic auth over plain HTTP sends
the password in the clear on every request. Port 8080 is open to `0.0.0.0/0`,
so the dashboard serves HTTPS itself: `-tls-cert` and `-tls-key` switch the
listener to TLS (1.2 minimum). Setting only one of the two is a startup error,
never a quiet fall back to plain HTTP.

The host has no domain name and no Elastic IP, so the certificate is
self-signed. `deploy/gen-self-signed-cert.sh` writes it to
`/opt/debot-dashboard/tls/` (key `0600`, owned by `ec2-user`; the script
itself is installed next to the binary) with the
instance's current public IPv4 and public DNS name from IMDS, plus `localhost`,
as subject alternative names. `deploy.yml` runs it with `--if-missing` on every
deploy, so a fresh host always has a pair before the unit that needs it is
installed. An existing pair is kept unless it expires within 30 days or no
longer names the current public IP; then the deploy renews it (and restarts the
service, as every deploy does) and the new certificate has to be re-trusted.

## Trusting it in the browser

The first visit shows a certificate warning. To make it go away, import
`dashboard.crt` into the OS / browser trust store:

```bash
scp debot:/opt/debot-dashboard/tls/dashboard.crt ~/debot-dashboard.crt
```

Compare the SHA-256 fingerprint the browser shows with
`openssl x509 -in ~/debot-dashboard.crt -noout -fingerprint -sha256` before
trusting it. Do not add HSTS: a browser that has seen HSTS refuses to let you
click through a certificate error, which would lock you out the day the
certificate has to be regenerated.

## When the public IP changes

The public IP is not elastic. After a stop/start the address changes and the
certificate no longer matches it. The next deploy renews it; to do it right
away:

```bash
/opt/debot-dashboard/gen-self-signed-cert.sh --force   # installed by deploy.yml
sudo systemctl restart debot-dashboard
```

then re-import the new certificate.

## Existing hosts

`deploy.yml` installs `debot-dashboard.service` only when it is missing, so a
host that already has the unit keeps serving plain HTTP after the merge. Switch
it over once the new binary is deployed (the old binary does not know
`-tls-cert` and would crash-loop on the new unit):

```bash
/opt/debot-dashboard/debot-dashboard -h 2>&1 | grep -q -- -tls-cert   # new binary?
sudo sed -i 's|-listen :8080$|-listen :8080 -tls-cert /opt/debot-dashboard/tls/dashboard.crt -tls-key /opt/debot-dashboard/tls/dashboard.key|' \
  /etc/systemd/system/debot-dashboard.service
sudo systemctl daemon-reload && sudo systemctl restart debot-dashboard
curl -sk -o /dev/null -w '%{http_code}\n' https://localhost:8080/   # 401 = HTTPS + auth up
```
