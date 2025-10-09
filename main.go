// Package main provides a command-line tool for quickly managing AWS ECS services.
// The tool lists all ECS clusters and services, allows selection, and provides
// functionality to update container images and force service updates.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	qc "github.com/bevelwork/quick_color"
	versionpkg "github.com/bevelwork/quick_ecs/version"
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
	DesiredCount   int32  // Desired number of tasks
	RunningCount   int32  // Number of running tasks
	TaskDefinition string // The task definition ARN
}

// Config holds all AWS clients and application configuration
type Config struct {
	ECSClient            *ecs.Client
	EC2Client            *ec2.Client
	IAMClient            *iam.Client
	ELBv2Client          *elasticloadbalancingv2.Client
	CloudWatchLogsClient *cloudwatchlogs.Client
	Region               string
	PrivateMode          bool
}

// version is set at build time via ldflags
var version = ""

// state helpers moved to state.go

func main() {
	// Parse command line flags
	region := flag.String("region", "us-east-1", "AWS region to use")
	privateMode := flag.Bool("private", false, "Enable private mode (hide account information)")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	// Handle version flag
	if *showVersion {
		fmt.Println(resolveVersion())
		os.Exit(0)
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

	selectedCluster := selectCluster(clusters, config)
	fmt.Printf("Selected cluster: %s\n", qc.ColorizeBold(selectedCluster.Name, qc.ColorGreen))

	// Handle repeat-last sentinel
	if selectedCluster.Name == "__REPEAT_LAST__" {
		last, err := loadLastState()
		if err != nil || last == nil {
			log.Fatal("No previous state found to repeat")
		}
		fmt.Printf("%s Repeating last action: cluster=%s service=%s action=%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen), qc.ColorizeBold(last.Action, qc.ColorYellow))
		if err := runRepeatLastAction(ctx, config, last); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Step 2: List and select service
	services, err := getServicesWithProgress(ctx, config, selectedCluster.Name)
	if err != nil {
		log.Fatal(err)
	}
	if len(services) == 0 {
		log.Fatal("No services found in cluster")
	}

	selectedService := selectService(services)
	fmt.Printf("Selected service: %s\n", qc.ColorizeBold(selectedService.Name, qc.ColorGreen))

	// Step 3: Show current service information
	taskDef, err := showProgressWithResult("Loading task definition...", func() (*types.TaskDefinition, error) {
		return getTaskDefinition(ctx, config, selectedService.TaskDefinition)
	})
	if err != nil {
		log.Fatal(err)
	}

	containerImage := getContainerImage(taskDef)
	fmt.Printf("Current container image: %s\n", qc.ColorizeBold(containerImage, qc.ColorCyan))
	fmt.Printf(
		"Current capacity: Desired=%d, Running=%d\n",
		selectedService.DesiredCount, selectedService.RunningCount,
	)

	// Step 4: Select action
	action := selectAction()

	switch action {
	case "dashboard":
		dashboardAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "dashboard"})
	case "image":
		updateImageAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "image"})
	case "enable-exec":
		enableExecAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "enable-exec"})
	case "capacity":
		updateCapacityAction(ctx, config, selectedCluster, selectedService)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "capacity"})
	case "logs":
		streamLogsAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "logs"})
	case "task-logs":
		streamTaskLogsAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "task-logs"})
	case "watch-deployments":
		watchDeploymentsAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "watch-deployments"})
	case "connect":
		connectAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "connect"})
	case "force-update":
		forceUpdateAction(ctx, config, selectedCluster, selectedService)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "force-update"})
	case "check":
		checkAction(ctx, config, selectedCluster, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "check"})
	case "service-config":
		showServiceConfiguration(ctx, config, selectedCluster.Name, selectedService.Name)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "service-config"})
	case "security-groups":
		showSecurityGroups(ctx, config, selectedCluster.Name, selectedService.Name)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "security-groups"})
	case "task-defs":
		showTaskDefinitionHistory(ctx, config, selectedService.TaskDefinition)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "task-defs"})
	case "healthchecks":
		showHealthChecks(ctx, config, selectedCluster.Name, selectedService, taskDef)
		saveLastState(&LastState{Region: config.Region, ClusterName: selectedCluster.Name, ServiceName: selectedService.Name, Action: "healthchecks"})
	}
}

