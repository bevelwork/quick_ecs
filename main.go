// Package main provides a command-line tool for quickly managing AWS ECS services.
// The tool lists all ECS clusters and services, allows selection, and provides
// functionality to update container images and force service updates.

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cloudwatchlogstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	versionpkg "github.com/bevelwork/quick_ecs/version"
)

// ANSI color codes for terminal output
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

// ClusterInfo represents an ECS cluster with its metadata for display purposes.
type ClusterInfo struct {
	Name           string // The cluster name
	ARN            string // The cluster ARN
	Status         string // The cluster status
	RunningTasks   int32  // Number of running tasks
	ActiveServices int32  // Number of active services
}

// ServiceInfo represents an ECS service with its metadata for display purposes.
type ServiceInfo struct {
	Name           string // The service name
	ARN            string // The service ARN
	Status         string // The service status
	DesiredCount   int32  // Desired task count
	RunningCount   int32  // Running task count
	TaskDefinition string // Current task definition ARN
}

// Config holds all AWS clients and configuration for the application
type Config struct {
	ECSClient            *ecs.Client
	EC2Client            *ec2.Client
	IAMClient            *iam.Client
	ELBv2Client          *elasticloadbalancingv2.Client
	CloudWatchLogsClient *cloudwatchlogs.Client
	Region               string
	PrivateMode          bool
}

// Deprecated: kept for backward compatibility if older ldflags are used.
// Prefer setting github.com/bevelwork/quick_ecs/version.Full instead.
var version = ""

