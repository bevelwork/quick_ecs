package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cloudwatchlogstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	qc "github.com/bevelwork/quick_color"
)

// streamLogsAction handles the log streaming action
func streamLogsAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Streaming logs for service: %s\n", qc.ColorizeBold(selectedService.Name, qc.ColorCyan))
	fmt.Printf("Press Ctrl+C to stop streaming\n\n")

	err := streamServiceLogs(ctx, config, selectedCluster.Name, selectedService.Name, taskDef)
	if err != nil {
		log.Fatal(err)
	}
}

// streamServiceLogs streams logs from the service's CloudWatch log group
func streamServiceLogs(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition) error {
	// Get log group name from task definition
	logGroupName, err := getLogGroupName(taskDef)
	if err != nil {
		return fmt.Errorf("failed to get log group name: %v", err)
	}

	// Get running tasks for the service
	tasks, err := getRunningTasks(ctx, config, clusterName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to get running tasks: %v", err)
	}

	if len(tasks) == 0 {
		return fmt.Errorf("no running tasks found for service %s", serviceName)
	}

	// Get log streams for the tasks
	logStreams, err := getLogStreams(ctx, config, logGroupName, tasks)
	if err != nil {
		return fmt.Errorf("failed to get log streams: %v", err)
	}

	if len(logStreams) == 0 {
		return fmt.Errorf("no log streams found for service %s", serviceName)
	}

	// Start streaming logs
	return streamLogs(ctx, config, logGroupName, logStreams)
}

// getLogGroupName extracts the log group name from the task definition
func getLogGroupName(taskDef *types.TaskDefinition) (string, error) {
	if len(taskDef.ContainerDefinitions) == 0 {
		return "", fmt.Errorf("no container definitions found in task definition")
	}

	containerDef := taskDef.ContainerDefinitions[0]
	if containerDef.LogConfiguration == nil {
		return "", fmt.Errorf("no log configuration found in container definition")
	}

	logGroupName, exists := containerDef.LogConfiguration.Options["awslogs-group"]
	if !exists {
		return "", fmt.Errorf("awslogs-group not found in log configuration")
	}

	return logGroupName, nil
}

// getRunningTasks gets the running tasks for the service
func getRunningTasks(ctx context.Context, config *Config, clusterName, serviceName string) ([]string, error) {
	output, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       &clusterName,
		ServiceName:   &serviceName,
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil {
		return nil, err
	}

	return output.TaskArns, nil
}

// getLogStreams gets the log streams for the given tasks
func getLogStreams(ctx context.Context, config *Config, logGroupName string, taskArns []string) ([]string, error) {
	// Extract task IDs from ARNs
	taskIDs := make([]string, len(taskArns))
	for i, arn := range taskArns {
		parts := strings.Split(arn, "/")
		if len(parts) > 0 {
			taskIDs[i] = parts[len(parts)-1]
		}
	}

	// Get log streams
	output, err := config.CloudWatchLogsClient.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: &logGroupName,
		OrderBy:      cloudwatchlogstypes.OrderByLastEventTime,
		Descending:   boolPtr(true),
	})
	if err != nil {
		return nil, err
	}

	// Filter log streams that match our tasks
	var matchingStreams []string
	for _, stream := range output.LogStreams {
		streamName := *stream.LogStreamName
		for _, taskID := range taskIDs {
			if strings.Contains(streamName, taskID) {
				matchingStreams = append(matchingStreams, streamName)
				break
			}
		}
	}

	return matchingStreams, nil
}

// streamLogs streams logs from the specified log streams
func streamLogs(ctx context.Context, config *Config, logGroupName string, logStreams []string) error {
	// Set up signal handling for Ctrl+C and termination
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Channel to handle user input
	inputChan := make(chan string, 1)

	// Start input reader in a goroutine
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			input, _ := reader.ReadString('\n')
			inputChan <- strings.TrimSpace(input)
		}
	}()

	var lastTimestamp int64 = 0

	fmt.Printf("%s Interactive log streaming from %s:\n", qc.Colorize("====", qc.ColorBlue), logStreams[0])
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("%s Press Enter to check for new logs, auto-refresh every 3s, Ctrl+C to exit\n", qc.Colorize("Tip:", qc.ColorYellow))
	fmt.Println(strings.Repeat("-", 60))

	// Initial log fetch
	if err := fetchAndDisplayLogs(ctx, config, logGroupName, logStreams[0], &lastTimestamp, false); err != nil {
		return err
	}
	// Show the action prompt after initial log fetch
	fmt.Printf("%s Press Enter to check for new logs, auto-refresh every 3s, Ctrl+C to exit: ", qc.Colorize("Action:", qc.ColorCyan))

	// Auto-refresh ticker
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Printf("\n%s Log streaming stopped by user.\n", qc.Colorize("Info:", qc.ColorGreen))
			return nil
		case <-ticker.C:
			// Auto refresh (quiet on no-new-logs)
			fmt.Print(".")
			if err := fetchAndDisplayLogs(ctx, config, logGroupName, logStreams[0], &lastTimestamp, true); err != nil {
				return err
			}
		case input := <-inputChan:
			if input == "" {
				// User pressed Enter, fetch new logs
				if err := fetchAndDisplayLogs(ctx, config, logGroupName, logStreams[0], &lastTimestamp, false); err != nil {
					return err
				}
			}
		}
	}
}

