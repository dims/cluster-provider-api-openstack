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

package v1alpha3

// OpenStackMachineTemplateResource describes the data needed to create a OpenStackMachine from a template.
type OpenStackMachineTemplateResource struct {
	// Spec is the specification of the desired behavior of the machine.
	Spec OpenStackMachineSpec `json:"spec"`
}

type ExternalRouterIPParam struct {
	// The FixedIP in the corresponding subnet
	FixedIP string `json:"fixedIP,omitempty"`
	// The subnet in which the FixedIP is used for the Gateway of this router
	Subnet SubnetParam `json:"subnet"`
}

type SecurityGroupParam struct {
	// Security Group UID
	UUID string `json:"uuid,omitempty"`
	// Security Group name
	Name string `json:"name,omitempty"`
	// Filters used to query security groups in openstack
	Filter SecurityGroupFilter `json:"filter,omitempty"`
}

type SecurityGroupFilter struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	TenantID    string `json:"tenantId,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Marker      string `json:"marker,omitempty"`
	SortKey     string `json:"sortKey,omitempty"`
	SortDir     string `json:"sortDir,omitempty"`
	Tags        string `json:"tags,omitempty"`
	TagsAny     string `json:"tagsAny,omitempty"`
	NotTags     string `json:"notTags,omitempty"`
	NotTagsAny  string `json:"notTagsAny,omitempty"`
}

type NetworkParam struct {
	// The UUID of the network. Required if you omit the port attribute.
	UUID string `json:"uuid,omitempty"`
	// A fixed IPv4 address for the NIC.
	FixedIP string `json:"fixedIp,omitempty"`
	// Filters for optional network query
	Filter Filter `json:"filter,omitempty"`
	// Subnet within a network to use
	Subnets []SubnetParam `json:"subnets,omitempty"`
}

type Filter struct {
	Status       string `json:"status,omitempty"`
	Name         string `json:"name,omitempty"`
	Description  string `json:"description,omitempty"`
	AdminStateUp *bool  `json:"adminStateUp,omitempty"`
	TenantID     string `json:"tenantId,omitempty"`
	ProjectID    string `json:"projectId,omitempty"`
	Shared       *bool  `json:"shared,omitempty"`
	ID           string `json:"id,omitempty"`
	Marker       string `json:"marker,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	SortKey      string `json:"sortKey,omitempty"`
	SortDir      string `json:"sortDir,omitempty"`
	Tags         string `json:"tags,omitempty"`
	TagsAny      string `json:"tagsAny,omitempty"`
	NotTags      string `json:"notTags,omitempty"`
	NotTagsAny   string `json:"notTagsAny,omitempty"`
}

type SubnetParam struct {
	// The UUID of the network. Required if you omit the port attribute.
	UUID string `json:"uuid,omitempty"`

	// Filters for optional network query
	Filter SubnetFilter `json:"filter,omitempty"`
}

type SubnetFilter struct {
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	EnableDHCP      *bool  `json:"enableDhcp,omitempty"`
	NetworkID       string `json:"networkId,omitempty"`
	TenantID        string `json:"tenantId,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	IPVersion       int    `json:"ipVersion,omitempty"`
	GatewayIP       string `json:"gateway_ip,omitempty"`
	CIDR            string `json:"cidr,omitempty"`
	IPv6AddressMode string `json:"ipv6AddressMode,omitempty"`
	IPv6RAMode      string `json:"ipv6RaMode,omitempty"`
	ID              string `json:"id,omitempty"`
	SubnetPoolID    string `json:"subnetpoolId,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Marker          string `json:"marker,omitempty"`
	SortKey         string `json:"sortKey,omitempty"`
	SortDir         string `json:"sortDir,omitempty"`
	Tags            string `json:"tags,omitempty"`
	TagsAny         string `json:"tagsAny,omitempty"`
	NotTags         string `json:"notTags,omitempty"`
	NotTagsAny      string `json:"notTagsAny,omitempty"`
}

