package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
)

// showHealthChecks prints task definition container health checks and ALB target group health checks with timeouts
func showHealthChecks(ctx context.Context, config *Config, clusterName string, service *ServiceInfo, taskDef *types.TaskDefinition) {
	fmt.Printf("\n%s\n", color("Health Checks Overview:", ColorBlue))
	fmt.Printf("- %s Task definition health checks run inside the container using a command; they report container health to ECS.\n", color("Task checks:", ColorCyan))
	fmt.Printf("- %s ALB health checks probe the service endpoint over the network; results depend on VPC networking (subnets, security groups, NACLs, routing) and target port exposure.\n", color("ALB checks:", ColorCyan))
	fmt.Printf("- Time to mark unhealthy is approximately attempts × interval (typical) or attempts × (interval + timeout) (worst-case), after any start period/grace.\n")

	// 1) Task definition container health checks
	fmt.Printf("\n%s\n", color("Task Definition Health Checks:", ColorBlue))
	hasHC := false
	secFmt := func(s int32) string {
		if s >= 60 {
			return fmt.Sprintf("%ds (~%dm%ds)", s, s/60, s%60)
		}
		return fmt.Sprintf("%ds", s)
	}
	for _, c := range taskDef.ContainerDefinitions {
		if c.HealthCheck != nil {
			hasHC = true
			hc := c.HealthCheck
			cmd := []string{}
			if hc.Command != nil {
				cmd = hc.Command
			}
			interval := int32(30)
			timeout := int32(5)
			retries := int32(3)
			startPeriod := int32(0)
			if hc.Interval != nil {
				interval = *hc.Interval
			}
			if hc.Timeout != nil {
				timeout = *hc.Timeout
			}
			if hc.Retries != nil {
				retries = *hc.Retries
			}
			if hc.StartPeriod != nil {
				startPeriod = *hc.StartPeriod
			}
			typical := startPeriod + retries*interval
			worst := startPeriod + retries*(interval+timeout)
			fmt.Printf("- Container: %s\n  Command: %s\n  Interval: %s  Timeout: %s  Retries: %d  StartPeriod: %s\n  Approx time until marked UNHEALTHY: typical %s, worst-case %s\n",
				*c.Name, strings.Join(cmd, " "), secFmt(interval), secFmt(timeout), retries, secFmt(startPeriod), secFmt(typical), secFmt(worst))
		}
	}
	if !hasHC {
		fmt.Printf("(none configured)\n")
	}

	// 2) ALB health checks
	fmt.Printf("\n%s\n", color("ALB Target Group Health Checks:", ColorBlue))
	svcOut, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &clusterName, Services: []string{service.Name}})
	if err != nil || len(svcOut.Services) == 0 || len(svcOut.Services[0].LoadBalancers) == 0 || svcOut.Services[0].LoadBalancers[0].TargetGroupArn == nil {
		fmt.Printf("No ALB/TargetGroup configuration found for service.\n")
		return
	}
	tgArn := *svcOut.Services[0].LoadBalancers[0].TargetGroupArn
	tgOut, err := config.ELBv2Client.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}})
	if err != nil || len(tgOut.TargetGroups) == 0 {
		fmt.Printf("Unable to read Target Group details.\n")
		return
	}
	tg := tgOut.TargetGroups[0]
	proto := string(tg.HealthCheckProtocol)
	path := ""
	if tg.HealthCheckPath != nil {
		path = *tg.HealthCheckPath
	}
	port := ""
	if tg.HealthCheckPort != nil {
		port = *tg.HealthCheckPort
	}
	interval := int32(0)
	timeout := int32(0)
	healthy := int32(0)
	unhealthy := int32(0)
	if tg.HealthCheckIntervalSeconds != nil {
		interval = *tg.HealthCheckIntervalSeconds
	}
	if tg.HealthCheckTimeoutSeconds != nil {
		timeout = *tg.HealthCheckTimeoutSeconds
	}
	if tg.HealthyThresholdCount != nil {
		healthy = *tg.HealthyThresholdCount
	}
	if tg.UnhealthyThresholdCount != nil {
		unhealthy = *tg.UnhealthyThresholdCount
	}
	matcher := ""
	if tg.Matcher != nil && tg.Matcher.HttpCode != nil {
		matcher = *tg.Matcher.HttpCode
	}
	typical := unhealthy * interval
	worst := unhealthy * (interval + timeout)
	fmt.Printf("Protocol: %s  Port: %s  Path: %s  Interval: %s  Timeout: %s  Thresholds: healthy=%d unhealthy=%d  Matcher: %s\n",
		proto, port, path, strconv.Itoa(int(interval))+"s", strconv.Itoa(int(timeout))+"s", healthy, unhealthy, matcher)
	fmt.Printf("Approx time until target marked UNHEALTHY: typical %s, worst-case %s (subject to network reachability)\n",
		strconv.Itoa(int(typical))+"s", strconv.Itoa(int(worst))+"s")
}
