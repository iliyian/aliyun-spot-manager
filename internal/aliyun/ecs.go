package aliyun

import (
	"fmt"
	"strings"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	log "github.com/sirupsen/logrus"
)

// SpotInstance represents a spot instance
type SpotInstance struct {
	InstanceID       string
	InstanceName     string
	RegionID         string
	Status           string
	PublicIPAddress  string
	PrivateIPAddress string
	SpotStrategy     string
}

// ECSClient wraps the Aliyun ECS client
type ECSClient struct {
	accessKeyID     string
	accessKeySecret string
	clients         map[string]*ecs.Client // region -> client
}

// NewECSClient creates a new ECS client
func NewECSClient(accessKeyID, accessKeySecret string) *ECSClient {
	return &ECSClient{
		accessKeyID:     accessKeyID,
		accessKeySecret: accessKeySecret,
		clients:         make(map[string]*ecs.Client),
	}
}

// getClient gets or creates an ECS client for the specified region
func (c *ECSClient) getClient(regionID string) (*ecs.Client, error) {
	if client, ok := c.clients[regionID]; ok {
		return client, nil
	}

	client, err := ecs.NewClientWithAccessKey(regionID, c.accessKeyID, c.accessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create ECS client for region %s: %w", regionID, err)
	}

	c.clients[regionID] = client
	return client, nil
}

// GetAllRegions returns all available regions
func (c *ECSClient) GetAllRegions() ([]string, error) {
	// Use cn-hangzhou as default region to query all regions
	client, err := c.getClient("cn-hangzhou")
	if err != nil {
		return nil, err
	}

	request := ecs.CreateDescribeRegionsRequest()
	request.Scheme = "https"

	response, err := client.DescribeRegions(request)
	if err != nil {
		return nil, fmt.Errorf("failed to describe regions: %w", err)
	}

	regions := make([]string, 0, len(response.Regions.Region))
	for _, region := range response.Regions.Region {
		regions = append(regions, region.RegionId)
	}

	return regions, nil
}

// GetSpotInstances returns all spot instances in the specified region
func (c *ECSClient) GetSpotInstances(regionID string) ([]*SpotInstance, error) {
	client, err := c.getClient(regionID)
	if err != nil {
		return nil, err
	}

	var instances []*SpotInstance
	pageNumber := 1
	pageSize := 100

	for {
		request := ecs.CreateDescribeInstancesRequest()
		request.Scheme = "https"
		request.RegionId = regionID
		request.PageNumber = requests.NewInteger(pageNumber)
		request.PageSize = requests.NewInteger(pageSize)
		// Filter for pay-as-you-go instances (spot instances are a type of pay-as-you-go)
		request.InstanceChargeType = "PostPaid"

		response, err := client.DescribeInstances(request)
		if err != nil {
			return nil, fmt.Errorf("failed to describe instances in region %s: %w", regionID, err)
		}

		for _, inst := range response.Instances.Instance {
			// Filter for spot instances only
			if inst.SpotStrategy != "NoSpot" && inst.SpotStrategy != "" {
				var publicIP, privateIP string
				if len(inst.PublicIpAddress.IpAddress) > 0 {
					publicIP = inst.PublicIpAddress.IpAddress[0]
				}
				// Check EIP
				if publicIP == "" && inst.EipAddress.IpAddress != "" {
					publicIP = inst.EipAddress.IpAddress
				}
				if len(inst.InnerIpAddress.IpAddress) > 0 {
					privateIP = inst.InnerIpAddress.IpAddress[0]
				}
				if privateIP == "" && len(inst.VpcAttributes.PrivateIpAddress.IpAddress) > 0 {
					privateIP = inst.VpcAttributes.PrivateIpAddress.IpAddress[0]
				}

				instances = append(instances, &SpotInstance{
					InstanceID:       inst.InstanceId,
					InstanceName:     inst.InstanceName,
					RegionID:         regionID,
					Status:           inst.Status,
					PublicIPAddress:  publicIP,
					PrivateIPAddress: privateIP,
					SpotStrategy:     inst.SpotStrategy,
				})
			}
		}

		// Check if there are more pages
		if len(response.Instances.Instance) < pageSize {
			break
		}
		pageNumber++
	}

	return instances, nil
}

