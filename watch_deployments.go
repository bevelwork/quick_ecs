package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	qc "github.com/bevelwork/quick_color"
)

// DeploymentInfo represents deployment information for display
type DeploymentInfo struct {
	ID             string
	Status         string
	TaskDefinition string
	CreatedAt      time.Time
	RunningCount   int32
	DesiredCount   int32
	IsActive       bool
}

// watchDeploymentsAction handles the watch deployments action
func watchDeploymentsAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Watching deployments for service: %s\n", qc.ColorizeBold(selectedService.Name, qc.ColorCyan))

	// Get recent deployments
	deployments, err := getRecentDeployments(ctx, config, selectedCluster.Name, selectedService.Name)
	if err != nil {
		log.Fatal(err)
	}

	if len(deployments) == 0 {
		fmt.Printf("No deployments found for service %s\n", selectedService.Name)
		return
	}

	// Display deployments and allow selection
	selectedDeployment := displayDeployments(deployments, config)
	if selectedDeployment == nil {
		fmt.Println("No deployment selected")
		return
	}

	// Stream logs for the selected deployment
	fmt.Printf("Streaming logs for deployment: %s\n", qc.ColorizeBold(selectedDeployment.ID, qc.ColorGreen))
	fmt.Printf("Press Ctrl+C to stop streaming\n\n")

	err = streamDeploymentLogs(ctx, config, selectedCluster.Name, selectedService.Name, selectedDeployment, taskDef)
	if err != nil {
		log.Fatal(err)
	}
}

// getRecentDeployments fetches the last 10 deployments for a service
func getRecentDeployments(ctx context.Context, config *Config, clusterName, serviceName string) ([]*DeploymentInfo, error) {
	// Get service details to access deployments
	services, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: []string{serviceName},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe service: %v", err)
	}

	if len(services.Services) == 0 {
		return nil, fmt.Errorf("service %s not found", serviceName)
	}

	service := services.Services[0]
	var deployments []*DeploymentInfo

	// Process deployments from the service
	for _, deployment := range service.Deployments {
		if deployment.Id == nil {
			continue
		}

		deploymentInfo := &DeploymentInfo{
			ID:           *deployment.Id,
			RunningCount: deployment.RunningCount,
			DesiredCount: deployment.DesiredCount,
		}

		// Set status
		if deployment.Status != nil {
			deploymentInfo.Status = *deployment.Status
		} else {
			deploymentInfo.Status = "UNKNOWN"
		}

		// Set task definition
		if deployment.TaskDefinition != nil {
			deploymentInfo.TaskDefinition = *deployment.TaskDefinition
		}

		// Set created at time
		if deployment.CreatedAt != nil {
			deploymentInfo.CreatedAt = *deployment.CreatedAt
		}

		// Determine if this is the active deployment
		deploymentInfo.IsActive = deploymentInfo.Status == "ACTIVE"

		deployments = append(deployments, deploymentInfo)
	}

	// Sort deployments by creation time (newest first)
	sort.Slice(deployments, func(i, j int) bool {
		return deployments[i].CreatedAt.After(deployments[j].CreatedAt)
	})

	// Limit to last 10 deployments
	if len(deployments) > 10 {
		deployments = deployments[:10]
	}

	return deployments, nil
}

