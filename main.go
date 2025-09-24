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
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
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

	ecsClient := ecs.NewFromConfig(cfg)

	// Step 1: List and select cluster
	clusters, err := getClusters(ctx, ecsClient)
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
	services, err := getServices(ctx, ecsClient, selectedCluster.Name)
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

	// Step 3: Get task definition and show container image
	taskDef, err := getTaskDefinition(ctx, ecsClient, selectedService.TaskDefinition)
	if err != nil {
		log.Fatal(err)
	}

	containerImage := getContainerImage(taskDef)
	fmt.Printf(
		"Current container image: %s\n",
		colorBold(containerImage, ColorCyan),
	)

	// Step 4: Get new image version from user
	reader := bufio.NewReader(os.Stdin)
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

	// Step 5: Create new task definition with updated image
	newImage := updateImageVersion(containerImage, newImageVersion)
	fmt.Printf("Updating image to: %s\n", colorBold(newImage, ColorCyan))

	newTaskDefArn, err := createNewTaskDefinition(ctx, ecsClient, taskDef, newImage)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Created new task definition: %s\n", colorBold(newTaskDefArn, ColorGreen))

	// Step 6: Update service with new task definition
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

	err = updateService(ctx, ecsClient, selectedCluster.Name, selectedService.Name, newTaskDefArn)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Service %s updated successfully!\n", colorBold(selectedService.Name, ColorGreen))
}

// getClusters retrieves all ECS clusters from the AWS account and returns them
// as a sorted list of ClusterInfo structs.
func getClusters(ctx context.Context, ecsClient *ecs.Client) ([]*ClusterInfo, error) {
	paginator := ecs.NewListClustersPaginator(
		ecsClient, &ecs.ListClustersInput{},
	)

	var clusterArns []string
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		clusterArns = append(clusterArns, output.ClusterArns...)
	}

	if len(clusterArns) == 0 {
		return []*ClusterInfo{}, nil
	}

	// Get detailed cluster information
	describeOutput, err := ecsClient.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: clusterArns,
		Include:  []types.ClusterField{types.ClusterFieldStatistics},
	})
	if err != nil {
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

	return clusters, nil
}

// getServices retrieves all ECS services from the specified cluster and returns them
// as a sorted list of ServiceInfo structs.
func getServices(ctx context.Context, ecsClient *ecs.Client, clusterName string) ([]*ServiceInfo, error) {
	paginator := ecs.NewListServicesPaginator(
		ecsClient, &ecs.ListServicesInput{
			Cluster: &clusterName,
		},
	)

	var serviceArns []string
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		serviceArns = append(serviceArns, output.ServiceArns...)
	}

	if len(serviceArns) == 0 {
		return []*ServiceInfo{}, nil
	}

	// Get detailed service information
	describeOutput, err := ecsClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: serviceArns,
	})
	if err != nil {
		return nil, err
	}

	services := make([]*ServiceInfo, len(describeOutput.Services))
	for i, service := range describeOutput.Services {
		services[i] = &ServiceInfo{
			Name:           *service.ServiceName,
			ARN:            *service.ServiceArn,
			Status:         *service.Status,
			DesiredCount:   service.DesiredCount,
			RunningCount:   service.RunningCount,
			TaskDefinition: *service.TaskDefinition,
		}
	}

	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	return services, nil
}

// getTaskDefinition retrieves the task definition details for the specified ARN.
func getTaskDefinition(ctx context.Context, ecsClient *ecs.Client, taskDefArn string) (*types.TaskDefinition, error) {
	output, err := ecsClient.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
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
func createNewTaskDefinition(ctx context.Context, ecsClient *ecs.Client, taskDef *types.TaskDefinition, newImage string) (string, error) {
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
	output, err := ecsClient.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
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
func updateService(ctx context.Context, ecsClient *ecs.Client, clusterName, serviceName, newTaskDefArn string) error {
	_, err := ecsClient.UpdateService(ctx, &ecs.UpdateServiceInput{
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
