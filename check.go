package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// checkAction handles the configuration check action
func checkAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("Running configuration checks for service: %s\n", colorBold(selectedService.Name, ColorCyan))

	err := runConfigurationChecks(ctx, config, selectedCluster.Name, selectedService, taskDef)
	if err != nil {
		log.Fatal(err)
	}
}

// runConfigurationChecks runs comprehensive configuration checks for the ECS service
func runConfigurationChecks(ctx context.Context, config *Config, clusterName string, service *ServiceInfo, taskDef *types.TaskDefinition) error {
	fmt.Printf("\n%s\n", color("=== ECS Configuration Health Check ===", ColorBlue))
	fmt.Println(strings.Repeat("=", 50))

	var issues []string
	var warnings []string

	// Check 0: Determine if there are running tasks
	fmt.Printf("🔍 Checking task status...\n")
	hasRunningTasks, runningTaskCount := checkRunningTasks(ctx, config, clusterName, service.Name)
	if hasRunningTasks {
		fmt.Printf("  ✅ %d running task(s) found\n", runningTaskCount)
	} else {
		fmt.Printf("  ⚠️  %s\n", color("No running tasks found", ColorYellow))
		warnings = append(warnings, "No running tasks - some checks may be limited")
	}

	// Check 1: Desired count validation
	fmt.Printf("🔍 Checking desired count...\n")
	if service.DesiredCount == 0 {
		issues = append(issues, "Desired count is 0 - no containers will be built")
		fmt.Printf("  ❌ %s\n", color("Desired count is 0", ColorRed))
	} else {
		fmt.Printf("  ✅ Desired count: %d\n", service.DesiredCount)
	}

	// Check 2: Execution and task role validation
	fmt.Printf("🔍 Checking IAM roles...\n")
	roleIssues, roleWarnings := checkIAMRoles(ctx, config, taskDef, hasRunningTasks)
	issues = append(issues, roleIssues...)
	warnings = append(warnings, roleWarnings...)

	// Check 3: Security group container port support
	fmt.Printf("🔍 Checking security group configuration...\n")
	sgIssues, sgWarnings := checkSecurityGroups(ctx, config, clusterName, service.Name, taskDef, hasRunningTasks)
	issues = append(issues, sgIssues...)
	warnings = append(warnings, sgWarnings...)

	// Check 4: Deployment failures
	fmt.Printf("🔍 Checking deployment status...\n")
	deploymentIssues := checkDeploymentStatus(ctx, config, clusterName, service.Name)
	issues = append(issues, deploymentIssues...)

	// Check 5: Container image pull permissions
	fmt.Printf("🔍 Checking container image access...\n")
	imageIssues := checkContainerImageAccess(taskDef)
	issues = append(issues, imageIssues...)

	// Check 6: SSM parameter and KMS permissions for secrets
	fmt.Printf("🔍 Checking secrets configuration...\n")
	secretsIssues, secretsWarnings := checkSecretsConfiguration(taskDef)
	issues = append(issues, secretsIssues...)
	warnings = append(warnings, secretsWarnings...)

	// Check 7: ALB configuration
	fmt.Printf("🔍 Checking load balancer configuration...\n")
	albIssues, albWarnings := checkALBConfiguration(ctx, config, clusterName, service, taskDef)
	issues = append(issues, albIssues...)
	warnings = append(warnings, albWarnings...)

	// Check 8: Additional common issues
	fmt.Printf("🔍 Checking additional configuration...\n")
	additionalIssues, additionalWarnings := checkAdditionalConfiguration(taskDef)
	issues = append(issues, additionalIssues...)
	warnings = append(warnings, additionalWarnings...)

	// Display summary
	fmt.Printf("\n%s\n", color("=== Check Summary ===", ColorBlue))
	fmt.Println(strings.Repeat("=", 30))

	if len(issues) == 0 && len(warnings) == 0 {
		fmt.Printf("🎉 %s\n", color("All checks passed! No issues found.", ColorGreen))
	} else {
		if len(issues) > 0 {
			fmt.Printf("❌ %s (%d issues found):\n", color("Critical Issues", ColorRed), len(issues))
			for i, issue := range issues {
				fmt.Printf("  %d. %s\n", i+1, issue)
			}
		}

		if len(warnings) > 0 {
			// Aggregate duplicate warnings
			warningCounts := make(map[string]int)
			warningOrder := make([]string, 0)
			for _, w := range warnings {
				if _, ok := warningCounts[w]; !ok {
					warningOrder = append(warningOrder, w)
				}
				warningCounts[w]++
			}
			totalUnique := len(warningCounts)
			fmt.Printf("⚠️  %s (%d warnings, %d unique):\n", color("Warnings", ColorYellow), len(warnings), totalUnique)
			idx := 1
			for _, w := range warningOrder {
				count := warningCounts[w]
				if count > 1 {
					fmt.Printf("  %d. %s %s\n", idx, w, color(fmt.Sprintf("(quantity: %d)", count), ColorCyan))
				} else {
					fmt.Printf("  %d. %s\n", idx, w)
				}
				idx++
			}
		}
	}

	return nil
}

// checkRunningTasks checks if there are any running tasks for the service
func checkRunningTasks(ctx context.Context, config *Config, clusterName, serviceName string) (bool, int) {
	// Get running tasks for the service
	tasks, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       &clusterName,
		ServiceName:   &serviceName,
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil {
		return false, 0
	}

	return len(tasks.TaskArns) > 0, len(tasks.TaskArns)
}