type Instance struct {
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name,omitempty"`
	Trunk          bool              `json:"trunk,omitempty"`
	FailureDomain  string            `json:"failureDomain,omitempty"`
	SecurityGroups *[]string         `json:"securigyGroups,omitempty"`
	Networks       *[]Network        `json:"networks,omitempty"`
	Subnet         string            `json:"subnet,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Image          string            `json:"image,omitempty"`
	Flavor         string            `json:"flavor,omitempty"`
	SSHKeyName     string            `json:"sshKeyName,omitempty"`
	UserData       string            `json:"userData,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ConfigDrive    *bool             `json:"configDrive,omitempty"`
	RootVolume     *RootVolume       `json:"rootVolume,omitempty"`
	ServerGroupID  string            `json:"serverGroupID,omitempty"`
	State          InstanceState     `json:"state,omitempty"`
	IP             string            `json:"ip,omitempty"`
	FloatingIP     string            `json:"floatingIP,omitempty"`
}

type RootVolume struct {
	SourceType string `json:"sourceType,omitempty"`
	SourceUUID string `json:"sourceUUID,omitempty"`
	DeviceType string `json:"deviceType,omitempty"`
	Size       int    `json:"diskSize,omitempty"`
}

// Network represents basic information about the associated OpenStach Neutron Network.
type Network struct {
	Name string `json:"name"`
	ID   string `json:"id"`

	//+optional
	Tags []string `json:"tags,omitempty"`

	Subnet *Subnet `json:"subnet,omitempty"`
	Router *Router `json:"router,omitempty"`

	// Be careful when using APIServerLoadBalancer, because this field is optional and therefore not
	// set in all cases
	APIServerLoadBalancer *LoadBalancer `json:"apiServerLoadBalancer,omitempty"`
}

// Subnet represents basic information about the associated OpenStack Neutron Subnet.
type Subnet struct {
	Name string `json:"name"`
	ID   string `json:"id"`

	CIDR string `json:"cidr"`

	//+optional
	Tags []string `json:"tags,omitempty"`
}

// Router represents basic information about the associated OpenStack Neutron Router.
type Router struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	//+optional
	Tags []string `json:"tags,omitempty"`
}

// LoadBalancer represents basic information about the associated OpenStack LoadBalancer.
type LoadBalancer struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	IP         string `json:"ip"`
	InternalIP string `json:"internalIP"`
}

// SecurityGroup represents the basic information of the associated
// OpenStack Neutron Security Group.
type SecurityGroup struct {
	Name  string              `json:"name"`
	ID    string              `json:"id"`
	Rules []SecurityGroupRule `json:"rules"`
}

// SecurityGroupRule represent the basic information of the associated OpenStack
// Security Group Role.
type SecurityGroupRule struct {
	Description     string `json:"description"`
	ID              string `json:"name"`
	Direction       string `json:"direction"`
	EtherType       string `json:"etherType"`
	SecurityGroupID string `json:"securityGroupID"`
	PortRangeMin    int    `json:"portRangeMin"`
	PortRangeMax    int    `json:"portRangeMax"`
	Protocol        string `json:"protocol"`
	RemoteGroupID   string `json:"remoteGroupID"`
	RemoteIPPrefix  string `json:"remoteIPPrefix"`
}

// Equal checks if two SecurityGroupRules are the same.
func (r SecurityGroupRule) Equal(x SecurityGroupRule) bool {
	return (r.Direction == x.Direction &&
		r.Description == x.Description &&
		r.EtherType == x.EtherType &&
		r.PortRangeMin == x.PortRangeMin &&
		r.PortRangeMax == x.PortRangeMax &&
		r.Protocol == x.Protocol &&
		r.RemoteGroupID == x.RemoteGroupID &&
		r.RemoteIPPrefix == x.RemoteIPPrefix)
}

// InstanceState describes the state of an OpenStack instance.
type InstanceState string

var (
	InstanceStateBuild = InstanceState("BUILD")

	InstanceStateActive = InstanceState("ACTIVE")

	InstanceStateError = InstanceState("ERROR")

	InstanceStateStopped = InstanceState("STOPPED")

	InstanceStateShutoff = InstanceState("SHUTOFF")
)

// Bastion represents basic information about the bastion node.
type Bastion struct {
	//+optional
	Enabled bool `json:"enabled"`

	// Instance for the bastion itself
	Instance OpenStackMachineSpec `json:"instance,omitempty"`

	//+optional
	AvailabilityZone string `json:"availabilityZone,omitempty"`
}
