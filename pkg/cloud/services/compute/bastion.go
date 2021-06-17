/*
Copyright 2020 The Kubernetes Authors.

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

package compute

import (
	"fmt"

	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
)

func (s *Service) CreateBastion(openStackCluster *infrav1.OpenStackCluster, clusterName string) (*infrav1.Instance, error) {
	name := fmt.Sprintf("%s-bastion", clusterName)
	input := &infrav1.Instance{
		Name:          name,
		Flavor:        openStackCluster.Spec.Bastion.Instance.Flavor,
		SSHKeyName:    openStackCluster.Spec.Bastion.Instance.SSHKeyName,
		Image:         openStackCluster.Spec.Bastion.Instance.Image,
		FailureDomain: openStackCluster.Spec.Bastion.AvailabilityZone,
		RootVolume:    openStackCluster.Spec.Bastion.Instance.RootVolume,
	}

	securityGroups, err := s.getSecurityGroups(openStackCluster.Spec.Bastion.Instance.SecurityGroups)
	if err != nil {
		return nil, err
	}
	if openStackCluster.Spec.ManagedSecurityGroups {
		securityGroups = append(securityGroups, openStackCluster.Status.BastionSecurityGroup.ID)
	}
	input.SecurityGroups = &securityGroups

	var nets []infrav1.Network
	if len(openStackCluster.Spec.Bastion.Instance.Networks) > 0 {
		var err error
		nets, err = s.getServerNetworks(openStackCluster.Spec.Bastion.Instance.Networks)
		if err != nil {
			return nil, err
		}
	} else {
		nets = []infrav1.Network{{
			ID: openStackCluster.Status.Network.ID,
			Subnet: &infrav1.Subnet{
				ID: openStackCluster.Status.Network.Subnet.ID,
			},
		}}
	}
	input.Networks = &nets
	networkLevelPortSecurityDisabled := openStackCluster.Spec.DisablePortSecurity
	out, err := s.createInstance(openStackCluster, clusterName, input, networkLevelPortSecurityDisabled)
	if err != nil {
		return nil, err
	}

	return out, nil
}