// getClustersWithProgress retrieves all ECS clusters with progress indication
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
	clusters, err := config.ECSClient.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: clusterArns,
		Include:  []types.ClusterField{types.ClusterFieldStatistics},
	})
	if err != nil {
		fmt.Print("\r\033[K")
		return nil, err
	}

	// Clear the progress line
	fmt.Print("\r\033[K")

	var clusterInfos []*ClusterInfo
	for _, cluster := range clusters.Clusters {
		clusterInfos = append(clusterInfos, &ClusterInfo{
			Name:           *cluster.ClusterName,
			ARN:            *cluster.ClusterArn,
			Status:         *cluster.Status,
			RunningTasks:   cluster.RunningTasksCount,
			ActiveServices: cluster.ActiveServicesCount,
		})
	}

	// Sort clusters by name
	sort.Slice(clusterInfos, func(i, j int) bool {
		return clusterInfos[i].Name < clusterInfos[j].Name
	})

	return clusterInfos, nil
}

// getServicesWithProgress retrieves all ECS services from the specified cluster with progress indication
func getServicesWithProgress(ctx context.Context, config *Config, clusterName string) ([]*ServiceInfo, error) {
	// Show initial progress (spinner)
	stop := startThrobber("Loading ECS services...")
	defer stop()

	paginator := ecs.NewListServicesPaginator(
		config.ECSClient, &ecs.ListServicesInput{
			Cluster: &clusterName,
		},
	)

	var serviceArns []string
	pageNum := 1
	for paginator.HasMorePages() {
		fmt.Printf("\r⠋ Grabbing page %d...", pageNum)
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		serviceArns = append(serviceArns, output.ServiceArns...)
		pageNum++
	}

	if len(serviceArns) == 0 {
		return []*ServiceInfo{}, nil
	}

	// Get detailed service information in batches of 10 (AWS limit)
	var allServices []*ServiceInfo
	batchSize := 10
	totalBatches := (len(serviceArns) + batchSize - 1) / batchSize

	for i := 0; i < len(serviceArns); i += batchSize {
		batchNum := (i / batchSize) + 1
		fmt.Printf("\r⠋ Processing batch %d/%d (%d services)...", batchNum, totalBatches, len(serviceArns))

		end := min(i+batchSize, len(serviceArns))
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

// showServiceConfiguration prints details from DescribeServices for a single service
func showServiceConfiguration(ctx context.Context, config *Config, clusterName, serviceName string) {
	out, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &clusterName, Services: []string{serviceName}})
	if err != nil || len(out.Services) == 0 {
		fmt.Printf("%s Unable to describe service: %v\n", qc.Colorize("Error:", qc.ColorRed), err)
		return
	}
	s := out.Services[0]
	fmt.Printf("\n%s\n", qc.Colorize("Service Configuration:", qc.ColorBlue))
	fmt.Printf("Name: %s\n", *s.ServiceName)
	fmt.Printf("Status: %s  Running: %d  Desired: %d\n", *s.Status, s.RunningCount, s.DesiredCount)
	if s.TaskDefinition != nil {
		fmt.Printf("TaskDefinition: %s\n", sanitizeARN(*s.TaskDefinition, config.PrivateMode))
	}
	if len(s.LoadBalancers) > 0 && s.LoadBalancers[0].TargetGroupArn != nil {
		fmt.Printf("TargetGroup: %s\n", sanitizeARN(*s.LoadBalancers[0].TargetGroupArn, config.PrivateMode))
	}
	if s.DeploymentConfiguration != nil {
		dc := s.DeploymentConfiguration
		if dc.MinimumHealthyPercent != nil && dc.MaximumPercent != nil {
			fmt.Printf("Deployment: MinHealthy=%d%% MaxPercent=%d%%\n", *dc.MinimumHealthyPercent, *dc.MaximumPercent)
		}
	}
	if s.NetworkConfiguration != nil && s.NetworkConfiguration.AwsvpcConfiguration != nil {
		awsvpc := s.NetworkConfiguration.AwsvpcConfiguration
		if len(awsvpc.Subnets) > 0 {
			fmt.Printf("Subnets: %s\n", strings.Join(awsvpc.Subnets, ", "))
		}
		if len(awsvpc.SecurityGroups) > 0 {
			fmt.Printf("SecurityGroups: %s\n", strings.Join(awsvpc.SecurityGroups, ", "))
		}
	}

	// --- Security Group Configuration ---
	fmt.Printf("\n%s\n", qc.Colorize("Security Group Configuration:", qc.ColorBlue))

	// Step 1: Resolve ALB SGs and VPC
	albSgIds, vpcId := resolveAlbSecurityGroups(ctx, config, s)
	if len(albSgIds) > 0 {
		printSgListWithNamesMultiline(ctx, config, "ALB SecurityGroups:", albSgIds)
		// Aggregated rules for ALB SGs
		displayAggregatedSgRules(ctx, config, albSgIds, "ALB")
	} else {
		fmt.Printf("ALB SecurityGroups: (none or not applicable)\n")
	}

	// Step 2: Resolve Task SGs (awsvpc or EC2 instance-derived)
	taskSgIds, vpcId := resolveTaskSecurityGroups(ctx, config, clusterName, serviceName, s, vpcId)
	if len(taskSgIds) > 0 {
		printSgListWithNamesMultiline(ctx, config, "Task SecurityGroups:", taskSgIds)
		// Aggregated rules for Task SGs
		displayAggregatedSgRules(ctx, config, taskSgIds, "Task")
	} else {
		fmt.Printf("Task SecurityGroups: (none or not applicable)\n")
	}

	// --- Network Info ---
	fmt.Printf("\n%s\n", qc.Colorize("Network Info:", qc.ColorBlue))
	// Step 3: Determine subnets from service config (awsvpc)
	var subnets []string
	if s.NetworkConfiguration != nil && s.NetworkConfiguration.AwsvpcConfiguration != nil {
		subnets = append(subnets, s.NetworkConfiguration.AwsvpcConfiguration.Subnets...)
	}

	// Step 4: Describe subnets for names and VPC, fallback to existing VPC ID
	vpcFromSubnets, formatted := formatSubnetsWithNames(ctx, config, subnets)
	if vpcId == "" {
		vpcId = vpcFromSubnets
	}
	fmt.Printf("VPC: %s\n", vpcId)
	if len(formatted) > 0 {
		fmt.Printf("Subnets: %s\n", strings.Join(formatted, ", "))
	} else if len(subnets) > 0 {
		fmt.Printf("Subnets: %s\n", strings.Join(subnets, ", "))
	} else {
		fmt.Printf("Subnets: (none or not applicable)\n")
	}
}

