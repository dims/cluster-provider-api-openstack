/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package loadbalancer

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/utils/openstack/clientconfig"

	"sigs.k8s.io/cluster-api-provider-openstack/pkg/cloud/services/networking"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/cloud/services/provider"
)

// Service interfaces with the OpenStack Neutron LBaaS v2 API.
type Service struct {
	projectID          string
	loadbalancerClient *gophercloud.ServiceClient
	networkingService  *networking.Service
	logger             logr.Logger
}

// NewService returns an instance of the loadbalancer service.
func NewService(client *gophercloud.ProviderClient, clientOpts *clientconfig.ClientOpts, logger logr.Logger) (*Service, error) {
	loadbalancerClient, err := openstack.NewLoadBalancerV2(client, gophercloud.EndpointOpts{
		Region: clientOpts.RegionName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer service client: %v", err)
	}

	networkingService, err := networking.NewService(client, clientOpts, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create networking service: %v", err)
	}

	var projectID string
	if clientOpts.AuthInfo != nil {
		projectID = clientOpts.AuthInfo.ProjectID
		if projectID == "" && clientOpts.AuthInfo.ProjectName != "" {
			projectID, err = provider.GetProjectID(client, clientOpts.AuthInfo.ProjectName)
			if err != nil {
				return nil, fmt.Errorf("error retrieveing project id: %v", err)
			}
		}
	}

	return &Service{
		projectID:          projectID,
		loadbalancerClient: loadbalancerClient,
		networkingService:  networkingService,
		logger:             logger,
	}, nil
}
