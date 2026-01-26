# debot-dashboard

Simple SSM-backed status dashboard for debot services.

## Requirements
- SSM agent online on target instances.
- IAM permissions on the dashboard host:
  - ssm:SendCommand
  - ssm:GetCommandInvocation

## Config
Copy the example file and fill in your instance IDs and status.json paths.

```
cp config.example.yaml config.yaml
```

## Run

```
go run . -config config.yaml -listen :8080
```

Open http://localhost:8080 in a browser.

## debot status.json

Each debot service should write a unique status file. Set one of:

- DEBOT_STATUS_ID=debot00 (default path becomes $HOME/debot_status/debot00/status.json)
- DEBOT_STATUS_PATH=/home/ec2-user/debot_status/debot00/status.json
