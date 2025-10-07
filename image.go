package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	qc "github.com/bevelwork/quick_color"
)

// updateImageAction handles the image update action
func updateImageAction(ctx context.Context, config *Config, selectedCluster *ClusterInfo, selectedService *ServiceInfo, taskDef *types.TaskDefinition) {
	reader := bufio.NewReader(os.Stdin)

	// Update image version
	fmt.Printf("%s", qc.Colorize("Enter new image version/tag: ", qc.ColorYellow))
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
	containerImage := getContainerImage(taskDef)
	newImage := updateImageVersion(containerImage, newImageVersion)
	fmt.Printf("Updating image to: %s\n", qc.ColorizeBold(newImage, qc.ColorCyan))

	newTaskDefArn, err := showProgressWithResult("Creating new task definition...", func() (string, error) {
		return createNewTaskDefinition(ctx, config, taskDef, newImage)
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Created new task definition: %s\n", qc.ColorizeBold(newTaskDefArn, qc.ColorGreen))

	// Update service with new task definition
	fmt.Printf("%s", qc.Colorize("Force update service? (y/N): ", qc.ColorYellow))
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

	fmt.Printf("Service %s updated successfully!\n", qc.ColorizeBold(selectedService.Name, qc.ColorGreen))
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
