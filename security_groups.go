package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	qc "github.com/bevelwork/quick_color"
)

// showSecurityGroups prints ALB and Task security group configuration only
func showSecurityGroups(ctx context.Context, config *Config, clusterName, serviceName string) {
	out, err := config.ECSClient.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &clusterName, Services: []string{serviceName}})
	if err != nil || len(out.Services) == 0 {
		fmt.Printf("%s Unable to describe service: %v\n", qc.Colorize("Error:", qc.ColorRed), err)
		return
	}
	s := out.Services[0]
	fmt.Printf("\n%s\n", qc.Colorize("Security Group Configuration:", qc.ColorBlue))
	albSgIds, _ := resolveAlbSecurityGroups(ctx, config, s)
	if len(albSgIds) > 0 {
		printSgListWithNamesMultiline(ctx, config, "ALB SecurityGroups:", albSgIds)
		displayAggregatedSgRules(ctx, config, albSgIds, "ALB")
	} else {
		fmt.Printf("ALB SecurityGroups: (none or not applicable)\n")
	}
	taskSgIds, _ := resolveTaskSecurityGroups(ctx, config, clusterName, serviceName, s, "")
	if len(taskSgIds) > 0 {
		printSgListWithNamesMultiline(ctx, config, "Task SecurityGroups:", taskSgIds)
		displayAggregatedSgRules(ctx, config, taskSgIds, "Task")
	} else {
		fmt.Printf("Task SecurityGroups: (none or not applicable)\n")
	}
}

// resolveAlbSecurityGroups returns the Load Balancer security groups and VPC ID, if available
func resolveAlbSecurityGroups(ctx context.Context, config *Config, svc types.Service) ([]string, string) {
	albSgIds := []string{}
	vpcId := ""
	if len(svc.LoadBalancers) == 0 || svc.LoadBalancers[0].TargetGroupArn == nil {
		return albSgIds, vpcId
	}
	tgArn := *svc.LoadBalancers[0].TargetGroupArn
	tgOut, err := config.ELBv2Client.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: []string{tgArn}})
	if err != nil || len(tgOut.TargetGroups) == 0 || len(tgOut.TargetGroups[0].LoadBalancerArns) == 0 {
		return albSgIds, vpcId
	}
	lbArn := tgOut.TargetGroups[0].LoadBalancerArns[0]
	lbOut, err := config.ELBv2Client.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	if err != nil || len(lbOut.LoadBalancers) == 0 {
		return albSgIds, vpcId
	}
	lb := lbOut.LoadBalancers[0]
	albSgIds = append(albSgIds, lb.SecurityGroups...)
	if lb.VpcId != nil {
		vpcId = *lb.VpcId
	}
	return albSgIds, vpcId
}

// resolveTaskSecurityGroups returns task-level security groups (awsvpc or EC2 instance SGs) and a possibly updated VPC ID
func resolveTaskSecurityGroups(ctx context.Context, config *Config, clusterName, serviceName string, svc types.Service, vpcId string) ([]string, string) {
	// awsvpc path
	if svc.NetworkConfiguration != nil && svc.NetworkConfiguration.AwsvpcConfiguration != nil && len(svc.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups) > 0 {
		return append([]string{}, svc.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups...), vpcId
	}
	// EC2 bridge/host path: derive from instance SGs using a representative running task
	taskSgIds := []string{}
	tasks, err := config.ECSClient.ListTasks(ctx, &ecs.ListTasksInput{Cluster: &clusterName, ServiceName: &serviceName, DesiredStatus: types.DesiredStatusRunning})
	if err != nil || len(tasks.TaskArns) == 0 {
		return taskSgIds, vpcId
	}
	desc, err := config.ECSClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: &clusterName, Tasks: tasks.TaskArns[:1]})
	if err != nil || len(desc.Tasks) == 0 {
		return taskSgIds, vpcId
	}
	t := desc.Tasks[0]
	if t.ContainerInstanceArn == nil {
		return taskSgIds, vpcId
	}
	ciOut, err := config.ECSClient.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{Cluster: &clusterName, ContainerInstances: []string{*t.ContainerInstanceArn}})
	if err != nil || len(ciOut.ContainerInstances) == 0 || ciOut.ContainerInstances[0].Ec2InstanceId == nil {
		return taskSgIds, vpcId
	}
	iid := *ciOut.ContainerInstances[0].Ec2InstanceId
	instOut, err := config.EC2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{iid}})
	if err != nil || len(instOut.Reservations) == 0 || len(instOut.Reservations[0].Instances) == 0 {
		return taskSgIds, vpcId
	}
	inst := instOut.Reservations[0].Instances[0]
	for _, sg := range inst.SecurityGroups {
		if sg.GroupId != nil {
			taskSgIds = append(taskSgIds, *sg.GroupId)
		}
	}
	if vpcId == "" && inst.VpcId != nil {
		vpcId = *inst.VpcId
	}
	return taskSgIds, vpcId
}