func main() {
	// Parse flags
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}
	versionFlag := flag.Bool("version", false, "Print version and exit")
	region := flag.String("region", "", "AWS region to use (defaults to current region)")
	privateMode := flag.Bool("private-mode", false, "Hide account information during execution")
	flag.Parse()

	if *versionFlag {
		fmt.Println(resolveVersion())
		return
	}

	// Confirm this looks like a region
	if *region != "" && strings.Count(*region, "-") != 2 {
		log.Fatal("Region must be specified as a region name, e.g. us-east-1")
	}

	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(*region))
	if err != nil {
		log.Fatal(err)
	}
	stsClient := sts.NewFromConfig(cfg)
	callerIdentity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		log.Fatal(fmt.Errorf("failed to authenticate with aws: %v", err))
	}
	printHeader(*privateMode, callerIdentity)

	// Create configuration with all clients
	config := &Config{
		ECSClient:            ecs.NewFromConfig(cfg),
		EC2Client:            ec2.NewFromConfig(cfg),
		IAMClient:            iam.NewFromConfig(cfg),
		ELBv2Client:          elasticloadbalancingv2.NewFromConfig(cfg),
		CloudWatchLogsClient: cloudwatchlogs.NewFromConfig(cfg),
		Region:               *region,
		PrivateMode:          *privateMode,
	}

	// Step 1: List and select cluster
	clusters, err := getClustersWithProgress(ctx, config)
	if err != nil {
		log.Fatal(err)
	}
	if len(clusters) == 0 {
		log.Fatal("No clusters found")
	}

	selectedCluster := selectCluster(clusters)
	fmt.Printf(
		"Selected cluster: %s\n",
		colorBold(selectedCluster.Name, ColorGreen),
	)

	// Step 2: List and select service
	services, err := getServicesWithProgress(ctx, config, selectedCluster.Name)
	if err != nil {
		log.Fatal(err)
	}
	if len(services) == 0 {
		log.Fatal("No services found in cluster")
	}

	selectedService := selectService(services)
	fmt.Printf(
		"Selected service: %s\n",
		colorBold(selectedService.Name, ColorGreen),
	)

	// Step 3: Show current service information
	taskDef, err := showProgressWithResult("Loading task definition...", func() (*types.TaskDefinition, error) {
		return getTaskDefinition(ctx, config, selectedService.TaskDefinition)
	})
	if err != nil {
		log.Fatal(err)
	}

	containerImage := getContainerImage(taskDef)
	fmt.Printf(
		"Current container image: %s\n",
		colorBold(containerImage, ColorCyan),
	)
	fmt.Printf(
		"Current capacity: Desired=%d, Running=%d\n",
		selectedService.DesiredCount, selectedService.RunningCount,
	)

	// Step 4: Select action
	action := selectAction()
	reader := bufio.NewReader(os.Stdin)

	switch action {
	case "image":
		// Update image version
		fmt.Printf("%s", color("Enter new image version/tag: ", ColorYellow))
		newImageVersion, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}
		newImageVersion = strings.TrimSpace(newImageVersion)
		if newImageVersion == "" {
			fmt.Println("No version provided. Exiting")
			return
		}

		// Create new task definition with updated image
		newImage := updateImageVersion(containerImage, newImageVersion)
		fmt.Printf("Updating image to: %s\n", colorBold(newImage, ColorCyan))

		newTaskDefArn, err := showProgressWithResult("Creating new task definition...", func() (string, error) {
			return createNewTaskDefinition(ctx, config, taskDef, newImage)
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Created new task definition: %s\n", colorBold(newTaskDefArn, ColorGreen))

		// Update service with new task definition
		fmt.Printf("%s", color("Force update service? (y/N): ", ColorYellow))
		confirmInput, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}
		confirmInput = strings.TrimSpace(confirmInput)

		if confirmInput != "y" && confirmInput != "Y" && confirmInput != "yes" {
			fmt.Println("Service update cancelled")
			return
		}

		err = showProgress("Updating ECS service...", func() error {
			return updateService(ctx, config, selectedCluster.Name, selectedService.Name, newTaskDefArn)
		})
		if err != nil {
			log.Fatal(err)
		}

		fmt.Printf("Service %s updated successfully!\n", colorBold(selectedService.Name, ColorGreen))

	case "capacity":
		// Update service capacity
		fmt.Printf("%s", color("Enter new capacity (min,desired,max): ", ColorYellow))
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
			colorBold(fmt.Sprintf("%d", minCount), ColorCyan),
			colorBold(fmt.Sprintf("%d", desiredCount), ColorCyan),
			colorBold(fmt.Sprintf("%d", maxCount), ColorCyan))

		if adjusted {
			fmt.Printf("Deployment config: MinHealthy=%d%%, MaxPercent=%d%% (adjusted for rolling deployments)\n",
				minHealthyPercent, maxPercent)
		} else {
			fmt.Printf("Deployment config: MinHealthy=%d%%, MaxPercent=%d%%\n",
				minHealthyPercent, maxPercent)
		}

		// Update service capacity
		fmt.Printf("%s", color("Update service capacity? (y/N): ", ColorYellow))
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

		fmt.Printf("Service %s capacity updated successfully!\n", colorBold(selectedService.Name, ColorGreen))

	case "logs":
		// Stream service logs
		fmt.Printf("Streaming logs for service: %s\n", colorBold(selectedService.Name, ColorCyan))
		fmt.Printf("Press Ctrl+C to stop streaming\n\n")

		err = streamServiceLogs(ctx, config, selectedCluster.Name, selectedService.Name, taskDef)
		if err != nil {
			log.Fatal(err)
		}

	case "connect":
		// Connect to container
		fmt.Printf("Connecting to container for service: %s\n", colorBold(selectedService.Name, ColorCyan))

		err = connectToContainer(ctx, config, selectedCluster.Name, selectedService.Name, taskDef)
		if err != nil {
			log.Fatal(err)
		}

	case "force-update":
		// Force update service
		fmt.Printf("Force updating service: %s\n", colorBold(selectedService.Name, ColorCyan))

		// Confirm the force update
		fmt.Printf("%s", color("Force update will restart all tasks. Continue? (y/N): ", ColorYellow))
		confirmInput, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}
		confirmInput = strings.TrimSpace(confirmInput)

		if confirmInput != "y" && confirmInput != "Y" && confirmInput != "yes" {
			fmt.Println("Force update cancelled")
			return
		}

		err = forceUpdateService(ctx, config, selectedCluster.Name, selectedService.Name)
		if err != nil {
			log.Fatal(err)
		}
	}
}

