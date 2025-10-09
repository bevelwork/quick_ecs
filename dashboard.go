package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cloudwatchlogstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	qc "github.com/bevelwork/quick_color"
)

// DashboardInfo holds all the information displayed in the dashboard
type DashboardInfo struct {
	ServiceConfig    *types.Service
	TaskDefinition   *types.TaskDefinition
	Deployments      []*DeploymentInfo
	Tasks            []*types.Task
	LogGroupName     string
	LastLogTimestamp int64
	Mutex            sync.RWMutex
}

// dashboardAction handles the comprehensive dashboard action
func dashboardAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Starting dashboard for service: %s\n", qc.ColorizeBold(selectedService.Name, qc.ColorCyan))
	fmt.Printf("Press Ctrl+C to exit dashboard\n\n")

	// Initialize dashboard info
	dashboard := &DashboardInfo{
		TaskDefinition: taskDef,
		Mutex:          sync.RWMutex{},
	}

	// Get initial service configuration
	serviceConfig, err := getServiceConfiguration(ctx, config, selectedCluster.Name, selectedService.Name)
	if err != nil {
		log.Fatal(fmt.Errorf("failed to get service configuration: %v", err))
	}
	dashboard.ServiceConfig = serviceConfig

	// Get log group name
	logGroupName, err := getLogGroupName(taskDef)
	if err != nil {
		log.Printf("Warning: Could not get log group name: %v", err)
	} else {
		dashboard.LogGroupName = logGroupName
	}

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Set up refresh ticker
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Initial display
	displayDashboard(dashboard, config)

	// Start monitoring loop
	for {
		select {
		case <-sigChan:
			fmt.Printf("\n%s Dashboard stopped by user.\n", qc.Colorize("Info:", qc.ColorGreen))
			return
		case <-ticker.C:
			// Refresh dashboard data
			if err := refreshDashboardData(ctx, config, selectedCluster.Name, selectedService.Name, dashboard); err != nil {
				log.Printf("Error refreshing dashboard: %v", err)
			}
			displayDashboard(dashboard, config)
		}
	}
}

// getServiceConfiguration retrieves the full service configuration
func getServiceConfiguration(ctx context.Context, config *Config, clusterName, serviceName string) (*types.Service, error) {
	output, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: []string{serviceName},
	})
	if err != nil {
		return nil, err
	}

	if len(output.Services) == 0 {
		return nil, fmt.Errorf("service %s not found", serviceName)
	}

	return &output.Services[0], nil
}

// refreshDashboardData updates all dashboard information
func refreshDashboardData(ctx context.Context, config *Config, clusterName, serviceName string, dashboard *DashboardInfo) error {
	dashboard.Mutex.Lock()
	defer dashboard.Mutex.Unlock()

	// Refresh service configuration
	serviceConfig, err := getServiceConfiguration(ctx, config, clusterName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to refresh service config: %v", err)
	}
	dashboard.ServiceConfig = serviceConfig

	// Refresh deployments
	deployments, err := getRecentDeployments(ctx, config, clusterName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to refresh deployments: %v", err)
	}
	dashboard.Deployments = deployments

	// Refresh tasks
	tasks, err := getRunningTasksWithDetails(ctx, config, clusterName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to refresh tasks: %v", err)
	}
	dashboard.Tasks = tasks

	return nil
}

// getRunningTasksWithDetails gets running tasks with full details
func getRunningTasksWithDetails(ctx context.Context, config *Config, clusterName, serviceName string) ([]*types.Task, error) {
	// List running tasks
	output, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       &clusterName,
		ServiceName:   &serviceName,
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil {
		return nil, err
	}

	if len(output.TaskArns) == 0 {
		return []*types.Task{}, nil
	}

	// Get task details
	describeOutput, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: &clusterName,
		Tasks:   output.TaskArns,
	})
	if err != nil {
		return nil, err
	}

	// Convert []types.Task to []*types.Task
	tasks := make([]*types.Task, len(describeOutput.Tasks))
	for i := range describeOutput.Tasks {
		tasks[i] = &describeOutput.Tasks[i]
	}
	return tasks, nil
}

// displayDashboard renders the complete dashboard
func displayDashboard(dashboard *DashboardInfo, config *Config) {
	// Clear screen (ANSI escape sequence)
	fmt.Print("\033[2J\033[H")

	dashboard.Mutex.RLock()
	defer dashboard.Mutex.RUnlock()

	// Header
	fmt.Printf("%s\n", qc.Colorize("="+strings.Repeat("=", 78)+"=", qc.ColorBlue))
	fmt.Printf("%s %s\n", qc.Colorize("ECS SERVICE DASHBOARD", qc.ColorBlue), qc.Colorize(time.Now().Format("2006-01-02 15:04:05"), qc.ColorCyan))
	fmt.Printf("%s\n", qc.Colorize("="+strings.Repeat("=", 78)+"=", qc.ColorBlue))

	// Service Configuration Section
	displayServiceConfiguration(dashboard.ServiceConfig, config)

	// Deployments Section
	displayDeploymentsSection(dashboard.Deployments)

	// Tasks Section
	displayTasksSection(dashboard.Tasks)

	// Logs Section
	if dashboard.LogGroupName != "" {
		displayLogsSection(dashboard.LogGroupName, dashboard.Tasks, config)
	}

	// Footer
	fmt.Printf("%s\n", qc.Colorize("="+strings.Repeat("=", 78)+"=", qc.ColorBlue))
	fmt.Printf("%s Auto-refresh every 5 seconds | Press Ctrl+C to exit\n", qc.Colorize("Info:", qc.ColorYellow))
	fmt.Printf("%s\n", qc.Colorize("="+strings.Repeat("=", 78)+"=", qc.ColorBlue))
}