// showTaskDefinitionHistory lists up to 10 recent task definition revisions for the same family as provided ARN
func showTaskDefinitionHistory(ctx context.Context, config *Config, taskDefArn string) error {
	// Extract family from ARN: arn:aws:ecs:region:acct:task-definition/family:revision
	family := taskDefArn
	if idx := strings.LastIndex(taskDefArn, ":"); idx != -1 {
		family = taskDefArn[:idx]
	}
	if slash := strings.LastIndex(family, "/"); slash != -1 {
		family = family[slash+1:]
	}

	// Get family metadata to determine default revision
	familyOut, _ := config.ECSClient.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: &taskDefArn})
	var currentInUse string
	if familyOut != nil && familyOut.TaskDefinition != nil && familyOut.TaskDefinition.TaskDefinitionArn != nil {
		currentInUse = *familyOut.TaskDefinition.TaskDefinitionArn
	}

	// Fetch latest 10 ARNs for family
	listOut, err := config.ECSClient.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: &family, Sort: types.SortOrderDesc, MaxResults: int32Ptr(10)})
	if err != nil {
		fmt.Printf("%s Unable to list task definitions: %v\n", qc.Colorize("Error:", qc.ColorRed), err)
		return err
	}

	// Determine latest and default
	latestArn := ""
	defaultArn := ""
	if len(listOut.TaskDefinitionArns) > 0 {
		latestArn = listOut.TaskDefinitionArns[0]
	}
	// Default is the highest ACTIVE that service picks by default; ECS has no dedicated default per family across revs,
	// so we will mark the one whose revision equals the service's task definition family+':'+maxRev among ACTIVE entries.
	// As a heuristic, mark latest as default if ACTIVE.
	if latestArn != "" {
		defaultArn = latestArn
	}

	fmt.Printf("\n%s\n", qc.Colorize("Task Definition History (latest up to 10):", qc.ColorBlue))
	for i, arn := range listOut.TaskDefinitionArns {
		indicators := []string{}
		if arn == latestArn {
			indicators = append(indicators, qc.Colorize("latest", qc.ColorGreen))
		}
		if arn == defaultArn {
			indicators = append(indicators, qc.Colorize("default", qc.ColorCyan))
		}
		if arn == currentInUse {
			indicators = append(indicators, qc.Colorize("in-use", qc.ColorYellow))
		}
		suffix := ""
		if len(indicators) > 0 {
			suffix = " [" + strings.Join(indicators, ", ") + "]"
		}
		fmt.Printf("  %2d. %s%s\n", i+1, sanitizeARN(arn, config.PrivateMode), suffix)
	}

	// Prompt for selection
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", qc.Colorize("Select a task definition to view/save (blank to exit): ", qc.ColorYellow))
	sel, _ := reader.ReadString('\n')
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil
	}
	idx, err := strconv.Atoi(sel)
	if err != nil || idx < 1 || idx > len(listOut.TaskDefinitionArns) {
		fmt.Println("Invalid selection.")
		return nil
	}
	chosenArn := listOut.TaskDefinitionArns[idx-1]

	// Describe selected task definition
	tdOut, err := config.ECSClient.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: &chosenArn})
	if err != nil || tdOut.TaskDefinition == nil {
		fmt.Printf("%s Failed to describe task definition: %v\n", qc.Colorize("Error:", qc.ColorRed), err)
		return err
	}

	// Pretty-print to terminal with camelCase conversion
	pretty, err := json.MarshalIndent(tdOut.TaskDefinition, "", "  ")
	if err != nil {
		fmt.Printf("%s Failed to marshal task definition: %v\n", qc.Colorize("Error:", qc.ColorRed), err)
		return err
	}

	// Convert capitalized JSON properties to camelCase
	camelCaseJSON, err := convertJSONToCamelCase(pretty)
	if err != nil {
		fmt.Printf("%s Failed to convert JSON to camelCase: %v\n", qc.Colorize("Error:", qc.ColorRed), err)
		return err
	}

	fmt.Printf("\n%s\n%s\n", qc.Colorize("Selected Task Definition:", qc.ColorBlue), string(camelCaseJSON))

	// Build filename: <definition-arn>.<version-number>.json (sanitize ARN for filesystem safety)
	rev := ""
	if i := strings.LastIndex(chosenArn, ":"); i != -1 {
		rev = chosenArn[i+1:]
	}
	safeArn := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(chosenArn)
	filename := fmt.Sprintf("%s.%s.json", safeArn, rev)
	if err := os.WriteFile(filename, camelCaseJSON, 0600); err != nil {
		fmt.Printf("%s Failed to write file %s: %v\n", qc.Colorize("Error:", qc.ColorRed), filename, err)
		return err
	}
	fmt.Printf("%s Saved to %s\n", qc.Colorize("Info:", qc.ColorGreen), filename)
	return nil
}

