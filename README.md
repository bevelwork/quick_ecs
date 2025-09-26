# Quick ECS

A simple Go CLI for quickly inspecting and managing Amazon ECS services. It helps you list clusters and services, inspect configs, review health checks, stream logs, exec into containers, adjust capacity, and more — with fast, readable output designed for day-to-day operations and troubleshooting.

## ✨ Features

- Interactive selection of ECS clusters and services
- Task definition history (latest up to 10) with indicators: latest, default, in-use
- Service configuration view (deployment config, load balancer, network info)
- Security groups view
  - Shows ALB and Task security groups
  - Aggregates inbound/outbound rules across SGs
  - Compact rule owners list with colored direction tags [In]/[Out]
  - Prints SG names alongside IDs
- Health checks view
  - Task definition container health checks (command, intervals/timeouts, estimated time to unhealthy)
  - ALB Target Group health checks (protocol, path/port, thresholds, matcher)
- Logs streaming (CloudWatch Logs)
- Exec into running container (ECS Exec)
- Update image (register new task def and deploy)
- Update capacity (min/desired/max with rolling constraints)
- Force new deployment
- Private mode (hide account info for screenshots)

## Demo (examples)

```bash
quick_ecs --version
quick_ecs --region us-east-1
```

During execution you’ll be prompted to select a cluster and service, then an action from the alphabetized menu:

- Capacity, Check configuration, Exec, Force update, Health checks,
  Image, Logs, Service configuration, Task definition history

You can also type shortcuts (e.g., `e` for Exec, `t` for Task defs, `s` for Service config, `h` for Health checks).

## Install

### Required Software
- Go 1.24.4 or later
- AWS CLI v2 (required for ECS Exec integration)

### Install with Go
```bash
go install github.com/bevelwork/quick_ecs@latest
quick_ecs --version
```

### Build from Source
```bash
git clone https://github.com/bevelwork/quick_ecs.git
cd quick_ecs
make build # or: go build -o quick_ecs .
./quick_ecs --version
```

## Usage

```bash
# Default region (or respect your environment)
quick_ecs

# Specify region
quick_ecs --region us-east-1

# Hide account/ARN details in the header (good for screenshots)
quick_ecs --private
```

After selecting a cluster and service, choose an action:

- Image: Update container image (register new task def + optional force deploy)
- Capacity: Update service min/desired/max (respects rolling deploy constraints)
- Logs: Stream CloudWatch Logs (interactive – press Enter to fetch new logs)
- Exec: Establish terminal session to a container via ECS Exec
- Force update: Trigger a new deployment
- Check configuration: Run a set of service checks (IAM roles, ports, ALB, secrets, etc.)
- Service configuration: Describe the service and show core settings
- Task definition history: Show latest revisions with indicators
- Health checks: Show task and ALB health checks with timing guidance
- Security groups: Show ALB/Task SGs and cumulative rules

## Notes on Select Actions

### Exec (ECS Exec)
The tool invokes `aws ecs execute-command` and will offer to enable ECS Exec on the service if it’s disabled. If it fails, check AWS docs:
- Setup and considerations: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs-exec.html
- Troubleshooting: https://docs.aws.amazon.com/AmazonECS/latest/developerguide/troubleshooting.html

### Health Checks
- Task checks run inside the container (command-based).
- ALB checks probe over the network, so SGs, NACLs, routing and target port exposure matter.
- Estimated time to unhealthy is printed for both (typical and worst-case).

### Security Groups
- Lists ALB and Task SGs one per line with names.
- Aggregates inbound/outbound rules across all SGs and shows a compact owners list per rule.

## Troubleshooting

- Authentication
  - If startup fails with authentication errors, confirm credentials and region.
  - `aws sts get-caller-identity` should work with your environment.

- ECS Exec (exec action)
  - Ensure ECS Exec is enabled on the service and you meet IAM requirements.
  - Use the docs linked above for setup and troubleshooting.

- Logs
  - Ensure the task definition has an `awslogs` configuration and group.

- Permissions
  - Your credentials need capabilities to call ECS/EC2/ELB/IAM/CloudWatch Logs APIs used by the tool.

## Version

The binary supports `--version` and prints either an ldflags-injected build version or a fallback development version.

## License

Apache 2.0
