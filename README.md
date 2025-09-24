# Quick ECS

A simple Go CLI tool for quickly managing AWS ECS services. This tool lists all your ECS clusters and services, allows you to select one, and provides functionality to update container images and force service updates.

## ✨ Features

- **Interactive Cluster Selection**: Lists all ECS clusters with numbered menu
- **Interactive Service Selection**: Lists all services in selected cluster
- **Container Image Display**: Shows current container image from task definition
- **Image Version Update**: Allows updating container image version/tag
- **Service Update**: Creates new task definition and updates service with force deployment
- **Visual Feedback**: Color-coded output with alternating row colors for easy scanning
- **Status Indicators**: Shows cluster and service status with color-coded indicators

## Usage

```bash
quick_ecs # Use default profile
AWS_PROFILE=production quick_ecs # Use specific profile
aws-vault exec production -- quick_ecs # Using aws-vault
granted production quick_ecs # Using granted
```

## Prerequisites

### Required Software

1. **Go 1.24.4 or later** - [Download and install Go](https://golang.org/dl/)
2. **AWS CLI** - [Install AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html#getting-started-install-instructions)

### Install with Go
```bash
go install github.com/bevelwork/quick_ecs@latest
quick_ecs --version
```

### Install with Homebrew (macOS/Linux)
```bash
brew tap bevelwork/homebrew-tap # See https://github.com/bevelwork/homebrew-tap
brew install quick-ecs
quick_ecs --version
```

### Or Build from Source
```bash
git clone https://github.com/bevelwork/quick_ecs.git
cd quick_ecs
go build -o quick_ecs .
./quick_ecs --version
```

## How It Works

1. **Authentication**: Uses AWS SDK v2 to authenticate with your AWS account
2. **Cluster Discovery**: Queries ECS to get all clusters using pagination
3. **Service Discovery**: Queries ECS to get all services in the selected cluster
4. **Task Definition Analysis**: Retrieves current task definition and extracts container image
5. **Image Update**: Creates new task definition with updated image version
6. **Service Update**: Updates the ECS service with new task definition and forces deployment

## Workflow

1. **Select Cluster**: Choose from available ECS clusters
2. **Select Service**: Choose from services in the selected cluster
3. **View Current Image**: See the current container image being used
4. **Enter New Version**: Provide the new image version/tag
5. **Confirm Update**: Confirm the service update operation

## Required IAM Permissions

Your AWS credentials need the following permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ecs:ListClusters",
        "ecs:DescribeClusters",
        "ecs:ListServices",
        "ecs:DescribeServices",
        "ecs:DescribeTaskDefinition",
        "ecs:RegisterTaskDefinition",
        "ecs:UpdateService",
        "sts:GetCallerIdentity"
      ],
      "Resource": "*"
    }
  ]
}
```

## Version Management

This project uses a simple date-based versioning system: `major.minor.YYYYMMDD`

## Development

```bash
# Build the binary
make build

# Run tests
make test

# Clean build artifacts
make clean

# Show current version
make version

# Update version
make major 2
make minor 1

# Build release binary
make build-release
```