// checkIAMRoles validates execution and task roles
func checkIAMRoles(ctx context.Context, config *Config, taskDef *types.TaskDefinition, hasRunningTasks bool) ([]string, []string) {
	var issues []string
	var warnings []string

	// Check execution role
	if taskDef.ExecutionRoleArn == nil {
		issues = append(issues, "Execution role is missing")
		fmt.Printf("  ❌ %s\n", color("Execution role is missing", ColorRed))
	} else {
		fmt.Printf("  ✅ Execution role: %s\n", sanitizeARN(*taskDef.ExecutionRoleArn, config.PrivateMode))

		// Check if execution role exists and has required policies
		roleName := extractRoleName(*taskDef.ExecutionRoleArn)
		execIssues, execWarnings := validateExecutionRole(ctx, config, roleName, hasRunningTasks)
		issues = append(issues, execIssues...)
		warnings = append(warnings, execWarnings...)
	}

	// Check task role
	if taskDef.TaskRoleArn == nil {
		warnings = append(warnings, "Task role is missing - containers will run with execution role permissions")
		fmt.Printf("  ⚠️  %s\n", color("Task role is missing", ColorYellow))
	} else {
		fmt.Printf("  ✅ Task role: %s\n", sanitizeARN(*taskDef.TaskRoleArn, config.PrivateMode))

		// Check if task role exists
		roleName := extractRoleName(*taskDef.TaskRoleArn)
		taskIssues, taskWarnings := validateTaskRole(ctx, config, roleName, hasRunningTasks)
		issues = append(issues, taskIssues...)
		warnings = append(warnings, taskWarnings...)
	}

	return issues, warnings
}

// extractRoleName extracts the role name from an ARN
func extractRoleName(roleArn string) string {
	parts := strings.Split(roleArn, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return roleArn
}

// validateExecutionRole checks if execution role exists and has required policies
func validateExecutionRole(ctx context.Context, config *Config, roleName string, hasRunningTasks bool) ([]string, []string) {
	var issues []string
	var warnings []string

	// Check if role exists
	_, err := config.IAMClient.GetRole(ctx, &iam.GetRoleInput{
		RoleName: &roleName,
	})
	if err != nil {
		issues = append(issues, fmt.Sprintf("Execution role '%s' does not exist or is not accessible", roleName))
		fmt.Printf("    ❌ %s\n", color("Role does not exist", ColorRed))
		return issues, warnings
	}

	// Check attached policies
	attachedPolicies, err := config.IAMClient.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: &roleName,
	})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("Could not list policies for execution role '%s'", roleName))
		fmt.Printf("    ⚠️  %s\n", color("Could not list policies", ColorYellow))
		return issues, warnings
	}

	// Check for required execution role policies
	requiredPolicies := []string{
		"arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy",
	}

	hasRequiredPolicy := false
	for _, policy := range attachedPolicies.AttachedPolicies {
		if slices.Contains(requiredPolicies, *policy.PolicyArn) {
			hasRequiredPolicy = true
			break
		}
	}

	if !hasRequiredPolicy {
		issues = append(issues, "Execution role missing required ECS task execution policy")
		fmt.Printf("    ❌ %s\n", color("Missing ECS task execution policy", ColorRed))
	} else {
		fmt.Printf("    ✅ Has ECS task execution policy\n")
	}

	// Additional validation that requires running tasks
	if !hasRunningTasks {
		warnings = append(warnings, "Unable to fully validate execution role - need running tasks to check actual permissions")
		fmt.Printf("    ⚠️  %s\n", color("Need running tasks for full validation", ColorYellow))
	}

	return issues, warnings
}

// validateTaskRole checks if task role exists
func validateTaskRole(ctx context.Context, config *Config, roleName string, hasRunningTasks bool) ([]string, []string) {
	var issues []string
	var warnings []string

	// Check if role exists
	_, err := config.IAMClient.GetRole(ctx, &iam.GetRoleInput{
		RoleName: &roleName,
	})
	if err != nil {
		issues = append(issues, fmt.Sprintf("Task role '%s' does not exist or is not accessible", roleName))
		fmt.Printf("    ❌ %s\n", color("Role does not exist", ColorRed))
		return issues, warnings
	}

	fmt.Printf("    ✅ Task role exists\n")

	// Additional validation that requires running tasks
	if !hasRunningTasks {
		warnings = append(warnings, "Unable to fully validate task role - need running tasks to check actual permissions")
		fmt.Printf("    ⚠️  %s\n", color("Need running tasks for full validation", ColorYellow))
	}

	return issues, warnings
}

