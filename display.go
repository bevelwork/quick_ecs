package main

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	qc "github.com/bevelwork/quick_color"
)

// Colors and helpers now provided by quick_color via adapter in quick_color_adapter.go

// printHeader prints the application header
func printHeader(privateMode bool, callerIdentity *sts.GetCallerIdentityOutput) {
	header := []string{
		qc.Colorize(strings.Repeat("-", 40), qc.ColorBlue),
		"-- ECS Quick Manager --",
		qc.Colorize(strings.Repeat("-", 40), qc.ColorBlue),
	}
	if !privateMode {
		header = append(header, fmt.Sprintf(
			"  Account: %s \n  User: %s",
			*callerIdentity.Account, *callerIdentity.Arn,
		))
		header = append(header, qc.Colorize(strings.Repeat("-", 40), qc.ColorBlue))
	}

	fmt.Println(strings.Join(header, "\n"))
}

// colorClusterStatus returns the appropriate color for cluster status
func colorClusterStatus(status string) string {
	switch status {
	case "ACTIVE":
		return qc.ColorGreen
	case "INACTIVE":
		return qc.ColorRed
	case "PROVISIONING":
		return qc.ColorYellow
	case "DEPROVISIONING":
		return qc.ColorYellow
	default:
		return qc.ColorWhite
	}
}

// colorServiceStatus returns the appropriate color for service status
func colorServiceStatus(status string) string {
	switch status {
	case "ACTIVE":
		return qc.ColorGreen
	case "DRAINING":
		return qc.ColorYellow
	case "INACTIVE":
		return qc.ColorRed
	default:
		return qc.ColorWhite
	}
}

// Progress indicator functions now provided by quick_color via adapter

// sanitizeARN masks account numbers in ARNs when private mode is active
func sanitizeARN(arn string, privateMode bool) string {
	if !privateMode {
		return arn
	}

	// ARN format: arn:partition:service:region:account-id:resource-type/resource
	// We want to mask the account-id part (4th colon-separated segment)
	parts := strings.Split(arn, ":")
	if len(parts) >= 5 {
		// Mask the account ID (4th segment, index 4)
		parts[4] = "***"
		return strings.Join(parts, ":")
	}

	// If it doesn't match expected ARN format, return as-is
	return arn
}