// formatSubnetsWithNames returns VPC ID from first subnet (if present) and subnet IDs formatted with Name tag when available
func formatSubnetsWithNames(ctx context.Context, config *Config, subnetIds []string) (string, []string) {
	if len(subnetIds) == 0 {
		return "", nil
	}
	subOut, err := config.EC2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: subnetIds})
	if err != nil {
		return "", nil
	}
	nameFor := func(tags []ec2types.Tag) string {
		for _, t := range tags {
			if t.Key != nil && *t.Key == "Name" && t.Value != nil {
				return *t.Value
			}
		}
		return ""
	}
	formatted := make([]string, 0, len(subOut.Subnets))
	vpcId := ""
	for i, sn := range subOut.Subnets {
		if i == 0 && sn.VpcId != nil {
			vpcId = *sn.VpcId
		}
		id := ""
		if sn.SubnetId != nil {
			id = *sn.SubnetId
		}
		n := nameFor(sn.Tags)
		if n != "" {
			formatted = append(formatted, fmt.Sprintf("%s(%s)", id, n))
		} else {
			formatted = append(formatted, id)
		}
	}
	return vpcId, formatted
}

// displayAggregatedSgRules prints cumulative inbound and outbound rules for the provided SGs
func displayAggregatedSgRules(ctx context.Context, config *Config, sgIds []string, label string) {
	if len(sgIds) == 0 {
		return
	}
	out, err := config.EC2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: sgIds})
	if err != nil || len(out.SecurityGroups) == 0 {
		return
	}
	// Build name map and aggregate rules
	nameOf := func(sg ec2types.SecurityGroup) string {
		name := ""
		if sg.GroupName != nil {
			name = *sg.GroupName
		}
		if name == "" {
			return *sg.GroupId
		}
		return fmt.Sprintf("%s(%s)", *sg.GroupId, name)
	}

	type ruleKey struct {
		dir, proto string
		from, to   int32
		src        string
	}
	agg := map[ruleKey][]string{}

	// Inbound
	for _, sg := range out.SecurityGroups {
		idWithName := nameOf(sg)
		for _, p := range sg.IpPermissions {
			proto := ""
			if p.IpProtocol != nil {
				proto = *p.IpProtocol
			}
			from, to := int32(-1), int32(-1)
			if p.FromPort != nil {
				from = *p.FromPort
			}
			if p.ToPort != nil {
				to = *p.ToPort
			}
			// IPv4 ranges
			for _, r := range p.IpRanges {
				cidr := ""
				if r.CidrIp != nil {
					cidr = *r.CidrIp
				}
				k := ruleKey{"in", proto, from, to, cidr}
				agg[k] = append(agg[k], idWithName)
			}
			// IPv6 ranges
			for _, r := range p.Ipv6Ranges {
				cidr := ""
				if r.CidrIpv6 != nil {
					cidr = *r.CidrIpv6
				}
				k := ruleKey{"in", proto, from, to, cidr}
				agg[k] = append(agg[k], idWithName)
			}
			// SG references
			for _, r := range p.UserIdGroupPairs {
				src := "sg-unknown"
				if r.GroupId != nil {
					src = *r.GroupId
				}
				k := ruleKey{"in", proto, from, to, src}
				agg[k] = append(agg[k], idWithName)
			}
		}
	}
	// Outbound
	for _, sg := range out.SecurityGroups {
		idWithName := nameOf(sg)
		for _, p := range sg.IpPermissionsEgress {
			proto := ""
			if p.IpProtocol != nil {
				proto = *p.IpProtocol
			}
			from, to := int32(-1), int32(-1)
			if p.FromPort != nil {
				from = *p.FromPort
			}
			if p.ToPort != nil {
				to = *p.ToPort
			}
			for _, r := range p.IpRanges {
				cidr := ""
				if r.CidrIp != nil {
					cidr = *r.CidrIp
				}
				k := ruleKey{"out", proto, from, to, cidr}
				agg[k] = append(agg[k], idWithName)
			}
			for _, r := range p.Ipv6Ranges {
				cidr := ""
				if r.CidrIpv6 != nil {
					cidr = *r.CidrIpv6
				}
				k := ruleKey{"out", proto, from, to, cidr}
				agg[k] = append(agg[k], idWithName)
			}
			for _, r := range p.UserIdGroupPairs {
				src := "sg-unknown"
				if r.GroupId != nil {
					src = *r.GroupId
				}
				k := ruleKey{"out", proto, from, to, src}
				agg[k] = append(agg[k], idWithName)
			}
		}
	}

	// Pretty print
	fmt.Printf("  %s cumulative rules:\n", label)
	// Collect keys for stable order
	keys := make([]ruleKey, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].dir != keys[j].dir {
			return keys[i].dir < keys[j].dir
		}
		if keys[i].proto != keys[j].proto {
			return keys[i].proto < keys[j].proto
		}
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		if keys[i].to != keys[j].to {
			return keys[i].to < keys[j].to
		}
		return keys[i].src < keys[j].src
	})
	for _, k := range keys {
		dirLabel := ""
		if k.dir == "in" {
			dirLabel = qc.Colorize("[In]", qc.ColorGreen)
		} else {
			dirLabel = qc.Colorize("[Out]", qc.ColorBlue)
		}
		portStr := "all"
		if k.from >= 0 && k.to >= 0 {
			if k.from == k.to {
				portStr = fmt.Sprintf("%d", k.from)
			} else {
				portStr = fmt.Sprintf("%d-%d", k.from, k.to)
			}
		}
		// Build compact SG list: SGs: abcd, 1234, ... (last 4 of GroupId inside each nameOf value)
		compactIds := make([]string, 0, len(agg[k]))
		for _, h := range agg[k] {
			// h is like sg-xxxxxxxx(name) or sg-xxxxxxxx
			id := h
			if i := strings.Index(h, "("); i != -1 {
				id = h[:i]
			}
			// take last 4 characters of the id
			suffix := id
			if len(id) > 4 {
				suffix = id[len(id)-4:]
			}
			compactIds = append(compactIds, suffix)
		}
		fmt.Printf("    %s %s %s %s [SGs: %s]\n", dirLabel, k.proto, portStr, k.src, strings.Join(compactIds, ", "))
	}
}

// printSgListWithNamesMultiline prints SGs one-per-line with name colored blue if present
func printSgListWithNamesMultiline(ctx context.Context, config *Config, header string, sgIds []string) {
	fmt.Printf("%s\n", header)
	if len(sgIds) == 0 {
		return
	}
	out, err := config.EC2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: sgIds})
	if err != nil || len(out.SecurityGroups) == 0 {
		for _, id := range sgIds {
			fmt.Printf("  - %s\n", id)
		}
		return
	}
	// Build name map
	names := map[string]string{}
	for _, sg := range out.SecurityGroups {
		if sg.GroupId != nil {
			n := ""
			if sg.GroupName != nil {
				n = *sg.GroupName
			}
			names[*sg.GroupId] = n
		}
	}
	for _, id := range sgIds {
		if n, ok := names[id]; ok && n != "" {
			fmt.Printf("  - %s(%s)\n", id, qc.Colorize(n, qc.ColorBlue))
		} else {
			fmt.Printf("  - %s\n", id)
		}
	}
}
