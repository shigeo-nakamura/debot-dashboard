# debot-dashboard

SSM-backed status dashboard for debot services.

## Quick start

```
cp config.example.yaml config.yaml
# edit config.yaml

go run . -config config.yaml -listen :8080
```

Open `http://localhost:8080` in a browser.

## debot status.json

Each debot service should write a unique status file. Set one of:

- `DEBOT_STATUS_ID=debot00` (default path becomes `$HOME/debot_status/debot00/status.json`)
- `DEBOT_STATUS_PATH=/home/ec2-user/debot_status/debot00/status.json`

## Deploy with GitHub Actions (S3 + SSM)

### 1) Create an S3 bucket
Create a bucket in `eu-central-1` and note the name (must be globally unique).

### 2) IAM role for GitHub Actions (OIDC)
Create an IAM role that GitHub Actions can assume and grant:
- `s3:PutObject`, `s3:GetObject`, `s3:ListBucket` on your bucket
- `ssm:SendCommand`, `ssm:GetCommandInvocation` on the target instance and document

Set repo secrets:
- `AWS_ROLE_ARN` (OIDC role)
- `S3_BUCKET`
- `INSTANCE_ID` (e.g., `i-0c08fba996bc21879`)

### 3) Instance IAM role permissions
The instance that runs the dashboard needs to read from S3 and call SSM.
Attach a policy to the instance role with:
- `s3:GetObject` on your bucket
- `ssm:SendCommand`, `ssm:GetCommandInvocation`

### 4) Place config.yaml on the instance
```
sudo mkdir -p /opt/debot-dashboard
sudo cp /path/to/config.yaml /opt/debot-dashboard/config.yaml
```

### 5) Open the port
Allow inbound access to port `8080` (or the port you choose) on the instance security group.

### 6) Deploy
Push to `main` and the workflow `.github/workflows/deploy.yml` will:
- build a Linux binary
- upload it to S3
- deploy and restart via SSM

## Manual install (one-time)
On the instance:

```
./deploy/install.sh
```

This installs the systemd unit and starts `debot-dashboard`.