// GetInstanceStatus returns the current status of an instance
func (c *ECSClient) GetInstanceStatus(regionID, instanceID string) (string, error) {
	client, err := c.getClient(regionID)
	if err != nil {
		return "", err
	}

	request := ecs.CreateDescribeInstanceStatusRequest()
	request.Scheme = "https"
	request.RegionId = regionID
	request.InstanceId = &[]string{instanceID}

	response, err := client.DescribeInstanceStatus(request)
	if err != nil {
		return "", fmt.Errorf("failed to get instance status: %w", err)
	}

	if len(response.InstanceStatuses.InstanceStatus) == 0 {
		return "", fmt.Errorf("instance %s not found", instanceID)
	}

	return response.InstanceStatuses.InstanceStatus[0].Status, nil
}

// GetInstance returns detailed information about an instance
func (c *ECSClient) GetInstance(regionID, instanceID string) (*SpotInstance, error) {
	client, err := c.getClient(regionID)
	if err != nil {
		return nil, err
	}

	request := ecs.CreateDescribeInstancesRequest()
	request.Scheme = "https"
	request.RegionId = regionID
	request.InstanceIds = fmt.Sprintf(`["%s"]`, instanceID)

	response, err := client.DescribeInstances(request)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance: %w", err)
	}

	if len(response.Instances.Instance) == 0 {
		return nil, fmt.Errorf("instance %s not found", instanceID)
	}

	inst := response.Instances.Instance[0]
	var publicIP, privateIP string
	if len(inst.PublicIpAddress.IpAddress) > 0 {
		publicIP = inst.PublicIpAddress.IpAddress[0]
	}
	if publicIP == "" && inst.EipAddress.IpAddress != "" {
		publicIP = inst.EipAddress.IpAddress
	}
	if len(inst.InnerIpAddress.IpAddress) > 0 {
		privateIP = inst.InnerIpAddress.IpAddress[0]
	}
	if privateIP == "" && len(inst.VpcAttributes.PrivateIpAddress.IpAddress) > 0 {
		privateIP = inst.VpcAttributes.PrivateIpAddress.IpAddress[0]
	}

	return &SpotInstance{
		InstanceID:       inst.InstanceId,
		InstanceName:     inst.InstanceName,
		RegionID:         regionID,
		Status:           inst.Status,
		PublicIPAddress:  publicIP,
		PrivateIPAddress: privateIP,
		SpotStrategy:     inst.SpotStrategy,
	}, nil
}

// StartInstance starts an instance
func (c *ECSClient) StartInstance(regionID, instanceID string) error {
	client, err := c.getClient(regionID)
	if err != nil {
		return err
	}

	request := ecs.CreateStartInstanceRequest()
	request.Scheme = "https"
	request.InstanceId = instanceID

	_, err = client.StartInstance(request)
	if err != nil {
		// Check if instance is already running or starting
		if strings.Contains(err.Error(), "IncorrectInstanceStatus") {
			log.Warnf("Instance %s is not in stopped state, skipping start", instanceID)
			return nil
		}
		return fmt.Errorf("failed to start instance %s: %w", instanceID, err)
	}

	return nil
}

// DiscoverAllSpotInstances discovers all spot instances across all regions
func (c *ECSClient) DiscoverAllSpotInstances() ([]*SpotInstance, error) {
	regions, err := c.GetAllRegions()
	if err != nil {
		return nil, err
	}

	var allInstances []*SpotInstance
	for _, region := range regions {
		instances, err := c.GetSpotInstances(region)
		if err != nil {
			log.Warnf("Failed to get spot instances in region %s: %v", region, err)
			continue
		}
		allInstances = append(allInstances, instances...)
	}

	return allInstances, nil
}