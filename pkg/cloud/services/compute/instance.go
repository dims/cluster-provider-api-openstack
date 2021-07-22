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

package compute

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/gophercloud/gophercloud/openstack/common/extensions"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/attachinterfaces"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/bootfromvolume"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/floatingips"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/schedulerhints"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	netext "github.com/gophercloud/gophercloud/openstack/networking/v2/extensions"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/attributestags"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/portsbinding"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/portsecurity"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/trunks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"github.com/gophercloud/utils/openstack/compute/v2/flavors"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util"

	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/metrics"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/record"
	capoerrors "sigs.k8s.io/cluster-api-provider-openstack/pkg/utils/errors"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/utils/names"
)

const (
	timeoutInstanceCreate       = 5
	retryIntervalInstanceStatus = 10 * time.Second

	timeoutTrunkDelete       = 3 * time.Minute
	retryIntervalTrunkDelete = 5 * time.Second

	timeoutPortDelete       = 3 * time.Minute
	retryIntervalPortDelete = 5 * time.Second

	timeoutInstanceDelete = 5 * time.Minute
)

func (s *Service) CreateInstance(openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine, clusterName string, userData string) (instance *infrav1.Instance, err error) {
	if openStackMachine == nil {
		return nil, fmt.Errorf("create Options need be specified to create instace")
	}

	if machine.Spec.FailureDomain == nil {
		return nil, fmt.Errorf("failure domain not set")
	}

	input := &infrav1.Instance{
		Name:          openStackMachine.Name,
		Image:         openStackMachine.Spec.Image,
		Flavor:        openStackMachine.Spec.Flavor,
		SSHKeyName:    openStackMachine.Spec.SSHKeyName,
		UserData:      userData,
		Metadata:      openStackMachine.Spec.ServerMetadata,
		ConfigDrive:   openStackMachine.Spec.ConfigDrive,
		FailureDomain: *machine.Spec.FailureDomain,
		RootVolume:    openStackMachine.Spec.RootVolume,
		Subnet:        openStackMachine.Spec.Subnet,
		ServerGroupID: openStackMachine.Spec.ServerGroupID,
	}

	if openStackMachine.Spec.Trunk {
		trunkSupport, err := s.getTrunkSupport()
		if err != nil {
			return nil, fmt.Errorf("there was an issue verifying whether trunk support is available, please disable it: %v", err)
		}
		if !trunkSupport {
			return nil, fmt.Errorf("there is no trunk support. Please disable it")
		}
		input.Trunk = trunkSupport
	}

	machineTags := []string{}

	// Append machine specific tags
	machineTags = append(machineTags, openStackMachine.Spec.Tags...)

	// Append cluster scope tags
	machineTags = append(machineTags, openStackCluster.Spec.Tags...)

	// tags need to be unique or the "apply tags" call will fail.
	machineTags = deduplicate(machineTags)

	input.Tags = machineTags

	// Get security groups
	securityGroups, err := s.getSecurityGroups(openStackMachine.Spec.SecurityGroups)
	if err != nil {
		return nil, err
	}
	if openStackCluster.Spec.ManagedSecurityGroups {
		if util.IsControlPlaneMachine(machine) {
			securityGroups = append(securityGroups, openStackCluster.Status.ControlPlaneSecurityGroup.ID)
		} else {
			securityGroups = append(securityGroups, openStackCluster.Status.WorkerSecurityGroup.ID)
		}
	}
	input.SecurityGroups = &securityGroups

	nets, err := s.constructNetworks(openStackCluster, openStackMachine)
	if err != nil {
		return nil, err
	}
	input.Networks = nets

	out, err := s.createInstance(openStackMachine, clusterName, input)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// constructNetworks builds an array of networks from the network, subnet and ports items in the machine spec.
// If no networks or ports are in the spec, returns a single network item for a network connection to the default cluster network.
func (s *Service) constructNetworks(openStackCluster *infrav1.OpenStackCluster, openStackMachine *infrav1.OpenStackMachine) (*[]infrav1.Network, error) {
	var nets []infrav1.Network
	if len(openStackMachine.Spec.Networks) > 0 {
		var err error
		nets, err = s.getServerNetworks(openStackMachine.Spec.Networks)
		if err != nil {
			return nil, err
		}
	}
	for i, port := range openStackMachine.Spec.Ports {
		if port.NetworkID != "" {
			nets = append(nets, infrav1.Network{
				ID:       port.NetworkID,
				Subnet:   &infrav1.Subnet{},
				PortOpts: &openStackMachine.Spec.Ports[i],
			})
		} else {
			nets = append(nets, infrav1.Network{
				ID: openStackCluster.Status.Network.ID,
				Subnet: &infrav1.Subnet{
					ID: openStackCluster.Status.Network.Subnet.ID,
				},
				PortOpts: &openStackMachine.Spec.Ports[i],
			})
		}
	}
	// no networks or ports found in the spec, so create a port on the cluster network
	if len(nets) == 0 {
		nets = []infrav1.Network{{
			ID: openStackCluster.Status.Network.ID,
			Subnet: &infrav1.Subnet{
				ID: openStackCluster.Status.Network.Subnet.ID,
			},
		}}
	}
	return &nets, nil
}

func (s *Service) createInstance(eventObject runtime.Object, clusterName string, instance *infrav1.Instance) (*infrav1.Instance, error) {
	accessIPv4 := ""
	portList := []servers.Network{}

	for i, network := range *instance.Networks {
		if network.ID == "" {
			return nil, fmt.Errorf("no network was found or provided. Please check your machine configuration and try again")
		}

		portName := getPortName(instance.Name, network.PortOpts, i)
		port, err := s.getOrCreatePort(eventObject, clusterName, portName, network, instance.SecurityGroups)
		if err != nil {
			return nil, err
		}

		if instance.Trunk {
			trunk, err := s.getOrCreateTrunk(eventObject, clusterName, instance.Name, port.ID)
			if err != nil {
				return nil, err
			}

			if err = s.replaceAllAttributesTags(eventObject, trunk.ID, instance.Tags); err != nil {
				return nil, err
			}
		}

		for _, fip := range port.FixedIPs {
			if fip.SubnetID == instance.Subnet {
				accessIPv4 = fip.IPAddress
			}
		}

		portList = append(portList, servers.Network{
			Port: port.ID,
		})
	}

	if instance.Subnet != "" && accessIPv4 == "" {
		if err := s.deletePorts(eventObject, portList); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no ports with fixed IPs found on Subnet %q", instance.Subnet)
	}

	imageID, err := s.getImageID(instance.Image)
	if err != nil {
		return nil, fmt.Errorf("create new server: %v", err)
	}

	flavorID, err := flavors.IDFromName(s.computeClient, instance.Flavor)
	if err != nil {
		return nil, fmt.Errorf("error getting flavor id from flavor name %s: %v", instance.Flavor, err)
	}

	var serverCreateOpts servers.CreateOptsBuilder = servers.CreateOpts{
		Name:             instance.Name,
		ImageRef:         imageID,
		FlavorRef:        flavorID,
		AvailabilityZone: instance.FailureDomain,
		Networks:         portList,
		UserData:         []byte(instance.UserData),
		SecurityGroups:   *instance.SecurityGroups,
		Tags:             instance.Tags,
		Metadata:         instance.Metadata,
		ConfigDrive:      instance.ConfigDrive,
		AccessIPv4:       accessIPv4,
	}

	serverCreateOpts = applyRootVolume(serverCreateOpts, instance.RootVolume)

	serverCreateOpts = applyServerGroupID(serverCreateOpts, instance.ServerGroupID)

	mc := metrics.NewMetricPrometheusContext("server", "create")

	server, err := servers.Create(s.computeClient, keypairs.CreateOptsExt{
		CreateOptsBuilder: serverCreateOpts,
		KeyName:           instance.SSHKeyName,
	}).Extract()

	if mc.ObserveRequest(err) != nil {
		serverErr := err
		if err = s.deletePorts(eventObject, portList); err != nil {
			return nil, fmt.Errorf("error creating OpenStack instance: %v, error deleting ports: %v", serverErr, err)
		}
		return nil, fmt.Errorf("error creating Openstack instance: %v", serverErr)
	}
	instanceCreateTimeout := getTimeout("CLUSTER_API_OPENSTACK_INSTANCE_CREATE_TIMEOUT", timeoutInstanceCreate)
	instanceCreateTimeout *= time.Minute
	var createdInstance *infrav1.Instance
	err = util.PollImmediate(retryIntervalInstanceStatus, instanceCreateTimeout, func() (bool, error) {
		createdInstance, err = s.GetInstance(server.ID)
		if err != nil {
			if capoerrors.IsRetryable(err) {
				return false, nil
			}
			return false, err
		}
		return createdInstance.State == infrav1.InstanceStateActive, nil
	})
	if err != nil {
		record.Warnf(eventObject, "FailedCreateServer", "Failed to create server %s: %v", createdInstance.Name, err)
		return nil, err
	}

	record.Eventf(eventObject, "SuccessfulCreateServer", "Created server %s with id %s", createdInstance.Name, createdInstance.ID)
	return createdInstance, nil
}

// getPortName appends a suffix to an instance name in order to try and get a unique name per port.
func getPortName(instanceName string, opts *infrav1.PortOpts, netIndex int) string {
	if opts != nil && opts.NameSuffix != "" {
		return fmt.Sprintf("%s-%s", instanceName, opts.NameSuffix)
	}
	return fmt.Sprintf("%s-%d", instanceName, netIndex)
}

// applyRootVolume sets a root volume if the root volume Size is not 0.
func applyRootVolume(opts servers.CreateOptsBuilder, rootVolume *infrav1.RootVolume) servers.CreateOptsBuilder {
	if rootVolume != nil && rootVolume.Size != 0 {
		block := bootfromvolume.BlockDevice{
			SourceType:          bootfromvolume.SourceType(rootVolume.SourceType),
			BootIndex:           0,
			UUID:                rootVolume.SourceUUID,
			DeleteOnTermination: true,
			DestinationType:     bootfromvolume.DestinationVolume,
			VolumeSize:          rootVolume.Size,
			DeviceType:          rootVolume.DeviceType,
		}
		return bootfromvolume.CreateOptsExt{
			CreateOptsBuilder: opts,
			BlockDevice:       []bootfromvolume.BlockDevice{block},
		}
	}
	return opts
}

// applyServerGroupID adds a scheduler hint to the CreateOptsBuilder, if the
// spec contains a server group ID.
func applyServerGroupID(opts servers.CreateOptsBuilder, serverGroupID string) servers.CreateOptsBuilder {
	if serverGroupID != "" {
		return schedulerhints.CreateOptsExt{
			CreateOptsBuilder: opts,
			SchedulerHints: schedulerhints.SchedulerHints{
				Group: serverGroupID,
			},
		}
	}
	return opts
}

func (s *Service) getTrunkSupport() (bool, error) {
	mc := metrics.NewMetricPrometheusContext("network_extension", "list")
	allPages, err := netext.List(s.networkClient).AllPages()
	if mc.ObserveRequest(err) != nil {
		return false, err
	}

	allExts, err := extensions.ExtractExtensions(allPages)
	if err != nil {
		return false, err
	}

	for _, ext := range allExts {
		if ext.Alias == "trunk" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) getSecurityGroups(securityGroupParams []infrav1.SecurityGroupParam) ([]string, error) {
	var sgIDs []string
	for _, sg := range securityGroupParams {
		listOpts := groups.ListOpts(sg.Filter)
		if listOpts.ProjectID == "" {
			listOpts.ProjectID = s.projectID
		}
		listOpts.Name = sg.Name
		listOpts.ID = sg.UUID
		mc := metrics.NewMetricPrometheusContext("security_group", "list")
		pages, err := groups.List(s.networkClient, listOpts).AllPages()
		if mc.ObserveRequest(err) != nil {
			return nil, err
		}

		SGList, err := groups.ExtractGroups(pages)
		if err != nil {
			return nil, err
		}

		if len(SGList) == 0 {
			return nil, fmt.Errorf("security group %s not found", sg.Name)
		}

		for _, group := range SGList {
			if isDuplicate(sgIDs, group.ID) {
				continue
			}
			sgIDs = append(sgIDs, group.ID)
		}
	}
	return sgIDs, nil
}

func (s *Service) getServerNetworks(networkParams []infrav1.NetworkParam) ([]infrav1.Network, error) {
	var nets []infrav1.Network
	for _, networkParam := range networkParams {
		opts := networks.ListOpts(networkParam.Filter)
		opts.ID = networkParam.UUID
		ids, err := s.networkingService.GetNetworkIDsByFilter(&opts)
		if err != nil {
			return nil, err
		}
		for _, netID := range ids {
			if networkParam.Subnets == nil {
				nets = append(nets, infrav1.Network{
					ID:     netID,
					Subnet: &infrav1.Subnet{},
				})
				continue
			}

			for _, subnet := range networkParam.Subnets {
				subnetOpts := subnets.ListOpts(subnet.Filter)
				subnetOpts.ID = subnet.UUID
				subnetOpts.NetworkID = netID
				subnetsByFilter, err := s.networkingService.GetSubnetsByFilter(&subnetOpts)
				if err != nil {
					return nil, err
				}
				for _, subnetByFilter := range subnetsByFilter {
					nets = append(nets, infrav1.Network{
						ID: subnetByFilter.NetworkID,
						Subnet: &infrav1.Subnet{
							ID: subnetByFilter.ID,
						},
					})
				}
			}
		}
	}
	return nets, nil
}

func (s *Service) getOrCreatePort(eventObject runtime.Object, clusterName string, portName string, net infrav1.Network, instanceSecurityGroups *[]string) (*ports.Port, error) {
	mc := metrics.NewMetricPrometheusContext("port", "list")
	allPages, err := ports.List(s.networkClient, ports.ListOpts{
		Name:      portName,
		NetworkID: net.ID,
	}).AllPages()
	if mc.ObserveRequest(err) != nil {
		return nil, fmt.Errorf("searching for existing port for server: %v", err)
	}
	existingPorts, err := ports.ExtractPorts(allPages)
	if err != nil {
		return nil, fmt.Errorf("searching for existing port for server: %v", err)
	}

	if len(existingPorts) == 1 {
		return &existingPorts[0], nil
	}

	if len(existingPorts) > 1 {
		return nil, fmt.Errorf("multiple ports found with name \"%s\"", portName)
	}

	// no port found, so create the port
	portOpts := net.PortOpts
	if portOpts == nil {
		portOpts = &infrav1.PortOpts{}
	}

	description := portOpts.Description
	if description == "" {
		description = names.GetDescription(clusterName)
	}

	var securityGroups *[]string
	addressPairs := []ports.AddressPair{}
	if portOpts.DisablePortSecurity == nil || !*portOpts.DisablePortSecurity {
		for _, ap := range portOpts.AllowedAddressPairs {
			addressPairs = append(addressPairs, ports.AddressPair{
				IPAddress:  ap.IPAddress,
				MACAddress: ap.MACAddress,
			})
		}

		securityGroups = portOpts.SecurityGroups

		// inherit port security groups from the instance if not explicitly specified
		if securityGroups == nil {
			securityGroups = instanceSecurityGroups
		}
	}

	createOpts := ports.CreateOpts{
		Name:                portName,
		NetworkID:           net.ID,
		Description:         description,
		AdminStateUp:        portOpts.AdminStateUp,
		MACAddress:          portOpts.MACAddress,
		TenantID:            portOpts.TenantID,
		ProjectID:           portOpts.ProjectID,
		SecurityGroups:      securityGroups,
		AllowedAddressPairs: addressPairs,
	}

	var finalCreateOpts ports.CreateOptsBuilder
	finalCreateOpts = createOpts

	if portOpts.DisablePortSecurity != nil {
		portSecurity := !*portOpts.DisablePortSecurity
		finalCreateOpts = portsecurity.PortCreateOptsExt{
			CreateOptsBuilder:   finalCreateOpts,
			PortSecurityEnabled: &portSecurity,
		}
	}

	finalCreateOpts = portsbinding.CreateOptsExt{
		CreateOptsBuilder: finalCreateOpts,
		HostID:            portOpts.HostID,
		VNICType:          portOpts.VNICType,
		Profile:           nil,
	}

	fixedIPs := make([]ports.IP, 0, len(portOpts.FixedIPs)+1)
	for _, fixedIP := range portOpts.FixedIPs {
		fixedIPs = append(fixedIPs, ports.IP{
			SubnetID:  fixedIP.SubnetID,
			IPAddress: fixedIP.IPAddress,
		})
	}
	if net.Subnet.ID != "" {
		fixedIPs = append(fixedIPs, ports.IP{SubnetID: net.Subnet.ID})
	}
	if len(fixedIPs) > 0 {
		createOpts.FixedIPs = fixedIPs
	}

	mc = metrics.NewMetricPrometheusContext("port", "create")
	port, err := ports.Create(s.networkClient, finalCreateOpts).Extract()
	if mc.ObserveRequest(err) != nil {
		record.Warnf(eventObject, "FailedCreatePort", "Failed to create port %s: %v", portName, err)
		return nil, err
	}

	record.Eventf(eventObject, "SuccessfulCreatePort", "Created port %s with id %s", port.Name, port.ID)
	return port, nil
}

func (s *Service) getOrCreateTrunk(eventObject runtime.Object, clusterName, trunkName, portID string) (*trunks.Trunk, error) {
	mc := metrics.NewMetricPrometheusContext("trunk", "list")
	allPages, err := trunks.List(s.networkClient, trunks.ListOpts{
		Name:   trunkName,
		PortID: portID,
	}).AllPages()
	if mc.ObserveRequest(err) != nil {
		return nil, fmt.Errorf("searching for existing trunk for server: %v", err)
	}
	trunkList, err := trunks.ExtractTrunks(allPages)
	if err != nil {
		return nil, fmt.Errorf("searching for existing trunk for server: %v", err)
	}

	if len(trunkList) != 0 {
		return &trunkList[0], nil
	}

	trunkCreateOpts := trunks.CreateOpts{
		Name:        trunkName,
		PortID:      portID,
		Description: names.GetDescription(clusterName),
	}

	mc = metrics.NewMetricPrometheusContext("trunk", "create")
	trunk, err := trunks.Create(s.networkClient, trunkCreateOpts).Extract()
	if mc.ObserveRequest(err) != nil {
		record.Warnf(eventObject, "FailedCreateTrunk", "Failed to create trunk %s: %v", trunkName, err)
		return nil, err
	}

	record.Eventf(eventObject, "SuccessfulCreateTrunk", "Created trunk %s with id %s", trunk.Name, trunk.ID)
	return trunk, nil
}

func (s *Service) replaceAllAttributesTags(eventObject runtime.Object, trunkID string, tags []string) error {
	mc := metrics.NewMetricPrometheusContext("trunk", "update")
	_, err := attributestags.ReplaceAll(s.networkClient, "trunks", trunkID, attributestags.ReplaceAllOpts{
		Tags: tags,
	}).Extract()
	if mc.ObserveRequest(err) != nil {
		record.Warnf(eventObject, "FailedReplaceAllAttributesTags", "Failed to replace all attributestags, trunk %s: %v", trunkID, err)
		return err
	}

	record.Eventf(eventObject, "SuccessfulReplaceAllAttributeTags", "Replaced all attributestags %s with tags %s", trunkID, tags)
	return nil
}

// Helper function for getting image ID from name.
func (s *Service) getImageID(imageName string) (string, error) {
	if imageName == "" {
		return "", nil
	}

	opts := images.ListOpts{
		Name: imageName,
	}

	mc := metrics.NewMetricPrometheusContext("image", "list")
	pages, err := images.List(s.imagesClient, opts).AllPages()
	if mc.ObserveRequest(err) != nil {
		return "", err
	}

	allImages, err := images.ExtractImages(pages)
	if err != nil {
		return "", err
	}

	switch len(allImages) {
	case 0:
		return "", fmt.Errorf("no image with the name %s could be found", imageName)
	case 1:
		return allImages[0].ID, nil
	default:
		return "", fmt.Errorf("too many images with the name, %s, were found", imageName)
	}
}

func (s *Service) AssociateFloatingIP(instanceID, floatingIP string) error {
	opts := floatingips.AssociateOpts{
		FloatingIP: floatingIP,
	}
	mc := metrics.NewMetricPrometheusContext("floating_ip", "update")
	err := floatingips.AssociateInstance(s.computeClient, instanceID, opts).ExtractErr()
	if mc.ObserveRequest(err) != nil {
		return err
	}
	return nil
}

func (s *Service) DeleteInstance(eventObject runtime.Object, instanceName string) error {
	instance, err := s.InstanceExists(instanceName)
	if err != nil {
		return err
	}

	if instance == nil || instance.ID == "" {
		return nil
	}

	mc := metrics.NewMetricPrometheusContext("server_os_interface", "list")
	allInterfaces, err := attachinterfaces.List(s.computeClient, instance.ID).AllPages()
	if mc.ObserveRequest(err) != nil {
		return err
	}
	instanceInterfaces, err := attachinterfaces.ExtractInterfaces(allInterfaces)
	if err != nil {
		return err
	}
	if len(instanceInterfaces) < 1 {
		return s.deleteInstance(eventObject, instance.ID)
	}

	trunkSupport, err := s.getTrunkSupport()
	if err != nil {
		return fmt.Errorf("obtaining network extensions: %v", err)
	}
	// get and delete trunks
	for _, port := range instanceInterfaces {
		if err = s.deleteAttachInterface(eventObject, instance.ID, port.PortID); err != nil {
			return err
		}

		if trunkSupport {
			if err = s.deleteTrunk(eventObject, port.PortID); err != nil {
				return err
			}
		}

		if err = s.deletePort(eventObject, port.PortID); err != nil {
			return err
		}
	}

	return s.deleteInstance(eventObject, instance.ID)
}

func (s *Service) deletePort(eventObject runtime.Object, portID string) error {
	port, err := s.getPort(portID)
	if err != nil {
		return err
	}
	if port == nil {
		return nil
	}

	err = util.PollImmediate(retryIntervalPortDelete, timeoutPortDelete, func() (bool, error) {
		mc := metrics.NewMetricPrometheusContext("port", "delete")
		err := ports.Delete(s.networkClient, port.ID).ExtractErr()
		if mc.ObserveRequest(err) != nil {
			if capoerrors.IsRetryable(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		record.Warnf(eventObject, "FailedDeletePort", "Failed to delete port %s with id %s: %v", port.Name, port.ID, err)
		return err
	}

	record.Eventf(eventObject, "SuccessfulDeletePort", "Deleted port %s with id %s", port.Name, port.ID)
	return nil
}

func (s *Service) deletePorts(eventObject runtime.Object, nets []servers.Network) error {
	for _, n := range nets {
		if err := s.deletePort(eventObject, n.Port); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteAttachInterface(eventObject runtime.Object, instanceID, portID string) error {
	instance, err := s.GetInstance(instanceID)
	if err != nil {
		return err
	}
	if instance == nil || instance.ID == "" {
		return nil
	}

	port, err := s.getPort(portID)
	if err != nil {
		return err
	}
	if port == nil {
		return nil
	}

	mc := metrics.NewMetricPrometheusContext("server_os_interface", "delete")
	err = attachinterfaces.Delete(s.computeClient, instanceID, portID).ExtractErr()
	if mc.ObserveRequestIgnoreNotFound(err) != nil {
		if capoerrors.IsNotFound(err) {
			return nil
		}
		record.Warnf(eventObject, "FailedDeleteAttachInterface", "Failed to delete attach interface: instance %s, port %s: %v", instance.ID, port.ID, err)
		return err
	}

	record.Eventf(eventObject, "SuccessfulDeleteAttachInterface", "Deleted attach interface: instance %s, port %s", instance.ID, port.ID)
	return nil
}

func (s *Service) deleteTrunk(eventObject runtime.Object, portID string) error {
	port, err := s.getPort(portID)
	if err != nil {
		return err
	}
	if port == nil {
		return nil
	}

	listOpts := trunks.ListOpts{
		PortID: port.ID,
	}
	mc := metrics.NewMetricPrometheusContext("trunk", "list")
	trunkList, err := trunks.List(s.networkClient, listOpts).AllPages()
	if mc.ObserveRequest(err) != nil {
		return err
	}
	trunkInfo, err := trunks.ExtractTrunks(trunkList)
	if err != nil {
		return err
	}
	if len(trunkInfo) != 1 {
		return nil
	}

	err = util.PollImmediate(retryIntervalTrunkDelete, timeoutTrunkDelete, func() (bool, error) {
		mc := metrics.NewMetricPrometheusContext("trunk", "delete")
		if err := trunks.Delete(s.networkClient, trunkInfo[0].ID).ExtractErr(); mc.ObserveRequest(err) != nil {
			if capoerrors.IsRetryable(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil {
		record.Warnf(eventObject, "FailedDeleteTrunk", "Failed to delete trunk %s with id %s: %v", trunkInfo[0].Name, trunkInfo[0].ID, err)
		return err
	}

	record.Eventf(eventObject, "SuccessfulDeleteTrunk", "Deleted trunk %s with id %s", trunkInfo[0].Name, trunkInfo[0].ID)
	return nil
}

func (s *Service) getPort(portID string) (port *ports.Port, err error) {
	if portID == "" {
		return nil, fmt.Errorf("portID should be specified to get detail")
	}
	mc := metrics.NewMetricPrometheusContext("port", "get")
	port, err = ports.Get(s.networkClient, portID).Extract()
	if mc.ObserveRequestIgnoreNotFound(err) != nil {
		if capoerrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get port %q detail failed: %v", portID, err)
	}
	return port, nil
}

func (s *Service) deleteInstance(eventObject runtime.Object, instanceID string) error {
	instance, err := s.GetInstance(instanceID)
	if err != nil {
		return err
	}

	if instance == nil || instance.ID == "" {
		return nil
	}
	mc := metrics.NewMetricPrometheusContext("server", "delete")
	err = servers.Delete(s.computeClient, instance.ID).ExtractErr()
	if mc.ObserveRequest(err) != nil {
		record.Warnf(eventObject, "FailedDeleteServer", "Failed to deleted server %s with id %s: %v", instance.Name, instance.ID, err)
		return err
	}

	err = util.PollImmediate(retryIntervalInstanceStatus, timeoutInstanceDelete, func() (bool, error) {
		i, err := s.GetInstance(instance.ID)
		if err != nil {
			return false, err
		}
		if i != nil {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		record.Warnf(eventObject, "FailedDeleteServer", "Failed to delete server %s with id %s: %v", instance.Name, instance.ID, err)
		return err
	}

	record.Eventf(eventObject, "SuccessfulDeleteServer", "Deleted server %s with id %s", instance.Name, instance.ID)
	return nil
}

func (s *Service) GetInstance(resourceID string) (instance *infrav1.Instance, err error) {
	if resourceID == "" {
		return nil, fmt.Errorf("resourceId should be specified to get detail")
	}
	mc := metrics.NewMetricPrometheusContext("server", "get")
	server, err := servers.Get(s.computeClient, resourceID).Extract()
	if mc.ObserveRequestIgnoreNotFound(err) != nil {
		if capoerrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get server %q detail failed: %v", resourceID, err)
	}
	i, err := serverToInstance(server)
	if err != nil {
		return nil, err
	}
	return i, err
}

func (s *Service) InstanceExists(name string) (instance *infrav1.Instance, err error) {
	var listOpts servers.ListOpts
	if name != "" {
		listOpts = servers.ListOpts{
			// The name parameter to /servers is a regular expression. Unless we
			// explicitly specify a whole string match this will be a substring
			// match.
			Name: fmt.Sprintf("^%s$", name),
		}
	} else {
		listOpts = servers.ListOpts{}
	}

	mc := metrics.NewMetricPrometheusContext("server", "list")
	allPages, err := servers.List(s.computeClient, listOpts).AllPages()
	if mc.ObserveRequest(err) != nil {
		return nil, fmt.Errorf("get server list: %v", err)
	}
	serverList, err := servers.ExtractServers(allPages)
	if err != nil {
		return nil, fmt.Errorf("extract server list: %v", err)
	}
	instanceList := []*infrav1.Instance{}
	for _, server := range serverList {
		server := server
		i, err := serverToInstance(&server)
		if err != nil {
			return nil, err
		}
		instanceList = append(instanceList, i)
	}
	if len(instanceList) == 0 {
		return nil, nil
	}
	return instanceList[0], nil
}

func isDuplicate(list []string, name string) bool {
	if len(list) == 0 {
		return false
	}
	for _, element := range list {
		if element == name {
			return true
		}
	}
	return false
}

func serverToInstance(v *servers.Server) (*infrav1.Instance, error) {
	if v == nil {
		return nil, nil
	}
	i := &infrav1.Instance{
		ID:         v.ID,
		Name:       v.Name,
		SSHKeyName: v.KeyName,
		State:      infrav1.InstanceState(v.Status),
	}
	addrMap, err := GetIPFromInstance(*v)
	if err != nil {
		return i, err
	}
	i.IP = addrMap["internal"]
	if addrMap["floating"] != "" {
		i.FloatingIP = addrMap["floating"]
	}
	return i, nil
}

func GetIPFromInstance(v servers.Server) (map[string]string, error) {
	addrMap := make(map[string]string)
	if v.AccessIPv4 != "" && net.ParseIP(v.AccessIPv4) != nil {
		addrMap["internal"] = v.AccessIPv4
		return addrMap, nil
	}
	type networkInterface struct {
		Address string  `json:"addr"`
		Version float64 `json:"version"`
		Type    string  `json:"OS-EXT-IPS:type"`
	}

	for _, b := range v.Addresses {
		list, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("extract IP from instance err: %v", err)
		}
		var networkList []interface{}
		err = json.Unmarshal(list, &networkList)
		if err != nil {
			return nil, fmt.Errorf("extract IP from instance err: %v", err)
		}
		for _, network := range networkList {
			var netInterface networkInterface
			b, _ := json.Marshal(network)
			err = json.Unmarshal(b, &netInterface)
			if err != nil {
				return nil, fmt.Errorf("extract IP from instance err: %v", err)
			}
			if netInterface.Version == 4.0 {
				if netInterface.Type == "floating" {
					addrMap["floating"] = netInterface.Address
				} else {
					addrMap["internal"] = netInterface.Address
				}
			}
		}
	}

	return addrMap, nil
}

// deduplicate takes a slice of input strings and filters out any duplicate
// string occurrences, for example making ["a", "b", "a", "c"] become ["a", "b",
// "c"].
func deduplicate(sequence []string) []string {
	var unique []string
	set := make(map[string]bool)

	for _, s := range sequence {
		if _, ok := set[s]; !ok {
			unique = append(unique, s)
			set[s] = true
		}
	}

	return unique
}

func getTimeout(name string, timeout int) time.Duration {
	if v := os.Getenv(name); v != "" {
		timeout, err := strconv.Atoi(v)
		if err == nil {
			return time.Duration(timeout)
		}
	}
	return time.Duration(timeout)
}