// checkSecurityGroups validates security group configuration
func checkSecurityGroups(ctx context.Context, config *Config, clusterName, serviceName string, taskDef *types.TaskDefinition, hasRunningTasks bool) ([]string, []string) {
	var issues []string
	var warnings []string

	// Get container ports from task definition
	var containerPorts []int32
	for _, container := range taskDef.ContainerDefinitions {
		for _, portMapping := range container.PortMappings {
			if portMapping.ContainerPort != nil {
				containerPorts = append(containerPorts, *portMapping.ContainerPort)
			}
		}
	}

	if len(containerPorts) == 0 {
		warnings = append(warnings, "No container ports defined in task definition")
		fmt.Printf("  ⚠️  %s\n", color("No container ports defined", ColorYellow))
		return issues, warnings
	}

	fmt.Printf("  ✅ Container ports: %v\n", containerPorts)

	// Check if we're running on Fargate or EC2
	if taskDef.RequiresCompatibilities != nil {
		if slices.Contains(taskDef.RequiresCompatibilities, types.CompatibilityFargate) {
			fmt.Printf("  ℹ️  Fargate launch type - security groups managed by service network configuration\n")
			warnings = append(warnings, "Fargate security group validation requires service network configuration - check manually")
			fmt.Printf("  ⚠️  %s\n", color("Manual security group check required for Fargate", ColorYellow))
			return issues, warnings
		}
	}

	// For EC2 launch type, check the instances
	if !hasRunningTasks {
		warnings = append(warnings, "Unable to check EC2 instance security groups - need running tasks")
		fmt.Printf("  ⚠️  %s\n", color("Need running tasks to check EC2 security groups", ColorYellow))
		return issues, warnings
	}

	// Get running tasks to find EC2 instances
	tasks, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       &clusterName,
		ServiceName:   &serviceName,
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil || len(tasks.TaskArns) == 0 {
		warnings = append(warnings, "Could not get running tasks to check EC2 instances")
		fmt.Printf("  ⚠️  %s\n", color("Could not get running tasks", ColorYellow))
		return issues, warnings
	}

	// Get detailed task information to find EC2 instances
	taskDetails, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: &clusterName,
		Tasks:   tasks.TaskArns,
	})
	if err != nil || len(taskDetails.Tasks) == 0 {
		warnings = append(warnings, "Could not get task details to check EC2 instances")
		fmt.Printf("  ⚠️  %s\n", color("Could not get task details", ColorYellow))
		return issues, warnings
	}

	// Get the first task's EC2 instance (assuming all instances have same security groups)
	var ec2InstanceID string
	for _, task := range taskDetails.Tasks {
		if task.ContainerInstanceArn != nil {
			// Get container instance details
			containerInstances, err := config.ECSClient.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
				Cluster:            &clusterName,
				ContainerInstances: []string{*task.ContainerInstanceArn},
			})
			if err == nil && len(containerInstances.ContainerInstances) > 0 {
				instance := containerInstances.ContainerInstances[0]
				if instance.Ec2InstanceId != nil {
					ec2InstanceID = *instance.Ec2InstanceId
					break
				}
			}
		}
	}

	if ec2InstanceID == "" {
		warnings = append(warnings, "Could not find EC2 instance for running tasks")
		fmt.Printf("  ⚠️  %s\n", color("Could not find EC2 instance", ColorYellow))
		return issues, warnings
	}

	fmt.Printf("  ✅ Found EC2 instance: %s\n", ec2InstanceID)

	// Check EC2 instance and its security groups
	instanceIssues, instanceWarnings := checkEC2Instance(ctx, config, ec2InstanceID, containerPorts)
	issues = append(issues, instanceIssues...)
	warnings = append(warnings, instanceWarnings...)

	return issues, warnings
}

// checkEC2Instance checks EC2 instance IAM role and security groups
func checkEC2Instance(ctx context.Context, config *Config, instanceID string, containerPorts []int32) ([]string, []string) {
	var issues []string
	var warnings []string

	// Get EC2 instance details
	instances, err := config.EC2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		issues = append(issues, fmt.Sprintf("Could not describe EC2 instance %s: %v", instanceID, err))
		fmt.Printf("    ❌ %s\n", color("Could not describe EC2 instance", ColorRed))
		return issues, warnings
	}

	if len(instances.Reservations) == 0 || len(instances.Reservations[0].Instances) == 0 {
		issues = append(issues, fmt.Sprintf("EC2 instance %s not found", instanceID))
		fmt.Printf("    ❌ %s\n", color("EC2 instance not found", ColorRed))
		return issues, warnings
	}

	instance := instances.Reservations[0].Instances[0]

	// Check IAM instance profile
	if instance.IamInstanceProfile == nil {
		issues = append(issues, "EC2 instance has no IAM instance profile attached")
		fmt.Printf("    ❌ %s\n", color("No IAM instance profile", ColorRed))
	} else {
		profileArn := *instance.IamInstanceProfile.Arn
		fmt.Printf("    ✅ IAM instance profile: %s\n", profileArn)

		// Extract profile name and check if it exists
		profileName := extractInstanceProfileName(profileArn)
		profileIssues, profileWarnings := validateInstanceProfile(ctx, config, profileName)
		issues = append(issues, profileIssues...)
		warnings = append(warnings, profileWarnings...)
	}

	// Check security groups
	if len(instance.SecurityGroups) == 0 {
		issues = append(issues, "EC2 instance has no security groups")
		fmt.Printf("    ❌ %s\n", color("No security groups", ColorRed))
		return issues, warnings
	}

	fmt.Printf("    ✅ Security groups: %d found\n", len(instance.SecurityGroups))

	// Get security group details
	var securityGroupIds []string
	for _, sg := range instance.SecurityGroups {
		securityGroupIds = append(securityGroupIds, *sg.GroupId)
	}

	// Check security group rules for container ports
	sgIssues, sgWarnings := validateSecurityGroupRules(ctx, config, securityGroupIds, containerPorts)
	issues = append(issues, sgIssues...)
	warnings = append(warnings, sgWarnings...)

	return issues, warnings
}