// selectCluster displays clusters and allows user to select one.
func selectCluster(clusters []*ClusterInfo, config *Config) *ClusterInfo {
	fmt.Printf("\n%s\n", qc.Colorize("Available ECS Clusters:", qc.ColorBlue))

	// Offer 0th option if last state exists
	last, _ := loadLastState()
	if last != nil {
		fmt.Printf("  0. %s %s -> %s\n", qc.ColorizeBold("Repeat last:", qc.ColorCyan), qc.Colorize(last.ClusterName, qc.ColorGreen), qc.Colorize(last.ServiceName+" ["+last.Action+"]", qc.ColorYellow))
	}

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
			rowColor = qc.ColorWhite
		} else {
			rowColor = qc.ColorCyan
		}

		// Color code the status
		statusColor := colorClusterStatus(cluster.Status)
		entry := fmt.Sprintf(
			"%3d. %-*s %s [%s] (%d tasks, %d services)",
			i+1, longestName, cluster.Name, sanitizeARN(cluster.ARN, config.PrivateMode),
			qc.Colorize(cluster.Status, statusColor),
			cluster.RunningTasks, cluster.ActiveServices,
		)
		fmt.Println(qc.Colorize(entry, rowColor))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", qc.Colorize("Select cluster (0 to repeat last). Blank, or non-numeric input will exit: ", qc.ColorYellow))
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
	if inputInt == 0 {
		// Repeat last action path triggered via sentinel return
		return &ClusterInfo{Name: "__REPEAT_LAST__"}
	}
	if inputInt < 1 || inputInt > len(clusters) {
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	return clusters[inputInt-1]
}

