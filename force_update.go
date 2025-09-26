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
)

// forceUpdateAction handles the force update action
func forceUpdateAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo) {
	fmt.Printf("Force updating service: %s\n", colorBold(selectedService.Name, ColorCyan))

	// Confirm the force update
	fmt.Printf("%s", color("Force update will restart all tasks. Continue? (y/N): ", ColorYellow))
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
		log.Fatal(err)
	}
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

	// Create a channel to signal when to stop the throbber
	done := make(chan bool, 1)

	// Start throbber in a separate goroutine
	go func() {
		i := 0
		for {
			select {
			case <-done:
				return
			default:
				fmt.Printf("\r%s Deployment in progress... (cycle %d)", throbberChars[i%len(throbberChars)], attempt+1)
				time.Sleep(200 * time.Millisecond)
				i++
			}
		}
	}()

	defer func() {
		done <- true
		fmt.Printf("\r\033[K") // Clear the throbber line
	}()

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

		// Check if deployment is complete
		deploymentComplete := true
		for _, deployment := range service.Deployments {
			if deployment.Status != nil && *deployment.Status == "ACTIVE" {
				// Check if this deployment is still in progress
				if deployment.RunningCount != deployment.DesiredCount {
					deploymentComplete = false
					break
				}
			}
		}

		if deploymentComplete {
			fmt.Printf("Deployment completed successfully!\n")
			return nil
		}

		// Wait before next check
		time.Sleep(10 * time.Second)
		attempt++
	}

	return fmt.Errorf("deployment did not complete within the timeout period")
}