// extractInstanceProfileName extracts the instance profile name from an ARN
func extractInstanceProfileName(profileArn string) string {
	parts := strings.Split(profileArn, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return profileArn
}

// validateInstanceProfile checks if the instance profile exists and has a role
func validateInstanceProfile(ctx context.Context, config *Config, profileName string) ([]string, []string) {
	var issues []string
	var warnings []string

	// Get instance profile details
	profiles, err := config.IAMClient.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: &profileName,
	})
	if err != nil {
		issues = append(issues, fmt.Sprintf("Instance profile '%s' does not exist or is not accessible", profileName))
		fmt.Printf("      ❌ %s\n", color("Instance profile does not exist", ColorRed))
		return issues, warnings
	}

	// Check if profile has a role
	if len(profiles.InstanceProfile.Roles) == 0 {
		issues = append(issues, fmt.Sprintf("Instance profile '%s' has no IAM role attached", profileName))
		fmt.Printf("      ❌ %s\n", color("No IAM role attached to instance profile", ColorRed))
	} else {
		roleName := *profiles.InstanceProfile.Roles[0].RoleName
		fmt.Printf("      ✅ IAM role attached: %s\n", roleName)

		// Check if the role exists
		_, err := config.IAMClient.GetRole(ctx, &iam.GetRoleInput{
			RoleName: &roleName,
		})
		if err != nil {
			issues = append(issues, fmt.Sprintf("IAM role '%s' attached to instance profile does not exist", roleName))
			fmt.Printf("        ❌ %s\n", color("IAM role does not exist", ColorRed))
		} else {
			fmt.Printf("        ✅ IAM role exists\n")
		}
	}

	return issues, warnings
}

// validateSecurityGroupRules checks if security groups allow inbound traffic on container ports
func validateSecurityGroupRules(ctx context.Context, config *Config, securityGroupIds []string, containerPorts []int32) ([]string, []string) {
	var issues []string
	var warnings []string

	// Get security group details
	securityGroups, err := config.EC2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: securityGroupIds,
	})
	if err != nil {
		issues = append(issues, fmt.Sprintf("Could not describe security groups: %v", err))
		fmt.Printf("    ❌ %s\n", color("Could not describe security groups", ColorRed))
		return issues, warnings
	}

	// Check each security group for container port access
	for _, sg := range securityGroups.SecurityGroups {
		fmt.Printf("    🔍 Checking security group: %s (%s)\n", *sg.GroupName, *sg.GroupId)

		// Check inbound rules for container ports
		hasPortAccess := false
		for _, port := range containerPorts {
			portAllowed := false
			for _, rule := range sg.IpPermissions {
				// Check if this rule allows the port
				if rule.FromPort != nil && rule.ToPort != nil {
					if port >= *rule.FromPort && port <= *rule.ToPort {
						// Allow any CIDR (including restricted ones) or security group references
						if len(rule.UserIdGroupPairs) > 0 || len(rule.IpRanges) > 0 || len(rule.Ipv6Ranges) > 0 {
							portAllowed = true
							break
						}
					}
				}
			}

			if portAllowed {
				fmt.Printf("      ✅ Port %d has at least one inbound rule\n", port)
				hasPortAccess = true
			} else {
				issues = append(issues, fmt.Sprintf("Security group %s may not allow inbound access to port %d", *sg.GroupId, port))
				fmt.Printf("      ❌ %s\n", color(fmt.Sprintf("No inbound rule found for port %d", port), ColorRed))
			}
		}

		if !hasPortAccess {
			warnings = append(warnings, fmt.Sprintf("Security group %s may not allow inbound access to container ports", *sg.GroupId))
		}
	}

	return issues, warnings
}