// getClusters retrieves all ECS clusters from the AWS account and returns them
// as a sorted list of ClusterInfo structs.
func getClustersWithProgress(ctx context.Context, config *Config) ([]*ClusterInfo, error) {
	// Show initial progress
	fmt.Printf("⠋ Loading ECS clusters...\r")

	paginator := ecs.NewListClustersPaginator(
		config.ECSClient, &ecs.ListClustersInput{},
	)

	var clusterArns []string
	pageNum := 1
	for paginator.HasMorePages() {
		fmt.Printf("⠋ Grabbing page %d...\r", pageNum)
		output, err := paginator.NextPage(ctx)
		if err != nil {
			fmt.Print("\r\033[K")
			return nil, err
		}
		clusterArns = append(clusterArns, output.ClusterArns...)
		pageNum++
	}

	if len(clusterArns) == 0 {
		fmt.Print("\r\033[K")
		return []*ClusterInfo{}, nil
	}

	// Get detailed cluster information
	fmt.Printf("⠋ Getting cluster details...\r")
	describeOutput, err := config.ECSClient.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: clusterArns,
		Include:  []types.ClusterField{types.ClusterFieldStatistics},
	})
	if err != nil {
		fmt.Print("\r\033[K")
		return nil, err
	}

	clusters := make([]*ClusterInfo, len(describeOutput.Clusters))
	for i, cluster := range describeOutput.Clusters {
		clusters[i] = &ClusterInfo{
			Name:           *cluster.ClusterName,
			ARN:            *cluster.ClusterArn,
			Status:         *cluster.Status,
			RunningTasks:   cluster.RunningTasksCount,
			ActiveServices: cluster.ActiveServicesCount,
		}
	}

	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].Name < clusters[j].Name
	})

	// Clear the progress line
	fmt.Print("\r\033[K")
	return clusters, nil
}

// getServices retrieves all ECS services from the specified cluster and returns them
// as a sorted list of ServiceInfo structs.
func getServicesWithProgress(ctx context.Context, config *Config, clusterName string) ([]*ServiceInfo, error) {
	// Show initial progress
	fmt.Printf("⠋ Loading ECS services...\r")

	paginator := ecs.NewListServicesPaginator(
		config.ECSClient, &ecs.ListServicesInput{
			Cluster: &clusterName,
		},
	)

	var serviceArns []string
	pageNum := 1
	for paginator.HasMorePages() {
		fmt.Printf("⠋ Grabbing page %d...\r", pageNum)
		output, err := paginator.NextPage(ctx)
		if err != nil {
			fmt.Print("\r\033[K")
			return nil, err
		}
		serviceArns = append(serviceArns, output.ServiceArns...)
		pageNum++
	}

	if len(serviceArns) == 0 {
		fmt.Print("\r\033[K")
		return []*ServiceInfo{}, nil
	}

	// Get detailed service information in batches of 10 (AWS limit)
	var allServices []*ServiceInfo
	batchSize := 10
	totalBatches := (len(serviceArns) + batchSize - 1) / batchSize

	for i := 0; i < len(serviceArns); i += batchSize {
		batchNum := (i / batchSize) + 1
		fmt.Printf("⠋ Processing batch %d/%d (%d services)...\r", batchNum, totalBatches, len(serviceArns))

		end := i + batchSize
		if end > len(serviceArns) {
			end = len(serviceArns)
		}
		batch := serviceArns[i:end]

		describeOutput, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  &clusterName,
			Services: batch,
		})
		if err != nil {
			fmt.Print("\r\033[K")
			return nil, err
		}

		for _, service := range describeOutput.Services {
			allServices = append(allServices, &ServiceInfo{
				Name:           *service.ServiceName,
				ARN:            *service.ServiceArn,
				Status:         *service.Status,
				DesiredCount:   service.DesiredCount,
				RunningCount:   service.RunningCount,
				TaskDefinition: *service.TaskDefinition,
			})
		}
	}

	// Clear the progress line
	fmt.Print("\r\033[K")

	services := allServices

	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	return services, nil
}

// getTaskDefinition retrieves the task definition details for the specified ARN.
func getTaskDefinition(ctx context.Context, config *Config, taskDefArn string) (*types.TaskDefinition, error) {
	output, err := config.ECSClient.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: &taskDefArn,
	})
	if err != nil {
		return nil, err
	}

	return output.TaskDefinition, nil
}

// getContainerImage extracts the container image from the first container definition.
func getContainerImage(taskDef *types.TaskDefinition) string {
	if len(taskDef.ContainerDefinitions) == 0 {
		return "no containers found"
	}

	// Return the image of the first container (as per requirements)
	return *taskDef.ContainerDefinitions[0].Image
}

// updateImageVersion updates the image version/tag while preserving the base image name.
func updateImageVersion(currentImage, newVersion string) string {
	// Split image into base and tag
	parts := strings.Split(currentImage, ":")
	if len(parts) == 1 {
		// No tag present, just append the new version
		return currentImage + ":" + newVersion
	}

	// Replace the tag with the new version
	baseImage := strings.Join(parts[:len(parts)-1], ":")
	return baseImage + ":" + newVersion
}

