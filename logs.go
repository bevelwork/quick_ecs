package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cloudwatchlogstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// streamLogsAction handles the log streaming action
func streamLogsAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Streaming logs for service: %s\n", colorBold(selectedService.Name, ColorCyan))
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
	// Set up signal handling for Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, os.Kill)

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

	fmt.Printf("%s Interactive log streaming from %s:\n", color("====", ColorBlue), logStreams[0])
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("%s Press Enter to check for new logs, Ctrl+C to exit\n", color("Tip:", ColorYellow))
	fmt.Println(strings.Repeat("-", 60))

	// Initial log fetch
	err := fetchAndDisplayLogs(ctx, config, logGroupName, logStreams[0], &lastTimestamp)
	if err != nil {
		return err
	}
	// Show the action prompt after initial log fetch
	fmt.Printf("%s Press Enter to check for new logs, Ctrl+C to exit: ", color("Action:", ColorCyan))

	for {
		select {
		case <-sigChan:
			fmt.Printf("\n%s Log streaming stopped by user.\n", color("Info:", ColorGreen))
			return nil
		case input := <-inputChan:
			if input == "" {
				// User pressed Enter, fetch new logs
				fmt.Printf("⠋ Checking for new logs...\r")
				err := fetchAndDisplayLogs(ctx, config, logGroupName, logStreams[0], &lastTimestamp)
				if err != nil {
					fmt.Printf("\r\033[K")
					return err
				}
				fmt.Printf("\r\033[K") // Clear the progress line
				// Show the action prompt after each log check
				fmt.Printf("%s Press Enter to check for new logs, Ctrl+C to exit: ", color("Action:", ColorCyan))
			}
		}
	}
}

// fetchAndDisplayLogs fetches and displays new log events
func fetchAndDisplayLogs(ctx context.Context, config *Config, logGroupName, logStreamName string, lastTimestamp *int64) error {
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
		fmt.Printf("%s No new logs since last check.\n", color("Info:", ColorYellow))
	} else {
		fmt.Printf("%s %d new log entries displayed.\n", color("Info:", ColorGreen), newLogsCount)
	}

	return nil
}