// displayDeployments shows the list of deployments and allows user selection
func displayDeployments(deployments []*DeploymentInfo, config *Config) *DeploymentInfo {
	fmt.Printf("\n%s\n", qc.Colorize("Recent Deployments (last 10):", qc.ColorBlue))

	for i, deployment := range deployments {
		// Color code the status
		statusColor := colorDeploymentStatus(deployment.Status)

		// Format creation time
		timeStr := deployment.CreatedAt.Format("2006-01-02 15:04:05")

		// Create status indicators
		indicators := []string{}
		if deployment.IsActive {
			indicators = append(indicators, qc.Colorize("ACTIVE", qc.ColorGreen))
		}
		if deployment.Status == "FAILED" {
			indicators = append(indicators, qc.Colorize("FAILED", qc.ColorRed))
		}

		indicatorStr := ""
		if len(indicators) > 0 {
			indicatorStr = " [" + strings.Join(indicators, ", ") + "]"
		}

		// Truncate deployment ID for display
		displayID := deployment.ID
		if len(displayID) > 20 {
			displayID = displayID[:20] + "..."
		}

		// Truncate task definition for display
		displayTaskDef := deployment.TaskDefinition
		if len(displayTaskDef) > 30 {
			displayTaskDef = displayTaskDef[len(displayTaskDef)-30:]
		}

		entry := fmt.Sprintf(
			"%3d. %-23s %s %s [%s] (%d/%d tasks)%s",
			i+1, displayID, timeStr, displayTaskDef,
			qc.Colorize(deployment.Status, statusColor),
			deployment.RunningCount, deployment.DesiredCount,
			indicatorStr,
		)

		// Alternate row colors for better readability
		var rowColor string
		if i%2 == 0 {
			rowColor = qc.ColorWhite
		} else {
			rowColor = qc.ColorCyan
		}

		fmt.Println(qc.Colorize(entry, rowColor))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", qc.Colorize("Select deployment to watch logs (number). Press Enter for first option, or invalid input will exit: ", qc.ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = strings.TrimSpace(input)

	if input == "" {
		// Default to first deployment
		return deployments[0]
	}

	inputInt, err := strconv.Atoi(input)
	if err != nil {
		fmt.Println("Non-numeric input. Exiting")
		os.Exit(0)
	}

	if inputInt < 1 || inputInt > len(deployments) {
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	return deployments[inputInt-1]
}

// streamDeploymentLogs streams logs for a specific deployment
func streamDeploymentLogs(ctx context.Context, config *Config, clusterName, serviceName string, deployment *DeploymentInfo, taskDef *types.TaskDefinition) error {
	// Get log group name from task definition
	logGroupName, err := getLogGroupName(taskDef)
	if err != nil {
		return fmt.Errorf("failed to get log group name: %v", err)
	}

	// Get tasks for this specific deployment
	tasks, err := getTasksForDeployment(ctx, config, clusterName, serviceName, deployment.ID)
	if err != nil {
		return fmt.Errorf("failed to get tasks for deployment: %v", err)
	}

	if len(tasks) == 0 {
		return fmt.Errorf("no tasks found for deployment %s", deployment.ID)
	}

	// Get log streams for the tasks
	logStreams, err := getLogStreams(ctx, config, logGroupName, tasks)
	if err != nil {
		return fmt.Errorf("failed to get log streams: %v", err)
	}

	if len(logStreams) == 0 {
		return fmt.Errorf("no log streams found for deployment %s", deployment.ID)
	}

	// Start streaming logs
	return streamLogs(ctx, config, logGroupName, logStreams)
}

// getTasksForDeployment gets tasks for a specific deployment
func getTasksForDeployment(ctx context.Context, config *Config, clusterName, serviceName, deploymentID string) ([]string, error) {
	// List all tasks for the service
	output, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       &clusterName,
		ServiceName:   &serviceName,
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil {
		return nil, err
	}

	if len(output.TaskArns) == 0 {
		return []string{}, nil
	}

	// Get task details to filter by deployment
	describeOutput, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: &clusterName,
		Tasks:   output.TaskArns,
	})
	if err != nil {
		return nil, err
	}

	var deploymentTasks []string
	for _, task := range describeOutput.Tasks {
		// Check if this task belongs to the selected deployment
		if task.StartedBy != nil && strings.Contains(*task.StartedBy, deploymentID) {
			deploymentTasks = append(deploymentTasks, *task.TaskArn)
		}
	}

	return deploymentTasks, nil
}

// colorDeploymentStatus returns the appropriate color for deployment status
func colorDeploymentStatus(status string) string {
	switch status {
	case "ACTIVE":
		return qc.ColorGreen
	case "FAILED":
		return qc.ColorRed
	case "INACTIVE":
		return qc.ColorYellow
	case "PRIMARY":
		return qc.ColorCyan
	default:
		return qc.ColorWhite
	}
}