// createNewTaskDefinition creates a new task definition with the updated image.
func createNewTaskDefinition(ctx context.Context, config *Config, taskDef *types.TaskDefinition, newImage string) (string, error) {
	// Create a copy of the container definitions with updated image
	newContainerDefs := make([]types.ContainerDefinition, len(taskDef.ContainerDefinitions))
	for i, container := range taskDef.ContainerDefinitions {
		newContainerDefs[i] = container
		// Update the first container's image (as per requirements)
		if i == 0 {
			newContainerDefs[i].Image = &newImage
		}
	}

	// Create new task definition
	output, err := config.ECSClient.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  taskDef.Family,
		ContainerDefinitions:    newContainerDefs,
		TaskRoleArn:             taskDef.TaskRoleArn,
		ExecutionRoleArn:        taskDef.ExecutionRoleArn,
		NetworkMode:             taskDef.NetworkMode,
		RequiresCompatibilities: taskDef.RequiresCompatibilities,
		Cpu:                     taskDef.Cpu,
		Memory:                  taskDef.Memory,
		Volumes:                 taskDef.Volumes,
		PlacementConstraints:    taskDef.PlacementConstraints,
	})
	if err != nil {
		return "", err
	}

	return *output.TaskDefinition.TaskDefinitionArn, nil
}

// updateService updates the ECS service with the new task definition.
func updateService(ctx context.Context, config *Config, clusterName, serviceName, newTaskDefArn string) error {
	_, err := config.ECSClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:            &clusterName,
		Service:            &serviceName,
		TaskDefinition:     &newTaskDefArn,
		ForceNewDeployment: true, // Force new deployment
	})
	return err
}

