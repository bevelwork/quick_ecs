package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ContainerInfo represents a container with task information
type ContainerInfo struct {
	Name      string
	TaskID    string
	TaskARN   string
	LastThree string
}

// connectAction handles the ECS exec connection action
func connectAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Connecting to container for service: %s\n", colorBold(selectedService.Name, ColorCyan))

	err := connectToContainer(ctx, config, selectedCluster.Name, selectedService.Name, taskDef)
	if err != nil {
		log.Fatal(err)
	}
}

// connectToContainer establishes an ECS Exec session to a container
func connectToContainer(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition) error {
	// Get running tasks for the service
	tasks, err := getRunningTasksForConnect(ctx, config, clusterName, serviceName)
	if err != nil {
		return fmt.Errorf("failed to get running tasks: %v", err)
	}

	if len(tasks) == 0 {
		return fmt.Errorf("no running tasks found for service %s", serviceName)
	}

	// Get detailed task information
	taskDetails, err := getTaskDetails(ctx, config, clusterName, tasks)
	if err != nil {
		return fmt.Errorf("failed to get task details: %v", err)
	}

	// Build container list with task ID suffixes
	containers := buildContainerList(taskDetails, taskDef)
	if len(containers) == 0 {
		return fmt.Errorf("no containers found in running tasks")
	}

	// Let user select a container
	selectedContainer := selectContainer(containers)

	// Execute ECS Exec
	return executeECSExec(ctx, config, clusterName, serviceName, selectedContainer.TaskARN, selectedContainer.Name)
}

// getRunningTasksForConnect gets the running tasks for the service
func getRunningTasksForConnect(ctx context.Context, config *Config, clusterName, serviceName string) ([]string, error) {
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

// getTaskDetails gets detailed information about the running tasks
func getTaskDetails(ctx context.Context, config *Config, clusterName string, taskArns []string) ([]types.Task, error) {
	output, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: &clusterName,
		Tasks:   taskArns,
	})
	if err != nil {
		return nil, err
	}

	return output.Tasks, nil
}

// buildContainerList creates a list of containers with task ID suffixes
func buildContainerList(tasks []types.Task, taskDef *types.TaskDefinition) []*ContainerInfo {
	var containers []*ContainerInfo

	// Get container names from task definition
	containerNames := make([]string, len(taskDef.ContainerDefinitions))
	for i, containerDef := range taskDef.ContainerDefinitions {
		containerNames[i] = *containerDef.Name
	}

	// Build container list for each task
	for _, task := range tasks {
		taskID := extractTaskID(*task.TaskArn)
		lastThree := taskID
		if len(taskID) >= 3 {
			lastThree = taskID[len(taskID)-3:]
		}

		for _, containerName := range containerNames {
			containers = append(containers, &ContainerInfo{
				Name:      containerName,
				TaskID:    taskID,
				TaskARN:   *task.TaskArn,
				LastThree: lastThree,
			})
		}
	}

	// Sort containers by name, then by task ID for consistency
	sort.Slice(containers, func(i, j int) bool {
		if containers[i].Name != containers[j].Name {
			return containers[i].Name < containers[j].Name
		}
		return containers[i].TaskID < containers[j].TaskID
	})

	return containers
}

// extractTaskID extracts the task ID from a task ARN
func extractTaskID(taskARN string) string {
	parts := strings.Split(taskARN, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return taskARN
}

// selectContainer allows the user to select a container
func selectContainer(containers []*ContainerInfo) *ContainerInfo {
	fmt.Printf("\n%s\n", color("Available Containers:", ColorBlue))

	for i, container := range containers {
		displayName := fmt.Sprintf("%s-%s", container.Name, container.LastThree)
		fmt.Printf("  %d. %s (Task: %s)\n", i+1, colorBold(displayName, ColorCyan), container.TaskID)
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", color("Select container. Blank, or non-numeric input will exit: ", ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = strings.TrimSpace(input)
	if input == "" {
		fmt.Println("Exiting")
		os.Exit(0)
	}
	inputInt, err := strconv.Atoi(input)
	if err != nil {
		fmt.Println("Non-numeric input. Exiting")
		os.Exit(0)
	}
	if inputInt < 1 || inputInt > len(containers) {
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	return containers[inputInt-1]
}

// executeECSExec runs the ECS Exec command
func executeECSExec(ctx context.Context, config *Config, clusterName, serviceName, taskARN, containerName string) error {
	// Try ECS Exec first
	err := tryECSExec(ctx, config, clusterName, taskARN, containerName)
	if err != nil {
		// Check if it's the "ECS Exec is not enabled" error
		if strings.Contains(err.Error(), "ECS Exec is not enabled for cluster") {
			fmt.Printf("%s ECS Exec is not enabled for this cluster.\n", color("Warning:", ColorYellow))
			fmt.Printf("%s", color("Would you like to enable ECS Exec for this service? (y/N): ", ColorYellow))

			reader := bufio.NewReader(os.Stdin)
			confirmInput, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			confirmInput = strings.TrimSpace(confirmInput)

			if confirmInput == "y" || confirmInput == "Y" || confirmInput == "yes" {
				// Enable ECS Exec for the service
				err = enableECSExecForService(ctx, config, clusterName, serviceName)
				if err != nil {
					return fmt.Errorf("failed to enable ECS Exec: %v", err)
				}

				fmt.Printf("%s ECS Exec enabled successfully!\n", color("Success:", ColorGreen))
				fmt.Printf("%s", color("Retrying connection...\n", ColorCyan))

				// Retry the connection
				err = tryECSExec(ctx, config, clusterName, taskARN, containerName)
				if err != nil {
					return fmt.Errorf("ECS Exec connection failed after enabling: %v", err)
				}
			} else {
				return fmt.Errorf("ECS Exec is not enabled and user declined to enable it")
			}
		} else {
			return err
		}
	}

	return nil
}

// tryECSExec attempts to run ECS Exec
func tryECSExec(ctx context.Context, config *Config, clusterName, taskARN, containerName string) error {
	// Build the ECS Exec command
	cmd := exec.Command("aws", "ecs", "execute-command",
		"--region", config.Region,
		"--cluster", clusterName,
		"--task", taskARN,
		"--container", containerName,
		"--interactive",
		"--command", "sh")

	// Set up the command to use the same AWS configuration
	cmd.Env = os.Environ()

	// Run the command
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// enableECSExecForService enables ECS Exec for the service
func enableECSExecForService(ctx context.Context, config *Config, clusterName, serviceName string) error {
	// Get the current service configuration
	services, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: []string{serviceName},
	})
	if err != nil {
		return fmt.Errorf("failed to describe service: %v", err)
	}

	if len(services.Services) == 0 {
		return fmt.Errorf("service %s not found", serviceName)
	}

	// Update the service to enable ECS Exec
	_, err = config.ECSClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:              &clusterName,
		Service:              &serviceName,
		EnableExecuteCommand: boolPtr(true),
	})
	if err != nil {
		return fmt.Errorf("failed to update service: %v", err)
	}

	return nil
}
