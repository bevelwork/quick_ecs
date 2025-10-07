package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	qc "github.com/bevelwork/quick_color"
)

// updateCapacityAction handles the capacity update action
func updateCapacityAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo) {
	reader := bufio.NewReader(os.Stdin)

	// Update service capacity
	fmt.Printf("%s", qc.Colorize("Enter new capacity (min,desired,max): ", qc.ColorYellow))
	capacityInput, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	capacityInput = strings.TrimSpace(capacityInput)
	if capacityInput == "" {
		fmt.Println("No capacity provided. Exiting")
		return
	}

	minCount, desiredCount, maxCount, err := parseCapacityInput(capacityInput)
	if err != nil {
		log.Fatal(err)
	}

	// Calculate the deployment percentages for display
	var minHealthyPercent, maxPercent int32
	if desiredCount > 0 {
		minHealthyPercent = (minCount * 100) / desiredCount
		maxPercent = (maxCount * 100) / desiredCount
		if maxPercent < 100 {
			maxPercent = 100
		}
	}

	// Check if we need to adjust for AWS ECS constraints
	adjusted := false
	if minHealthyPercent == 100 && maxPercent == 100 {
		adjusted = true
		if desiredCount > 1 {
			minHealthyPercent = 50
			maxPercent = 200
		} else {
			minHealthyPercent = 0
			maxPercent = 200
		}
	}

	fmt.Printf("Updating capacity to: Min=%s, Desired=%s, Max=%s\n",
		qc.ColorizeBold(fmt.Sprintf("%d", minCount), qc.ColorCyan),
		qc.ColorizeBold(fmt.Sprintf("%d", desiredCount), qc.ColorCyan),
		qc.ColorizeBold(fmt.Sprintf("%d", maxCount), qc.ColorCyan))

	if adjusted {
		fmt.Printf("Deployment config: MinHealthy=%d%%, MaxPercent=%d%% (adjusted for rolling deployments)\n",
			minHealthyPercent, maxPercent)
	} else {
		fmt.Printf("Deployment config: MinHealthy=%d%%, MaxPercent=%d%%\n",
			minHealthyPercent, maxPercent)
	}

	// Update service capacity
	fmt.Printf("%s", qc.Colorize("Update service capacity? (y/N): ", qc.ColorYellow))
	confirmInput, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	confirmInput = strings.TrimSpace(confirmInput)

	if confirmInput != "y" && confirmInput != "Y" && confirmInput != "yes" {
		fmt.Println("Capacity update cancelled")
		return
	}

	err = showProgress("Updating service capacity...", func() error {
		return updateServiceCapacity(ctx, config, selectedCluster.Name, selectedService.Name, minCount, desiredCount, maxCount)
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Service %s capacity updated successfully!\n", qc.ColorizeBold(selectedService.Name, qc.ColorGreen))
}

// parseCapacityInput parses the capacity input string in format "min,desired,max".
func parseCapacityInput(input string) (int32, int32, int32, error) {
	parts := strings.Split(input, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid format: expected 'min,desired,max' (e.g., '1,2,3')")
	}

	minStr := strings.TrimSpace(parts[0])
	desiredStr := strings.TrimSpace(parts[1])
	maxStr := strings.TrimSpace(parts[2])

	minCount, err := strconv.Atoi(minStr)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid min count: %v", err)
	}

	desiredCount, err := strconv.Atoi(desiredStr)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid desired count: %v", err)
	}

	maxCount, err := strconv.Atoi(maxStr)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid max count: %v", err)
	}

	// Validate that min <= desired <= max
	if minCount > desiredCount {
		return 0, 0, 0, fmt.Errorf("min count (%d) cannot be greater than desired count (%d)", minCount, desiredCount)
	}
	if desiredCount > maxCount {
		return 0, 0, 0, fmt.Errorf("desired count (%d) cannot be greater than max count (%d)", desiredCount, maxCount)
	}
	if minCount < 0 || desiredCount < 0 || maxCount < 0 {
		return 0, 0, 0, fmt.Errorf("counts must be non-negative")
	}

	return int32(minCount), int32(desiredCount), int32(maxCount), nil
}

// updateServiceCapacity updates the ECS service capacity settings.
func updateServiceCapacity(ctx context.Context, config *Config, clusterName, serviceName string, minCount, desiredCount, maxCount int32) error {
	// Calculate deployment configuration percentages
	// MinimumHealthyPercent: minimum percentage of tasks that must remain healthy during deployment
	// MaximumPercent: maximum percentage of tasks that can be running during deployment
	var minHealthyPercent, maxPercent int32

	if desiredCount > 0 {
		// Calculate minimum healthy percent: (minCount / desiredCount) * 100
		minHealthyPercent = (minCount * 100) / desiredCount
		// Calculate maximum percent: (maxCount / desiredCount) * 100
		maxPercent = (maxCount * 100) / desiredCount
	} else {
		// If desired count is 0, use default values
		minHealthyPercent = 0
		maxPercent = 100
	}

	// Ensure MaximumPercent is at least 100 (AWS requirement)
	if maxPercent < 100 {
		maxPercent = 100
	}

	// AWS ECS constraint: Both cannot be 100% as it blocks deployments
	// If both are 100%, adjust to allow rolling deployments
	if minHealthyPercent == 100 && maxPercent == 100 {
		if desiredCount > 1 {
			// For multiple tasks, use 50% minimum healthy, 200% maximum
			minHealthyPercent = 50
			maxPercent = 200
		} else {
			// For single task, use 0% minimum healthy, 200% maximum
			minHealthyPercent = 0
			maxPercent = 200
		}
	}

	_, err := config.ECSClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      &clusterName,
		Service:      &serviceName,
		DesiredCount: &desiredCount,
		DeploymentConfiguration: &types.DeploymentConfiguration{
			MinimumHealthyPercent: &minHealthyPercent,
			MaximumPercent:        &maxPercent,
		},
	})
	return err
}