// checkDeploymentStatus checks for deployment failures
func checkDeploymentStatus(ctx context.Context, config *Config, clusterName, serviceName string) []string {
	var issues []string

	// Get service details to check deployment status
	services, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: []string{serviceName},
	})
	if err != nil {
		warnings := []string{fmt.Sprintf("Could not check deployment status: %v", err)}
		fmt.Printf("  ⚠️  %s\n", color("Could not check deployment status", ColorYellow))
		return warnings
	}

	if len(services.Services) == 0 {
		issues = append(issues, "Service not found")
		fmt.Printf("  ❌ %s\n", color("Service not found", ColorRed))
		return issues
	}

	service := services.Services[0]

	// Check deployments
	for _, deployment := range service.Deployments {
		if deployment.Status != nil && *deployment.Status == "FAILED" {
			issues = append(issues, fmt.Sprintf("Deployment failed: %s", *deployment.Id))
			fmt.Printf("  ❌ %s\n", color("Deployment failed", ColorRed))
		} else if deployment.Status != nil && *deployment.Status == "ACTIVE" {
			fmt.Printf("  ✅ Active deployment: %s\n", *deployment.Id)
		}
	}

	// Check for recent deployment failures with specific error patterns
	if service.Events != nil {
		foundSpecificError := false
		for _, event := range service.Events {
			message := strings.ToLower(*event.Message)

			// Check for specific error patterns
			if strings.Contains(message, "cannotpullcontainererror") {
				issues = append(issues, fmt.Sprintf("Container pull error: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Container pull error detected", ColorRed))
				foundSpecificError = true
				break
			} else if strings.Contains(message, "manifest for") && strings.Contains(message, "not found") {
				issues = append(issues, fmt.Sprintf("Image manifest not found: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Image manifest not found", ColorRed))
				foundSpecificError = true
				break
			} else if strings.Contains(message, "manifest unknown") {
				issues = append(issues, fmt.Sprintf("Unknown image manifest: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Unknown image manifest", ColorRed))
				foundSpecificError = true
				break
			} else if strings.Contains(message, "requested image not found") {
				issues = append(issues, fmt.Sprintf("Image not found: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Image not found", ColorRed))
				foundSpecificError = true
				break
			} else if strings.Contains(message, "access denied") {
				issues = append(issues, fmt.Sprintf("Access denied error: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Access denied error", ColorRed))
				foundSpecificError = true
				break
			} else if strings.Contains(message, "unauthorized") {
				issues = append(issues, fmt.Sprintf("Unauthorized error: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Unauthorized error", ColorRed))
				foundSpecificError = true
				break
			} else if strings.Contains(message, "failed") {
				issues = append(issues, fmt.Sprintf("Deployment failure: %s", *event.Message))
				fmt.Printf("  ❌ %s\n", color("Deployment failure detected", ColorRed))
				foundSpecificError = true
				break
			}
		}

		// If no specific errors found, check if there are any recent events
		if !foundSpecificError && len(service.Events) > 0 {
			// Check the most recent event for any issues
			recentEvent := service.Events[0]
			if recentEvent.Message != nil {
				fmt.Printf("  ℹ️  Most recent event: %s\n", *recentEvent.Message)
			}
		}
	}

	// Check task-level failures (tasks that failed to start)
	taskIssues := checkTaskFailures(ctx, config, clusterName, serviceName)
	issues = append(issues, taskIssues...)

	return issues
}

// checkTaskFailures checks for task-level failures that might not show up in service events
func checkTaskFailures(ctx context.Context, config *Config, clusterName, serviceName string) []string {
	var issues []string

	// Get all tasks for the service (including stopped ones)
	tasks, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:     &clusterName,
		ServiceName: &serviceName,
	})
	if err != nil {
		// If we can't list tasks, it's not critical - just return
		return issues
	}

	if len(tasks.TaskArns) == 0 {
		return issues
	}

	// Get detailed task information
	taskDetails, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: &clusterName,
		Tasks:   tasks.TaskArns,
	})
	if err != nil {
		return issues
	}

	// Check each task for failures
	for _, task := range taskDetails.Tasks {
		// Check task status
		if task.LastStatus != nil {
			status := *task.LastStatus
			if status == "STOPPED" {
				// Check stop reason
				if task.StoppedReason != nil {
					reason := strings.ToLower(*task.StoppedReason)

					// Check for container pull errors
					if strings.Contains(reason, "cannotpullcontainererror") {
						issues = append(issues, fmt.Sprintf("Task stopped due to container pull error: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - container pull error", ColorRed))
					} else if strings.Contains(reason, "manifest for") && strings.Contains(reason, "not found") {
						issues = append(issues, fmt.Sprintf("Task stopped - image manifest not found: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - image manifest not found", ColorRed))
					} else if strings.Contains(reason, "manifest unknown") {
						issues = append(issues, fmt.Sprintf("Task stopped - unknown image manifest: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - unknown image manifest", ColorRed))
					} else if strings.Contains(reason, "requested image not found") {
						issues = append(issues, fmt.Sprintf("Task stopped - image not found: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - image not found", ColorRed))
					} else if strings.Contains(reason, "access denied") {
						issues = append(issues, fmt.Sprintf("Task stopped - access denied: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - access denied", ColorRed))
					} else if strings.Contains(reason, "unauthorized") {
						issues = append(issues, fmt.Sprintf("Task stopped - unauthorized: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - unauthorized", ColorRed))
					} else if strings.Contains(reason, "failed") {
						issues = append(issues, fmt.Sprintf("Task stopped - failure: %s", *task.StoppedReason))
						fmt.Printf("  ❌ %s\n", color("Task stopped - failure", ColorRed))
					}
				}
			}
		}

		// Check container statuses within the task
		for _, container := range task.Containers {
			if container.Reason != nil {
				reason := strings.ToLower(*container.Reason)

				// Check for container pull errors
				if strings.Contains(reason, "cannotpullcontainererror") {
					issues = append(issues, fmt.Sprintf("Container failed to start - pull error: %s", *container.Reason))
					fmt.Printf("  ❌ %s\n", color("Container failed - pull error", ColorRed))
				} else if strings.Contains(reason, "manifest for") && strings.Contains(reason, "not found") {
					issues = append(issues, fmt.Sprintf("Container failed - image manifest not found: %s", *container.Reason))
					fmt.Printf("  ❌ %s\n", color("Container failed - image manifest not found", ColorRed))
				} else if strings.Contains(reason, "manifest unknown") {
					issues = append(issues, fmt.Sprintf("Container failed - unknown image manifest: %s", *container.Reason))
					fmt.Printf("  ❌ %s\n", color("Container failed - unknown image manifest", ColorRed))
				} else if strings.Contains(reason, "requested image not found") {
					issues = append(issues, fmt.Sprintf("Container failed - image not found: %s", *container.Reason))
					fmt.Printf("  ❌ %s\n", color("Container failed - image not found", ColorRed))
				} else if strings.Contains(reason, "access denied") {
					issues = append(issues, fmt.Sprintf("Container failed - access denied: %s", *container.Reason))
					fmt.Printf("  ❌ %s\n", color("Container failed - access denied", ColorRed))
				} else if strings.Contains(reason, "unauthorized") {
					issues = append(issues, fmt.Sprintf("Container failed - unauthorized: %s", *container.Reason))
					fmt.Printf("  ❌ %s\n", color("Container failed - unauthorized", ColorRed))
				}
			}
		}
	}

	return issues
}