// selectService displays services and allows user to select one.
func selectService(services []*ServiceInfo) *ServiceInfo {
	fmt.Printf("\n%s\n", qc.Colorize("Available ECS Services:", qc.ColorBlue))

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
			rowColor = qc.ColorWhite
		} else {
			rowColor = qc.ColorCyan
		}

		// Color code the status
		statusColor := colorServiceStatus(service.Status)
		entry := fmt.Sprintf(
			"%3d. %-*s [%s] (%d/%d running)",
			i+1, longestName, service.Name,
			qc.Colorize(service.Status, statusColor),
			service.RunningCount, service.DesiredCount,
		)
		fmt.Println(qc.Colorize(entry, rowColor))
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", qc.Colorize("Select service. Press Enter for first option, or non-numeric input will exit: ", qc.ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = strings.TrimSpace(input)
	if input == "" {
		// Default to first service
		return services[0]
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

type Action struct {
	PrimaryShortcut string
	Shortcuts       []string
	Description     string
}

// selectAction displays available actions and allows user to select one.
func selectAction() string {
	// Define actions with canonical keys in PrimaryShortcut
	actions := []Action{
		{PrimaryShortcut: "dashboard", Shortcuts: []string{"d", "dash", "dashboard"}, Description: "[D]ashboard - Live monitoring of service config, deployments, tasks, and logs"},
		{PrimaryShortcut: "connect", Shortcuts: []string{"e", "x", "exec", "conn", "connect"}, Description: "[E]xec - Establish terminal session to container"},
		{PrimaryShortcut: "capacity", Shortcuts: []string{"cap"}, Description: "[Cap]acity - Update service capacity (min, desired, max)"},
		{PrimaryShortcut: "check", Shortcuts: []string{"c", "chk"}, Description: "[C]heck configuration"},
		{PrimaryShortcut: "force-update", Shortcuts: []string{"f", "force"}, Description: "[F]orce update service"},
		{PrimaryShortcut: "security-groups", Shortcuts: []string{"sg", "security", "secgroups"}, Description: "[Sg] Security groups (ALB and Task)"},
		{PrimaryShortcut: "healthchecks", Shortcuts: []string{"h", "hc", "health", "healthchecks", "health-checks"}, Description: "[H]ealth checks (task defs and ALB)"},
		{PrimaryShortcut: "image", Shortcuts: []string{"i", "img", "image"}, Description: "[I]mage - Update container image version"},
		{PrimaryShortcut: "logs", Shortcuts: []string{"l", "log", "logs"}, Description: "[L]ogs - Stream service-wide logs (all running tasks)"},
		{PrimaryShortcut: "task-logs", Shortcuts: []string{"tl", "tlog", "tasklog", "task-logs"}, Description: "[TL] Task logs - Stream logs for a specific task"},
		{PrimaryShortcut: "watch-deployments", Shortcuts: []string{"w", "watch", "deployments", "watch-deployments"}, Description: "[W]atch deployments - View and stream logs for recent deployments"},
		{PrimaryShortcut: "service-config", Shortcuts: []string{"s", "svc", "service"}, Description: "[S]ervice configuration"},
		{PrimaryShortcut: "task-defs", Shortcuts: []string{"t", "td", "task", "taskdefs", "task-defs", "task-definition"}, Description: "[T]ask definition history (latest 10)"},
		{PrimaryShortcut: "enable-exec", Shortcuts: []string{"enable", "enable-exec", "enable-execution", "enable-execution-mode", "exec-enable"}, Description: "Enable e[X]ecution mode (ECS Exec)"},
	}

	// Use actions in the order defined (no sorting)
	sorted := actions

	fmt.Printf("\n%s\n", qc.Colorize("Available Actions:", qc.ColorBlue))
	for idx, a := range sorted {
		fmt.Printf("%3d. %s\n", idx+1, a.Description)
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s", qc.Colorize("Select action (number or shortcut). Press Enter for first option, or invalid input will exit: ", qc.ColorYellow))
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		// Default to first action
		return sorted[0].PrimaryShortcut
	}

	// If numeric, map to sorted index
	if n, err := strconv.Atoi(input); err == nil {
		if n >= 1 && n <= len(sorted) {
			return sorted[n-1].PrimaryShortcut
		}
		fmt.Println("Invalid selection. Exiting")
		os.Exit(0)
	}

	// Otherwise, match by shortcut across all actions
	for _, a := range actions {
		if input == a.PrimaryShortcut {
			return a.PrimaryShortcut
		}
		if slices.Contains(a.Shortcuts, input) {
			return a.PrimaryShortcut
		}
	}

	fmt.Println("Invalid selection. Exiting")
	os.Exit(0)
	return ""
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

// Helper functions for compatibility with other modules

// boolPtr returns a pointer to a bool value
func boolPtr(v bool) *bool {
	return &v
}

// int32Ptr returns a pointer to an int32 value
func int32Ptr(v int32) *int32 {
	return &v
}

// runRepeatLastAction executes the stored last action without prompting
func runRepeatLastAction(ctx context.Context, config *Config, last *LastState) error {
	// Fetch current task definition for service (for actions that need it)
	// Describe service to get task definition
	svcOut, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &last.ClusterName, Services: []string{last.ServiceName}})
	if err != nil || len(svcOut.Services) == 0 {
		return fmt.Errorf("failed to describe service for repeat action")
	}
	tdArn := svcOut.Services[0].TaskDefinition
	taskDef, err := getTaskDefinition(ctx, config, *tdArn)
	if err != nil {
		return err
	}

	// Build minimal structs to call existing actions
	selectedCluster := &ClusterInfo{Name: last.ClusterName}
	selectedService := &ServiceInfo{Name: last.ServiceName}

	switch last.Action {
	case "dashboard":
		fmt.Printf("%s Repeating: dashboard for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		dashboardAction(ctx, config, selectedCluster, selectedService, taskDef)
		return nil
	case "logs":
		fmt.Printf("%s Repeating: stream logs for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		return streamServiceLogs(ctx, config, last.ClusterName, last.ServiceName, taskDef)
	case "task-logs":
		fmt.Printf("%s Repeating: stream task logs for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		return streamTaskLogsRepeat(ctx, config, last.ClusterName, last.ServiceName, taskDef)
	case "watch-deployments":
		fmt.Printf("%s Repeating: watch deployments for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		watchDeploymentsAction(ctx, config, selectedCluster, selectedService, taskDef)
		return nil
	case "connect":
		fmt.Printf("%s Repeating: connect to container for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		return connectToContainer(ctx, config, last.ClusterName, last.ServiceName, taskDef)
	case "check":
		fmt.Printf("%s Repeating: run checks for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		checkAction(ctx, config, selectedCluster, selectedService, taskDef)
		return nil
	case "force-update":
		fmt.Printf("%s Repeating: force update for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		return forceUpdateService(ctx, config, last.ClusterName, last.ServiceName)
	case "capacity":
		// Capacity requires user input; provide info and exit
		fmt.Printf("%s Stored action 'capacity' requires input; please select it manually.\n", qc.Colorize("Note:", qc.ColorYellow))
		return nil
	case "image":
		fmt.Printf("%s Stored action 'image' requires input; please select it manually.\n", qc.Colorize("Note:", qc.ColorYellow))
		return nil
	case "service-config":
		fmt.Printf("%s Repeating: service configuration for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		showServiceConfiguration(ctx, config, last.ClusterName, last.ServiceName)
		return nil
	case "task-defs":
		fmt.Printf("%s Repeating: task definition history for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		return showTaskDefinitionHistory(ctx, config, *tdArn)
	case "healthchecks":
		fmt.Printf("%s Repeating: health checks for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		// Reuse current taskDef for repeat
		showHealthChecks(ctx, config, last.ClusterName, selectedService, taskDef)
		return nil
	case "enable-exec":
		fmt.Printf("%s Repeating: enable exec for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		enableExecAction(ctx, config, selectedCluster, selectedService, taskDef)
		return nil
	case "security-groups":
		fmt.Printf("%s Repeating: security groups for %s/%s\n", qc.Colorize("Info:", qc.ColorCyan), qc.ColorizeBold(last.ClusterName, qc.ColorGreen), qc.ColorizeBold(last.ServiceName, qc.ColorGreen))
		showSecurityGroups(ctx, config, last.ClusterName, last.ServiceName)
		return nil
	default:
		return fmt.Errorf("unknown stored action: %s", last.Action)
	}
}

// convertJSONToCamelCase converts capitalized JSON property names to camelCase
func convertJSONToCamelCase(jsonData []byte) ([]byte, error) {
	var data interface{}
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, err
	}

	converted := convertToCamelCase(data)
	return json.MarshalIndent(converted, "", "  ")
}

// convertToCamelCase recursively converts map keys from PascalCase to camelCase
func convertToCamelCase(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, value := range v {
			camelKey := toCamelCase(key)
			result[camelKey] = convertToCamelCase(value)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = convertToCamelCase(item)
		}
		return result
	default:
		return v
	}
}

// toCamelCase converts PascalCase to camelCase
func toCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}

	// Handle special cases that should remain as-is
	specialCases := map[string]string{
		"ARN":   "arn",
		"ARNs":  "arns",
		"CPU":   "cpu",
		"CPUs":  "cpus",
		"DNS":   "dns",
		"IP":    "ip",
		"IPs":   "ips",
		"ID":    "id",
		"IDs":   "ids",
		"URL":   "url",
		"URLs":  "urls",
		"VPC":   "vpc",
		"VPCs":  "vpcs",
		"ALB":   "alb",
		"ALBs":  "albs",
		"EC2":   "ec2",
		"ECS":   "ecs",
		"SSM":   "ssm",
		"KMS":   "kms",
		"IAM":   "iam",
		"API":   "api",
		"APIs":  "apis",
		"JSON":  "json",
		"XML":   "xml",
		"HTTP":  "http",
		"HTTPS": "https",
		"TCP":   "tcp",
		"UDP":   "udp",
		"SSL":   "ssl",
		"TLS":   "tls",
		"UUID":  "uuid",
		"UUIDs": "uuids",
	}

	if special, exists := specialCases[s]; exists {
		return special
	}

	// Convert PascalCase to camelCase
	runes := []rune(s)
	if len(runes) > 0 {
		runes[0] = runes[0] + 32 // Convert first character to lowercase
	}
	return string(runes)
}