// displayServiceConfiguration shows the service configuration at the top
func displayServiceConfiguration(service *types.Service, config *Config) {
	fmt.Printf("\n%s\n", qc.Colorize("SERVICE CONFIGURATION", qc.ColorGreen))
	fmt.Printf("%s\n", qc.Colorize(strings.Repeat("-", 20), qc.ColorGreen))

	if service.ServiceName != nil {
		fmt.Printf("Name: %s\n", qc.ColorizeBold(*service.ServiceName, qc.ColorCyan))
	}
	if service.Status != nil {
		statusColor := qc.ColorGreen
		if *service.Status != "ACTIVE" {
			statusColor = qc.ColorRed
		}
		fmt.Printf("Status: %s\n", qc.Colorize(*service.Status, statusColor))
	}
	fmt.Printf("Capacity: Running=%d, Desired=%d\n", service.RunningCount, service.DesiredCount)

	if service.TaskDefinition != nil {
		fmt.Printf("Task Definition: %s\n", sanitizeARN(*service.TaskDefinition, config.PrivateMode))
	}

	if len(service.LoadBalancers) > 0 && service.LoadBalancers[0].TargetGroupArn != nil {
		fmt.Printf("Target Group: %s\n", sanitizeARN(*service.LoadBalancers[0].TargetGroupArn, config.PrivateMode))
	}

	if service.DeploymentConfiguration != nil {
		dc := service.DeploymentConfiguration
		if dc.MinimumHealthyPercent != nil && dc.MaximumPercent != nil {
			fmt.Printf("Deployment Config: MinHealthy=%d%%, MaxPercent=%d%%\n", *dc.MinimumHealthyPercent, *dc.MaximumPercent)
		}
	}

	if service.NetworkConfiguration != nil && service.NetworkConfiguration.AwsvpcConfiguration != nil {
		awsvpc := service.NetworkConfiguration.AwsvpcConfiguration
		if len(awsvpc.Subnets) > 0 {
			fmt.Printf("Subnets: %s\n", strings.Join(awsvpc.Subnets, ", "))
		}
		if len(awsvpc.SecurityGroups) > 0 {
			fmt.Printf("Security Groups: %s\n", strings.Join(awsvpc.SecurityGroups, ", "))
		}
	}
}

// displayDeploymentsSection shows the deployments section
func displayDeploymentsSection(deployments []*DeploymentInfo) {
	fmt.Printf("\n%s\n", qc.Colorize("RECENT DEPLOYMENTS", qc.ColorGreen))
	fmt.Printf("%s\n", qc.Colorize(strings.Repeat("-", 18), qc.ColorGreen))

	if len(deployments) == 0 {
		fmt.Printf("%s No deployments found\n", qc.Colorize("Info:", qc.ColorYellow))
		return
	}

	// Show last 5 deployments
	maxDeployments := 5
	if len(deployments) < maxDeployments {
		maxDeployments = len(deployments)
	}

	for i, deployment := range deployments[:maxDeployments] {
		// Color code the status
		statusColor := colorDeploymentStatusDashboard(deployment.Status)

		// Format creation time
		timeStr := deployment.CreatedAt.Format("15:04:05")

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
			"%d. %-23s %s %s [%s] (%d/%d tasks)%s",
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
}

// displayTasksSection shows the running tasks section
func displayTasksSection(tasks []*types.Task) {
	fmt.Printf("\n%s\n", qc.Colorize("RUNNING TASKS", qc.ColorGreen))
	fmt.Printf("%s\n", qc.Colorize(strings.Repeat("-", 13), qc.ColorGreen))

	if len(tasks) == 0 {
		fmt.Printf("%s No running tasks found\n", qc.Colorize("Info:", qc.ColorYellow))
		return
	}

	// Sort tasks by start time (newest first)
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].StartedAt == nil || tasks[j].StartedAt == nil {
			return false
		}
		return tasks[i].StartedAt.After(*tasks[j].StartedAt)
	})

	for i, task := range tasks {
		taskID := extractTaskIDFromDashboard(*task.TaskArn)

		// Get task definition version
		version := ""
		if task.TaskDefinitionArn != nil {
			if idx := strings.LastIndex(*task.TaskDefinitionArn, ":"); idx != -1 {
				version = (*task.TaskDefinitionArn)[idx+1:]
			}
		}

		// Calculate running duration
		duration := "unknown"
		if task.StartedAt != nil {
			duration = humanDurationDashboard(time.Since(*task.StartedAt))
		}

		// Get task status
		status := "UNKNOWN"
		if task.LastStatus != nil {
			status = *task.LastStatus
		}

		// Color code status
		statusColor := qc.ColorGreen
		if status == "STOPPED" || status == "STOPPING" {
			statusColor = qc.ColorRed
		} else if status == "PENDING" {
			statusColor = qc.ColorYellow
		}

		entry := fmt.Sprintf(
			"%d. Task %-20s def:%s status:[%s] running:%s",
			i+1, taskID, version,
			qc.Colorize(status, statusColor),
			duration,
		)

		// Alternate row colors
		var rowColor string
		if i%2 == 0 {
			rowColor = qc.ColorWhite
		} else {
			rowColor = qc.ColorCyan
		}

		fmt.Println(qc.Colorize(entry, rowColor))
	}
}

