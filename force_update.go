package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	qc "github.com/bevelwork/quick_color"
)

// forceUpdateAction handles the force update action
func forceUpdateAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo) {
	fmt.Printf("Force updating service: %s\n", qc.ColorizeBold(selectedService.Name, qc.ColorCyan))

	// Confirm the force update
	fmt.Printf("%s", qc.Colorize("Force update will restart all tasks. Continue? (y/N): ", qc.ColorYellow))
	confirmInput, err := bufio.NewReader(os.Stdin).ReadString('\n')
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
		fmt.Printf("Force update failed: %v\n", err)
		os.Exit(1)
	}
}

// forceUpdateService forces a new deployment and waits for completion
func forceUpdateService(ctx context.Context, config *Config, clusterName, serviceName string) error {
	// Get the current service state to identify the new deployment
	services, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  &clusterName,
		Services: []string{serviceName},
	})
	if err != nil {
		return fmt.Errorf("failed to describe service before update: %v", err)
	}

	if len(services.Services) == 0 {
		return fmt.Errorf("service %s not found", serviceName)
	}

	// Store the current deployment state to track changes
	var currentDeploymentState *types.Deployment
	if len(services.Services[0].Deployments) > 0 {
		currentDeploymentState = &services.Services[0].Deployments[0]
	}

	// Force a new deployment
	fmt.Printf("Initiating force update for service %s...\n", qc.ColorizeBold(serviceName, qc.ColorCyan))

	_, err = config.ECSClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:            &clusterName,
		Service:            &serviceName,
		ForceNewDeployment: true,
	})
	if err != nil {
		return fmt.Errorf("failed to force update service: %v", err)
	}

	fmt.Printf("Force update initiated successfully!\n")
	fmt.Printf("Waiting for deployment to complete...\n\n")

	// Poll for deployment completion with the new behavior
	return pollDeploymentCompletion(ctx, config, clusterName, serviceName, currentDeploymentState)
}

// pollDeploymentCompletion polls the service until the new deployment succeeds or fails
func pollDeploymentCompletion(ctx context.Context, config *Config, clusterName, serviceName string, previousDeploymentState *types.Deployment) error {
	maxAttempts := 200 // 10 minutes with 3-second intervals
	attempt := 0

	// Create a channel to signal when to stop the throbber
	done := make(chan bool, 1)

	// Start throbber in a separate goroutine
	go func() {
		i := 0
		cycle := 1
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Printf("\r%s Deployment in progress... (cycle %d)", throbberChars[i%len(throbberChars)], cycle)
				i++
				// update cycle number every ~3 seconds (matching poll cadence)
				if i%(1000/200*3) == 0 {
					cycle++
				}
			}
		}
	}()

	defer func() {
		done <- true
		fmt.Printf("\r\033[K") // Clear the throbber line
	}()

	var newDeploymentId string

	for attempt < maxAttempts {
		// Get service status
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

		service := services.Services[0]

		// Find the active deployment (the one we're monitoring)
		var activeDeployment *types.Deployment

		// Debug: Print all deployments for troubleshooting
		if attempt%20 == 0 { // Print every 20 attempts (every minute)
			fmt.Printf("\nDebug: Found %d deployments:\n", len(service.Deployments))
			for i, deployment := range service.Deployments {
				if deployment.Id != nil {
					status := "unknown"
					if deployment.Status != nil {
						status = *deployment.Status
					}
					fmt.Printf("  %d. ID: %s, Status: %s, Running: %d, Desired: %d\n",
						i+1, *deployment.Id, status, deployment.RunningCount, deployment.DesiredCount)
				}
			}
		}

		// Find the primary active deployment
		for _, deployment := range service.Deployments {
			if deployment.Status != nil && *deployment.Status == "ACTIVE" {
				activeDeployment = &deployment
				break
			}
		}

		// If we haven't found an active deployment yet, continue polling
		if activeDeployment == nil {
			time.Sleep(3 * time.Second)
			attempt++
			continue
		}

		// Check if this is a new deployment (different ID) or an updated deployment
		isNewDeployment := false
		if previousDeploymentState != nil && previousDeploymentState.Id != nil && activeDeployment.Id != nil {
			if *activeDeployment.Id != *previousDeploymentState.Id {
				isNewDeployment = true
				if newDeploymentId == "" {
					newDeploymentId = *activeDeployment.Id
					fmt.Printf("Found new deployment ID: %s\n", newDeploymentId)
				}
			}
		} else {
			// If we don't have previous state, assume this is the deployment we're monitoring
			isNewDeployment = true
		}

		// If this is the same deployment ID, check if it has been updated (force update should cause changes)
		if !isNewDeployment && previousDeploymentState != nil {
			// Check if the deployment has been updated (different task definition, updated timestamp, etc.)
			// For now, we'll assume any active deployment after a force update is what we want to monitor
			isNewDeployment = true
		}

		// Check if the deployment has failed
		if activeDeployment.Status != nil && *activeDeployment.Status == "FAILED" {
			fmt.Printf("Deployment failed!\n")
			os.Exit(1)
		}

		// Check if the deployment is complete and running
		if activeDeployment.Status != nil && *activeDeployment.Status == "ACTIVE" {
			// Debug: Print deployment status
			if attempt%10 == 0 { // Print every 10 attempts (every 30 seconds)
				fmt.Printf("Deployment status: Running=%d, Desired=%d\n",
					activeDeployment.RunningCount, activeDeployment.DesiredCount)
			}

			if activeDeployment.RunningCount == activeDeployment.DesiredCount {

				// Deployment is running successfully
				fmt.Printf("Deployment is running successfully!\n")

				// For force updates, we don't need to wait for old tasks to deregister
				// since force updates typically replace all tasks
				fmt.Printf("Force update completed successfully!\n")
				os.Exit(0)
			}
		} else {
			// Debug: Print why deployment is not active
			if attempt%10 == 0 { // Print every 10 attempts (every 30 seconds)
				status := "unknown"
				if activeDeployment.Status != nil {
					status = *activeDeployment.Status
				}
				fmt.Printf("Deployment not yet active. Status: %s, Running=%d, Desired=%d\n",
					status, activeDeployment.RunningCount, activeDeployment.DesiredCount)
			}
		}

		// Wait before next check (3-second intervals)
		time.Sleep(3 * time.Second)
		attempt++
	}

	// Timeout reached
	fmt.Printf("Deployment did not complete within the timeout period\n")
	os.Exit(1)
	return nil // This line will never be reached due to os.Exit above
}