// streamTaskLogsAction allows user to select a specific task and streams only its logs
func streamTaskLogsAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Streaming logs for a specific task in service: %s\n", qc.ColorizeBold(selectedService.Name, qc.ColorCyan))

	if err := streamTaskLogsInteractive(ctx, config, selectedCluster.Name, selectedService.Name, taskDef); err != nil {
		log.Fatal(err)
	}
}

// streamTaskLogsRepeat supports repeat-last behavior by prompting again for a task
func streamTaskLogsRepeat(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition) error {
	return streamTaskLogsInteractive(ctx, config, clusterName, serviceName, taskDef)
}

// streamTaskLogsInteractive lists running tasks with metadata and streams the selected task's logs
func streamTaskLogsInteractive(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition) error {
	// Resolve log group first
	logGroupName, err := getLogGroupName(taskDef)
	if err != nil {
		return fmt.Errorf("failed to get log group name: %v", err)
	}

	// List running tasks
	taskArns, err := getRunningTasks(ctx, config, clusterName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to get running tasks: %v", err)
	}
	if len(taskArns) == 0 {
		return fmt.Errorf("no running tasks found for service %s", serviceName)
	}

	// Describe tasks for metadata
	desc, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: &clusterName, Tasks: taskArns})
	if err != nil {
		return fmt.Errorf("failed to describe tasks: %v", err)
	}
	if len(desc.Tasks) == 0 {
		return fmt.Errorf("no task details available")
	}

	// Build display list with version and running duration
	fmt.Printf("\n%s\n", qc.Colorize("Running Tasks:", qc.ColorBlue))
	for i, t := range desc.Tasks {
		taskID := extractTaskID(*t.TaskArn)
		version := ""
		if t.TaskDefinitionArn != nil {
			if idx := strings.LastIndex(*t.TaskDefinitionArn, ":"); idx != -1 {
				version = (*t.TaskDefinitionArn)[idx+1:]
			}
		}
		dur := "unknown"
		if t.StartedAt != nil {
			dur = humanDuration(time.Since(*t.StartedAt))
		}
		fmt.Printf("%3d. Task %s  def:%s  running:%s\n", i+1, qc.ColorizeBold(taskID, qc.ColorCyan), version, dur)
	}

	// Prompt for selection
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", qc.Colorize("Select task. Press Enter for first option, or non-numeric input will exit: ", qc.ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	input = strings.TrimSpace(input)
	idx := 1
	if input != "" {
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(desc.Tasks) {
			fmt.Println("Invalid selection. Exiting")
			os.Exit(0)
		}
		idx = n
	}

	chosen := desc.Tasks[idx-1]
	// Resolve a log stream for this task
	streamName, err := resolveSingleTaskLogStream(ctx, config, logGroupName, *chosen.TaskArn)
	if err != nil {
		return err
	}

	fmt.Printf("\nStreaming logs for task %s\n", qc.ColorizeBold(extractTaskID(*chosen.TaskArn), qc.ColorGreen))
	fmt.Printf("Press Ctrl+C to stop streaming\n\n")

	return streamLogs(ctx, config, logGroupName, []string{streamName})
}

// resolveSingleTaskLogStream finds the most recent log stream for a specific task ID within a log group
func resolveSingleTaskLogStream(ctx context.Context, config *Config, logGroupName, taskArn string) (string, error) {
	taskID := extractTaskID(taskArn)

	out, err := config.CloudWatchLogsClient.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: &logGroupName,
		OrderBy:      cloudwatchlogstypes.OrderByLastEventTime,
		Descending:   boolPtr(true),
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe log streams: %v", err)
	}

	for _, ls := range out.LogStreams {
		if ls.LogStreamName == nil {
			continue
		}
		if strings.Contains(*ls.LogStreamName, taskID) {
			return *ls.LogStreamName, nil
		}
	}
	return "", fmt.Errorf("no log stream found for task %s", taskID)
}

// humanDuration makes a short human-friendly duration like 1h23m, 5m10s
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	// Limit granularity to seconds
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

// fetchAndDisplayLogs fetches and displays new log events
func fetchAndDisplayLogs(ctx context.Context, config *Config, logGroupName, logStreamName string, lastTimestamp *int64, quiet bool) error {
	// Get log events starting from the last timestamp we saw
	input := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  &logGroupName,
		LogStreamName: &logStreamName,
		StartTime:     lastTimestamp,
	}

	output, err := config.CloudWatchLogsClient.GetLogEvents(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to get log events: %v", err)
	}

	newLogsCount := 0
	for _, event := range output.Events {
		if event.Timestamp != nil && *event.Timestamp > *lastTimestamp {
			// Format timestamp
			timestamp := time.Unix(*event.Timestamp/1000, 0)
			fmt.Printf("[%s] %s\n", timestamp.Format("2006-01-02 15:04:05"), *event.Message)
			*lastTimestamp = *event.Timestamp
			newLogsCount++
		}
	}

	if newLogsCount == 0 {
		if !quiet {
			fmt.Printf("%s No new logs since last check.\n", qc.Colorize("Info:", qc.ColorYellow))
		}
	} else {
		fmt.Printf("%s %d new log entries displayed.\n", qc.Colorize("Info:", qc.ColorGreen), newLogsCount)
	}

	return nil
}