// checkContainerImageAccess validates container image pull permissions
func checkContainerImageAccess(taskDef *types.TaskDefinition) []string {
	var issues []string

	for _, container := range taskDef.ContainerDefinitions {
		if container.Image != nil {
			image := *container.Image
			fmt.Printf("  ✅ Container image: %s\n", image)

			// Check if it's an ECR image
			if strings.Contains(image, "amazonaws.com") {
				// ECR images require proper IAM permissions
				fmt.Printf("    ℹ️  ECR image - ensure execution role has ECR permissions\n")

				// Check if it's using a specific SHA (which can cause issues)
				if strings.Contains(image, "@sha256:") {
					issues = append(issues, "ECR image using SHA digest - ensure the specific image exists in the repository")
					fmt.Printf("    ⚠️  %s\n", color("Using SHA digest - verify image exists", ColorYellow))
				}

				// Check if it's using a tag
				if strings.Contains(image, ":") && !strings.Contains(image, "@sha256:") {
					fmt.Printf("    ℹ️  Using tag - ensure tag exists in ECR repository\n")
				}
			} else if strings.Contains(image, "docker.io") || strings.Contains(image, "gcr.io") || strings.Contains(image, "quay.io") {
				// Public images should be accessible
				fmt.Printf("    ℹ️  Public image - should be accessible\n")
			} else {
				// Private registry - check if credentials are configured
				fmt.Printf("    ⚠️  Private registry image - ensure credentials are configured\n")
			}
		}
	}

	return issues
}

// checkSecretsConfiguration validates SSM parameter and KMS permissions for secrets
func checkSecretsConfiguration(taskDef *types.TaskDefinition) ([]string, []string) {
	var issues []string
	var warnings []string

	hasSecrets := false
	for _, container := range taskDef.ContainerDefinitions {
		if len(container.Secrets) > 0 {
			hasSecrets = true
			fmt.Printf("  ✅ Container has secrets configured\n")

			for _, secret := range container.Secrets {
				if secret.ValueFrom != nil {
					secretArn := *secret.ValueFrom
					fmt.Printf("    ℹ️  Secret: %s\n", secretArn)

					// Check if it's an SSM parameter
					if strings.Contains(secretArn, "parameter") {
						warnings = append(warnings, "SSM parameter secrets require SSM and KMS permissions")
						fmt.Printf("      ⚠️  %s\n", color("Requires SSM and KMS permissions", ColorYellow))
					}
				}
			}
		}
	}

	if !hasSecrets {
		fmt.Printf("  ℹ️  No secrets configured\n")
	}

	return issues, warnings
}

// checkAdditionalConfiguration checks for additional common ECS issues
func checkAdditionalConfiguration(taskDef *types.TaskDefinition) ([]string, []string) {
	var issues []string
	var warnings []string

	// Check CPU and memory configuration
	if taskDef.Cpu != nil && taskDef.Memory != nil {
		cpu := *taskDef.Cpu
		memory := *taskDef.Memory

		// Parse CPU and memory values (they are strings in ECS)
		cpuInt, err := strconv.Atoi(cpu)
		if err != nil {
			warnings = append(warnings, "Invalid CPU configuration")
			fmt.Printf("  ⚠️  %s\n", color("Invalid CPU configuration", ColorYellow))
		} else {
			memoryInt, err := strconv.Atoi(memory)
			if err != nil {
				warnings = append(warnings, "Invalid memory configuration")
				fmt.Printf("  ⚠️  %s\n", color("Invalid memory configuration", ColorYellow))
			} else {
				// Check for reasonable CPU/memory ratios
				if memoryInt > 0 && cpuInt > 0 {
					ratio := float64(memoryInt) / float64(cpuInt)
					if ratio < 1.0 {
						warnings = append(warnings, "Low memory-to-CPU ratio - consider increasing memory")
						fmt.Printf("  ⚠️  %s\n", color("Low memory-to-CPU ratio", ColorYellow))
					} else {
						fmt.Printf("  ✅ CPU: %s, Memory: %s MB\n", cpu, memory)
					}
				}
			}
		}
	}

	// Check for health check configuration
	hasHealthCheck := false
	for _, container := range taskDef.ContainerDefinitions {
		if container.HealthCheck != nil {
			hasHealthCheck = true
			break
		}
	}

	if !hasHealthCheck {
		warnings = append(warnings, "No health check configured - consider adding health checks")
		fmt.Printf("  ⚠️  %s\n", color("No health check configured", ColorYellow))
	} else {
		fmt.Printf("  ✅ Health check configured\n")
	}

	// Check for log configuration
	hasLogConfig := false
	for _, container := range taskDef.ContainerDefinitions {
		if container.LogConfiguration != nil {
			hasLogConfig = true
			break
		}
	}

	if !hasLogConfig {
		warnings = append(warnings, "No log configuration - consider adding CloudWatch logging")
		fmt.Printf("  ⚠️  %s\n", color("No log configuration", ColorYellow))
	} else {
		fmt.Printf("  ✅ Log configuration present\n")
	}

	return issues, warnings
}