// selectCluster displays clusters and allows user to select one.
func selectCluster(clusters []*ClusterInfo) *ClusterInfo {
	fmt.Printf("\n%s\n", color("Available ECS Clusters:", ColorBlue))

	longestName := 0
	for _, cluster := range clusters {
		if len(cluster.Name) > longestName {
			longestName = len(cluster.Name)
		}
	}

	for i, cluster := range clusters {
		// Alternate row colors for better readability
		var rowColor string
		if i%2 == 0 {
			rowColor = ColorWhite
		} else {
			rowColor = ColorCyan
		}

		// Color code the status
		statusColor := colorClusterStatus(cluster.Status)
		entry := fmt.Sprintf(
			"%3d. %-*s %s [%s] (%d tasks, %d services)",
			i+1, longestName, cluster.Name, cluster.ARN,
			color(cluster.Status, statusColor),
			cluster.RunningTasks, cluster.ActiveServices,
		)
		fmt.Println(color(entry, rowColor))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", color("Select cluster. Blank, or non-numeric input will exit: ", ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = input[:len(input)-1]
	if input == "" {
		fmt.Println("Exiting")
		os.Exit(0)
	}
	inputInt, err := strconv.Atoi(input)
	if err != nil {
		fmt.Println("Non-numeric input. Exiting")
		os.Exit(0)
	}
	if inputInt < 1 || inputInt > len(clusters) {
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	return clusters[inputInt-1]
}

// selectService displays services and allows user to select one.
func selectService(services []*ServiceInfo) *ServiceInfo {
	fmt.Printf("\n%s\n", color("Available ECS Services:", ColorBlue))

	longestName := 0
	for _, service := range services {
		if len(service.Name) > longestName {
			longestName = len(service.Name)
		}
	}

	for i, service := range services {
		// Alternate row colors for better readability
		var rowColor string
		if i%2 == 0 {
			rowColor = ColorWhite
		} else {
			rowColor = ColorCyan
		}

		// Color code the status
		statusColor := colorServiceStatus(service.Status)
		entry := fmt.Sprintf(
			"%3d. %-*s [%s] (%d/%d running)",
			i+1, longestName, service.Name,
			color(service.Status, statusColor),
			service.RunningCount, service.DesiredCount,
		)
		fmt.Println(color(entry, rowColor))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", color("Select service. Blank, or non-numeric input will exit: ", ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = input[:len(input)-1]
	if input == "" {
		fmt.Println("Exiting")
		os.Exit(0)
	}
	inputInt, err := strconv.Atoi(input)
	if err != nil {
		fmt.Println("Non-numeric input. Exiting")
		os.Exit(0)
	}
	if inputInt < 1 || inputInt > len(services) {
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	return services[inputInt-1]
}

// Helper functions

// color wraps a string with the specified color code
func color(text, colorCode string) string {
	return colorCode + text + ColorReset
}

// colorBold wraps a string with the specified color code and bold formatting
func colorBold(text, colorCode string) string {
	return colorCode + ColorBold + text + ColorReset
}

func printHeader(privateMode bool, callerIdentity *sts.GetCallerIdentityOutput) {
	header := []string{
		color(strings.Repeat("-", 40), ColorBlue),
		"-- ECS Quick Manager --",
		color(strings.Repeat("-", 40), ColorBlue),
	}
	if !privateMode {
		header = append(header, fmt.Sprintf(
			"  Account: %s \n  User: %s",
			*callerIdentity.Account, *callerIdentity.Arn,
		))
		header = append(header, color(strings.Repeat("-", 40), ColorBlue))
	}

	fmt.Println(strings.Join(header, "\n"))
}

func colorClusterStatus(status string) string {
	switch status {
	case "ACTIVE":
		return ColorGreen
	case "INACTIVE":
		return ColorRed
	case "PROVISIONING":
		return ColorYellow
	case "DEPROVISIONING":
		return ColorYellow
	default:
		return ColorWhite
	}
}

func colorServiceStatus(status string) string {
	switch status {
	case "ACTIVE":
		return ColorGreen
	case "DRAINING":
		return ColorYellow
	case "INACTIVE":
		return ColorRed
	default:
		return ColorWhite
	}
}

// resolveVersion returns the version string. If ldflags-injected version is empty,
// it attempts to derive a dev version from version/version.go, but will not be able
// to display the compile date.
func resolveVersion() string {
	if strings.TrimSpace(version) != "" {
		return version
	}
	if strings.TrimSpace(versionpkg.Full) != "" {
		return versionpkg.Full
	}
	// LOG Warning
	log.Println(
		"[WARNING]: This version was not compiled with a version tag.",
		"Usually this means that the binary was built locally.",
	)
	return fmt.Sprintf("v%d.%d.%s", versionpkg.Major, versionpkg.Minor, "unknown")
}

// selectAction displays available actions and allows user to select one.
func selectAction() string {
	fmt.Printf("\n%s\n", color("Available Actions:", ColorBlue))
	fmt.Printf("  1. Update container image version\n")
	fmt.Printf("  2. Update service capacity (min, desired, max)\n")
	fmt.Printf("  3. Stream service logs\n")
	fmt.Printf("  4. Connect to container\n")
	fmt.Printf("  5. Force update service\n")

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", color("Select action (1-5). Blank, or non-numeric input will exit: ", ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = input[:len(input)-1]
	if input == "" {
		fmt.Println("Exiting")
		os.Exit(0)
	}
	inputInt, err := strconv.Atoi(input)
	if err != nil {
		fmt.Println("Non-numeric input. Exiting")
		os.Exit(0)
	}
	if inputInt < 1 || inputInt > 5 {
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	switch inputInt {
	case 1:
		return "image"
	case 2:
		return "capacity"
	case 3:
		return "logs"
	case 4:
		return "connect"
	case 5:
		return "force-update"
	default:
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
		return ""
	}
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

// Progress indicator functions
var throbberChars = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// showProgress runs a throbber animation while executing a function
func showProgress(message string, fn func() error) error {
	done := make(chan error, 1)

	// Start the operation in a goroutine
	go func() {
		done <- fn()
	}()

	// Show throbber while waiting
	i := 0
	for {
		select {
		case err := <-done:
			// Clear the line and return
			fmt.Print("\r\033[K")
			return err
		default:
			fmt.Printf("\r%s %s", throbberChars[i%len(throbberChars)], message)
			time.Sleep(100 * time.Millisecond)
			i++
		}
	}
}

// showProgressWithResult runs a throbber animation while executing a function that returns a result
func showProgressWithResult[T any](message string, fn func() (T, error)) (T, error) {
	done := make(chan struct {
		result T
		err    error
	}, 1)

	// Start the operation in a goroutine
	go func() {
		result, err := fn()
		done <- struct {
			result T
			err    error
		}{result, err}
	}()

	// Show throbber while waiting
	i := 0
	for {
		select {
		case res := <-done:
			// Clear the line and return
			fmt.Print("\r\033[K")
			return res.result, res.err
		default:
			fmt.Printf("\r%s %s", throbberChars[i%len(throbberChars)], message)
			time.Sleep(100 * time.Millisecond)
			i++
		}
	}
}

// showProgressWithCallback runs a throbber animation while executing a function that can report progress
func showProgressWithCallback[T any](baseMessage string, fn func(updateProgress func(string)) (T, error)) (T, error) {
	done := make(chan struct {
		result T
		err    error
	}, 1)

	// Start the operation in a goroutine
	go func() {
		updateProgress := func(message string) {
			// This will be called by the function to update progress
		}
		result, err := fn(updateProgress)
		done <- struct {
			result T
			err    error
		}{result, err}
	}()

	// Show throbber while waiting
	i := 0
	for {
		select {
		case res := <-done:
			// Clear the line and return
			fmt.Print("\r\033[K")
			return res.result, res.err
		default:
			fmt.Printf("\r%s %s", throbberChars[i%len(throbberChars)], baseMessage)
			time.Sleep(100 * time.Millisecond)
			i++
		}
	}
}

// streamServiceLogs streams logs from the ECS service to the terminal
func streamServiceLogs(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition) error {
	// Get the log group name from the task definition
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
		StartFromHead: boolPtr(false),
		Limit:         int32Ptr(50),
	}

	// If we have a last timestamp, start from there
	if *lastTimestamp > 0 {
		input.StartTime = int64Ptr(*lastTimestamp + 1)
	}

	output, err := config.CloudWatchLogsClient.GetLogEvents(ctx, input)
	if err != nil {
		return err
	}

	// Display new log events
	newLogsCount := 0
	for _, event := range output.Events {
		if *event.Timestamp > *lastTimestamp {
			timestamp := time.Unix(*event.Timestamp/1000, 0).Format("2006-01-02 15:04:05")
			message := strings.TrimSpace(*event.Message)
			fmt.Printf("[%s] %s\n", color(timestamp, ColorCyan), message)
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

// int32Ptr returns a pointer to an int32 value
func int32Ptr(v int32) *int32 {
	return &v
}

// int64Ptr returns a pointer to an int64 value
func int64Ptr(v int64) *int64 {
	return &v
}

// boolPtr returns a pointer to a bool value
func boolPtr(v bool) *bool {
	return &v
}

// ContainerInfo represents a container with its task information
type ContainerInfo struct {
	Name      string
	TaskID    string
	TaskARN   string
	LastThree string // Last three digits of task ID for display
}

// connectToContainer connects to a container using ECS Exec
func connectToContainer(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition) error {
	// Get running tasks for the service
	tasks, err := getRunningTasks(ctx, config, clusterName, serviceName)
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
	input = input[:len(input)-1]
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

	selected := containers[inputInt-1]
	fmt.Printf("Selected container: %s-%s\n", colorBold(selected.Name, ColorGreen), selected.LastThree)
	return selected
}

// executeECSExec runs ECS Exec to connect to the container
func executeECSExec(ctx context.Context, config *Config, clusterName, serviceName, taskARN, containerName string) error {
	// First, try to execute ECS Exec
	err := tryECSExec(ctx, config, clusterName, taskARN, containerName)
	if err == nil {
		return nil // Success
	}

	// Check if the error is about ECS Exec not being enabled
	if strings.Contains(err.Error(), "ECS Exec is not enabled") ||
		strings.Contains(err.Error(), "execute-command") ||
		strings.Contains(err.Error(), "ExecuteCommandAgent") {

		fmt.Printf("%s ECS Exec is not enabled for this service.\n", color("Warning:", ColorYellow))
		fmt.Printf("Would you like to enable ECS Exec for service %s? (y/N): ", colorBold(serviceName, ColorCyan))

		reader := bufio.NewReader(os.Stdin)
		confirmInput, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %v", err)
		}
		confirmInput = strings.TrimSpace(confirmInput)

		if confirmInput != "y" && confirmInput != "Y" && confirmInput != "yes" {
			fmt.Println("ECS Exec not enabled. Exiting.")
			return nil
		}

		// Enable ECS Exec for the service
		fmt.Printf("Enabling ECS Exec for service %s...\n", colorBold(serviceName, ColorCyan))
		err = enableECSExecForService(ctx, config, clusterName, serviceName)
		if err != nil {
			return fmt.Errorf("failed to enable ECS Exec: %v", err)
		}

		fmt.Printf("ECS Exec enabled successfully! Waiting for service to update...\n")
		fmt.Printf("This may take a few minutes. Retrying connection...\n\n")

		// Wait a moment for the service to update
		time.Sleep(5 * time.Second)

		// Try ECS Exec again
		err = tryECSExec(ctx, config, clusterName, taskARN, containerName)
		if err != nil {
			return fmt.Errorf("ECS Exec connection failed after enabling: %v", err)
		}
	}

	return err
}

// tryECSExec attempts to execute ECS Exec command
func tryECSExec(ctx context.Context, config *Config, clusterName, taskARN, containerName string) error {
	// Execute the ECS Exec command
	cmd := exec.Command("aws", "ecs", "execute-command",
		"--cluster", clusterName,
		"--task", taskARN,
		"--container", containerName,
		"--interactive",
		"--command", "sh")

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Connecting to container %s in task %s...\n",
		colorBold(containerName, ColorCyan),
		colorBold(extractTaskID(taskARN), ColorCyan))
	fmt.Printf("Use 'exit' to disconnect from the container\n\n")

	return cmd.Run()
}

// enableECSExecForService enables ECS Exec for the specified service
func enableECSExecForService(ctx context.Context, config *Config, clusterName, serviceName string) error {
	// Get current service configuration
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

// forceUpdateService forces a new deployment and waits for completion
func forceUpdateService(ctx context.Context, config *Config, clusterName, serviceName string) error {
	// Force a new deployment
	fmt.Printf("Initiating force update for service %s...\n", colorBold(serviceName, ColorCyan))

	_, err := config.ECSClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:            &clusterName,
		Service:            &serviceName,
		ForceNewDeployment: true,
	})
	if err != nil {
		return fmt.Errorf("failed to force update service: %v", err)
	}

	fmt.Printf("Force update initiated successfully!\n")
	fmt.Printf("Waiting for deployment to complete...\n\n")

	// Poll for deployment completion
	return pollDeploymentCompletion(ctx, config, clusterName, serviceName)
}

// pollDeploymentCompletion polls the service until all tasks are running
func pollDeploymentCompletion(ctx context.Context, config *Config, clusterName, serviceName string) error {
	maxAttempts := 60 // 10 minutes with 10-second intervals
	attempt := 0

	for attempt < maxAttempts {
		attempt++

		// Start continuous throbber animation for this cycle
		done := make(chan bool, 1)
		go func() {
			throbberIndex := 0
			for {
				select {
				case <-done:
					return
				default:
					throbber := throbberChars[throbberIndex%len(throbberChars)]
					fmt.Printf("%s Checking deployment status... (cycle %d/%d)\r", throbber, attempt, maxAttempts)
					throbberIndex++
					time.Sleep(200 * time.Millisecond) // Update throbber every 200ms
				}
			}
		}()

		// Get service details
		services, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  &clusterName,
			Services: []string{serviceName},
		})

		// Stop the throbber animation
		done <- true

		if err != nil {
			fmt.Printf("\r\033[K") // Clear the progress line
			return fmt.Errorf("failed to describe service: %v", err)
		}

		if len(services.Services) == 0 {
			fmt.Printf("\r\033[K") // Clear the progress line
			return fmt.Errorf("service %s not found", serviceName)
		}

		service := services.Services[0]

		// Check if deployment is complete
		if len(service.Deployments) > 0 {
			primaryDeployment := service.Deployments[0]

			// Check if the primary deployment is running and stable
			if primaryDeployment.Status != nil && *primaryDeployment.Status == "PRIMARY" {
				running := primaryDeployment.RunningCount
				desired := primaryDeployment.DesiredCount
				pending := primaryDeployment.PendingCount

				fmt.Printf("\r\033[K") // Clear the progress line
				fmt.Printf("Deployment status: %d/%d tasks running (pending: %d) - cycle %d\n",
					running, desired, pending, attempt)

				if running == desired && running > 0 && pending == 0 {
					fmt.Printf("✅ Force update completed successfully!\n")
					fmt.Printf("All %d tasks are running and healthy after %d refresh cycles.\n", running, attempt)
					return nil
				}
			}
		}

		// Wait before next attempt
		time.Sleep(10 * time.Second)
	}

	// Clear the progress line
	fmt.Printf("\r\033[K")
	return fmt.Errorf("force update timed out after %d refresh cycles (%d minutes)", attempt, maxAttempts/6)
}