// displayLogsSection shows recent log entries
func displayLogsSection(logGroupName string, tasks []*types.Task, config *Config) {
	fmt.Printf("\n%s\n", qc.Colorize("RECENT LOGS", qc.ColorGreen))
	fmt.Printf("%s\n", qc.Colorize(strings.Repeat("-", 11), qc.ColorGreen))

	if len(tasks) == 0 {
		fmt.Printf("%s No tasks available for log streaming\n", qc.Colorize("Info:", qc.ColorYellow))
		return
	}

	// Get the most recent task's logs
	mostRecentTask := tasks[0] // tasks are sorted by start time
	taskID := extractTaskIDFromDashboard(*mostRecentTask.TaskArn)

	// Get log stream for this task
	logStream, err := getLogStreamForTask(context.Background(), config, logGroupName, taskID)
	if err != nil {
		fmt.Printf("%s Could not get logs for task %s: %v\n", qc.Colorize("Warning:", qc.ColorYellow), taskID, err)
		return
	}

	// Get recent log events
	logEvents, err := getRecentLogEvents(context.Background(), config, logGroupName, logStream)
	if err != nil {
		fmt.Printf("%s Could not fetch log events: %v\n", qc.Colorize("Warning:", qc.ColorYellow), err)
		return
	}

	if len(logEvents) == 0 {
		fmt.Printf("%s No recent log entries found\n", qc.Colorize("Info:", qc.ColorYellow))
		return
	}

	// Display last 10 log entries
	maxLogs := 10
	if len(logEvents) < maxLogs {
		maxLogs = len(logEvents)
	}

	for i, event := range logEvents[len(logEvents)-maxLogs:] {
		if event.Timestamp != nil && event.Message != nil {
			timestamp := time.Unix(*event.Timestamp/1000, 0)
			timeStr := timestamp.Format("15:04:05")

			// Truncate long messages
			message := *event.Message
			if len(message) > 60 {
				message = message[:57] + "..."
			}

			entry := fmt.Sprintf("[%s] %s", timeStr, message)

			// Alternate row colors
			var rowColor string
			if i%2 == 0 {
				rowColor = qc.ColorWhite
			} else {
				rowColor = qc.ColorCyan
			}

			fmt.Println(qc.Colorize(entry, rowColor))
		}
	}
}

// getLogStreamForTask finds the log stream for a specific task
func getLogStreamForTask(ctx context.Context, config *Config, logGroupName, taskID string) (string, error) {
	output, err := config.CloudWatchLogsClient.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: &logGroupName,
		OrderBy:      cloudwatchlogstypes.OrderByLastEventTime,
		Descending:   boolPtr(true),
	})
	if err != nil {
		return "", err
	}

	for _, stream := range output.LogStreams {
		if stream.LogStreamName != nil && strings.Contains(*stream.LogStreamName, taskID) {
			return *stream.LogStreamName, nil
		}
	}

	return "", fmt.Errorf("no log stream found for task %s", taskID)
}

// getRecentLogEvents gets the most recent log events from a log stream
func getRecentLogEvents(ctx context.Context, config *Config, logGroupName, logStreamName string) ([]cloudwatchlogstypes.OutputLogEvent, error) {
	// Get events from the last hour
	startTime := time.Now().Add(-1*time.Hour).Unix() * 1000

	output, err := config.CloudWatchLogsClient.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  &logGroupName,
		LogStreamName: &logStreamName,
		StartTime:     &startTime,
	})
	if err != nil {
		return nil, err
	}

	return output.Events, nil
}

// extractTaskIDFromDashboard extracts the task ID from a task ARN
func extractTaskIDFromDashboard(taskArn string) string {
	parts := strings.Split(taskArn, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return taskArn
}

// humanDurationDashboard formats a duration in a human-readable way
func humanDurationDashboard(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	seconds := int(d.Seconds())
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%02ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// colorDeploymentStatusDashboard returns the appropriate color for deployment status
func colorDeploymentStatusDashboard(status string) string {
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