// checkALBConfiguration attempts to discover the ALB and validate SGs and listeners
func checkALBConfiguration(ctx context.Context, config *Config, clusterName string, service *ServiceInfo, taskDef *types.TaskDefinition) ([]string, []string) {
	var issues []string
	var warnings []string

	// Describe service to get load balancer/target group
	svcOut, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: []string{service.Name},
	})
	if err != nil || len(svcOut.Services) == 0 {
		warnings = append(warnings, "Could not describe service to determine load balancer configuration")
		fmt.Printf("  ⚠️  %s\n", color("Unable to determine load balancer configuration", ColorYellow))
		return issues, warnings
	}

	svc := svcOut.Services[0]
	// Detect if recent events indicate health check failures
	healthFailed := false
	if svc.Events != nil {
		for _, ev := range svc.Events {
			if ev.Message != nil {
				m := strings.ToLower(*ev.Message)
				if strings.Contains(m, "health check") || strings.Contains(m, "healthchecks failed") || strings.Contains(m, "unhealthy") {
					healthFailed = true
					break
				}
			}
		}
	}
	if len(svc.LoadBalancers) == 0 {
		warnings = append(warnings, "Service has no load balancer configuration")
		fmt.Printf("  ⚠️  %s\n", color("No load balancer configured for service", ColorYellow))
		return issues, warnings
	}

	// Assume first LB config
	lbCfg := svc.LoadBalancers[0]
	if lbCfg.TargetGroupArn == nil {
		warnings = append(warnings, "No target group associated with service load balancer config")
		fmt.Printf("  ⚠️  %s\n", color("No target group associated", ColorYellow))
		return issues, warnings
	}

	tgArn := *lbCfg.TargetGroupArn
	tgOut, err := config.ELBv2Client.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}})
	if err != nil || len(tgOut.TargetGroups) == 0 {
		issues = append(issues, "Target group not found or inaccessible")
		fmt.Printf("  ❌ %s\n", color("Target group not found", ColorRed))
		return issues, warnings
	}

	tg := tgOut.TargetGroups[0]
	fmt.Printf("  ✅ Target group: %s\n", sanitizeARN(*tg.TargetGroupArn, config.PrivateMode))

	// If deployment failed due to health checks, print detailed health check configuration
	if healthFailed {
		fmt.Printf("  %s Deployment reported health check failures. Inspecting Target Group health check configuration...\n", color("Info:", ColorYellow))
		// Health check configuration details
		if tg.HealthCheckProtocol != "" {
			fmt.Printf("    • HealthCheckProtocol: %s\n", tg.HealthCheckProtocol)
		}
		if tg.HealthCheckPort != nil {
			fmt.Printf("    • HealthCheckPort: %s\n", *tg.HealthCheckPort)
		}
		if tg.HealthCheckPath != nil {
			fmt.Printf("    • HealthCheckPath: %s\n", *tg.HealthCheckPath)
		}
		if tg.Matcher != nil && tg.Matcher.HttpCode != nil {
			fmt.Printf("    • Matcher: %s\n", *tg.Matcher.HttpCode)
		}
		if tg.HealthCheckIntervalSeconds != nil {
			fmt.Printf("    • IntervalSeconds: %d\n", *tg.HealthCheckIntervalSeconds)
		}
		if tg.HealthCheckTimeoutSeconds != nil {
			fmt.Printf("    • TimeoutSeconds: %d\n", *tg.HealthCheckTimeoutSeconds)
		}
		if tg.HealthyThresholdCount != nil && tg.UnhealthyThresholdCount != nil {
			fmt.Printf("    • Thresholds: healthy=%d, unhealthy=%d\n", *tg.HealthyThresholdCount, *tg.UnhealthyThresholdCount)
		}

		// Show target health descriptions
		thOut, err := config.ELBv2Client.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: tg.TargetGroupArn})
		if err == nil {
			for _, desc := range thOut.TargetHealthDescriptions {
				state := ""
				reason := ""
				if desc.TargetHealth != nil {
					if desc.TargetHealth.State != "" {
						state = string(desc.TargetHealth.State)
					}
					if desc.TargetHealth.Reason != "" {
						reason = string(desc.TargetHealth.Reason)
					}
				}
				targetId := ""
				if desc.Target != nil && desc.Target.Id != nil {
					targetId = *desc.Target.Id
				}
				fmt.Printf("    • Target %s: state=%s", targetId, state)
				if reason != "" {
					fmt.Printf(", reason=%s", reason)
				}
				fmt.Printf("\n")
			}
		}
	}

	// Find associated load balancer
	if len(tg.LoadBalancerArns) == 0 {
		warnings = append(warnings, "Target group has no associated load balancer")
		fmt.Printf("  ⚠️  %s\n", color("No associated load balancer", ColorYellow))
		return issues, warnings
	}

	lbArn := tg.LoadBalancerArns[0]
	lbOut, err := config.ELBv2Client.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	if err != nil || len(lbOut.LoadBalancers) == 0 {
		issues = append(issues, "Load balancer not found or inaccessible")
		fmt.Printf("  ❌ %s\n", color("Load balancer not found", ColorRed))
		return issues, warnings
	}

	lb := lbOut.LoadBalancers[0]
	fmt.Printf("  ✅ Load balancer: %s (%s)\n", *lb.LoadBalancerName, sanitizeARN(*lb.LoadBalancerArn, config.PrivateMode))

	// Validate listeners exist
	lsOut, err := config.ELBv2Client.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{LoadBalancerArn: lb.LoadBalancerArn})
	if err != nil || len(lsOut.Listeners) == 0 {
		issues = append(issues, "No listeners configured on ALB")
		fmt.Printf("  ❌ %s\n", color("No ALB listeners configured", ColorRed))
	} else {
		var have80, have443 bool
		for _, l := range lsOut.Listeners {
			if l.Port != nil {
				if *l.Port == 80 {
					have80 = true
				}
				if *l.Port == 443 {
					have443 = true
				}
			}
		}
		if have80 || have443 {
			fmt.Printf("  ✅ Listeners present: ")
			if have80 {
				fmt.Printf("80 ")
			}
			if have443 {
				fmt.Printf("443 ")
			}
			fmt.Printf("\n")
		} else {
			warnings = append(warnings, "No standard web listeners (80/443) detected")
			fmt.Printf("  ⚠️  %s\n", color("No 80/443 listeners detected", ColorYellow))
		}
	}

	// Validate ALB Security Groups inbound on 80/443 and outbound to ECS SGs on container port(s)
	if len(lb.SecurityGroups) == 0 {
		warnings = append(warnings, "ALB has no security groups attached")
		fmt.Printf("  ⚠️  %s\n", color("ALB has no security groups", ColorYellow))
		return issues, warnings
	}

	albSgIds := make([]string, len(lb.SecurityGroups))
	copy(albSgIds, lb.SecurityGroups)

	// Inbound check on 80/443
	albSgIssues, albSgWarnings := validateAlbSecurityGroups(ctx, config, albSgIds)
	issues = append(issues, albSgIssues...)
	warnings = append(warnings, albSgWarnings...)

	// Outbound to ECS SGs on container port - heuristic egress check
	// Here, just confirm ALB SG has outbound rules (many orgs keep default allow-all egress). If restrictive, flag warning.
	egressOK, egressWarn := checkAlbOutboundRules(ctx, config, albSgIds)
	if !egressOK {
		warnings = append(warnings, "ALB security group egress rules may block traffic to ECS tasks")
		fmt.Printf("  ⚠️  %s\n", color("ALB SG egress may be restrictive", ColorYellow))
	}
	if egressWarn != "" {
		warnings = append(warnings, egressWarn)
	}

	// Basic sanity checks between health check port and container ports
	// Gather container ports from task def
	var containerPorts []int32
	for _, c := range taskDef.ContainerDefinitions {
		for _, pm := range c.PortMappings {
			if pm.ContainerPort != nil {
				containerPorts = append(containerPorts, *pm.ContainerPort)
			}
		}
	}
	if len(containerPorts) > 0 && tg.HealthCheckPort != nil {
		hcPort := *tg.HealthCheckPort
		// If not "traffic-port", ensure it matches one of the container ports
		if hcPort != "traffic-port" {
			// parse hcPort to int
			if p, err := strconv.Atoi(hcPort); err == nil {
				match := false
				for _, cp := range containerPorts {
					if int(cp) == p {
						match = true
						break
					}
				}
				if !match {
					warnings = append(warnings, fmt.Sprintf("Target group health check port %d does not match any container port %v", p, containerPorts))
					fmt.Printf("  ⚠️  %s\n", color("Health check port does not match container port(s)", ColorYellow))
				}
			}
		}
	}

	return issues, warnings
}

func validateAlbSecurityGroups(ctx context.Context, config *Config, albSgIds []string) ([]string, []string) {
	var issues []string
	var warnings []string

	sgs, err := config.EC2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: albSgIds})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("Could not describe ALB security groups: %v", err))
		fmt.Printf("  ⚠️  %s\n", color("Could not describe ALB security groups", ColorYellow))
		return issues, warnings
	}

	// Check inbound for 80 or 443
	var hasInbound bool
	for _, sg := range sgs.SecurityGroups {
		for _, p := range sg.IpPermissions {
			if p.FromPort != nil && p.ToPort != nil {
				if (*p.FromPort <= 80 && *p.ToPort >= 80) || (*p.FromPort <= 443 && *p.ToPort >= 443) {
					hasInbound = true
				}
			}
		}
	}
	if !hasInbound {
		warnings = append(warnings, "ALB SG does not expose common web ports (80/443)")
		fmt.Printf("  ⚠️  %s\n", color("ALB SG missing inbound 80/443", ColorYellow))
	} else {
		fmt.Printf("  ✅ ALB SG inbound allows 80/443 (or equivalent range)\n")
	}

	return issues, warnings
}

func checkAlbOutboundRules(ctx context.Context, config *Config, albSgIds []string) (bool, string) {
	sgs, err := config.EC2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: albSgIds})
	if err != nil {
		return false, "Could not read ALB SG egress rules"
	}
	// Heuristic: if any SG has an egress rule, assume OK; else warn
	for _, sg := range sgs.SecurityGroups {
		if len(sg.IpPermissionsEgress) > 0 {
			return true, ""
		}
	}
	return false, "ALB SG has no egress rules configured"
}
