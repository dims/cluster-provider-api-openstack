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

package loadbalancer

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/monitors"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/pools"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util"

	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/record"
)

const (
	networkPrefix   string = "k8s-clusterapi"
	kubeapiLBSuffix string = "kubeapi"
)

func (s *Service) ReconcileLoadBalancer(openStackCluster *infrav1.OpenStackCluster, clusterName string) error {
	loadBalancerName := getLoadBalancerName(clusterName)
	s.logger.Info("Reconciling load balancer", "name", loadBalancerName)

	// lb
	lb, err := checkIfLbExists(s.loadbalancerClient, loadBalancerName)
	if err != nil {
		return err
	}
	if lb == nil {
		s.logger.Info("Creating load balancer", "name", loadBalancerName)
		lbCreateOpts := loadbalancers.CreateOpts{
			Name:        loadBalancerName,
			VipSubnetID: openStackCluster.Status.Network.Subnet.ID,
		}

		lb, err = loadbalancers.Create(s.loadbalancerClient, lbCreateOpts).Extract()
		if err != nil {
			record.Warnf(openStackCluster, "FailedCreateLoadBalancer", "Failed to create load balancer %s: %v", loadBalancerName, err)
			return err
		}
		record.Eventf(openStackCluster, "SuccessfulCreateLoadBalancer", "Created load balancer %s with id %s", loadBalancerName, lb.ID)
	}
	if err := waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lb.ID); err != nil {
		return err
	}

	floatingIPAddress := openStackCluster.Spec.ControlPlaneEndpoint.Host
	if openStackCluster.Spec.APIServerFloatingIP != "" {
		floatingIPAddress = openStackCluster.Spec.APIServerFloatingIP
	}
	fp, err := s.networkingService.GetOrCreateFloatingIP(openStackCluster, floatingIPAddress)
	if err != nil {
		return err
	}
	if err = s.networkingService.AssociateFloatingIP(openStackCluster, fp, lb.VipPortID); err != nil {
		return err
	}

	// lb listener
	portList := []int{int(openStackCluster.Spec.ControlPlaneEndpoint.Port)}
	portList = append(portList, openStackCluster.Spec.APIServerLoadBalancerAdditionalPorts...)
	for _, port := range portList {
		lbPortObjectsName := fmt.Sprintf("%s-%d", loadBalancerName, port)

		listener, err := checkIfListenerExists(s.loadbalancerClient, lbPortObjectsName)
		if err != nil {
			return err
		}
		if listener == nil {
			s.logger.Info("Creating load balancer listener", "name", lbPortObjectsName)
			listenerCreateOpts := listeners.CreateOpts{
				Name:           lbPortObjectsName,
				Protocol:       "TCP",
				ProtocolPort:   port,
				LoadbalancerID: lb.ID,
			}
			listener, err = listeners.Create(s.loadbalancerClient, listenerCreateOpts).Extract()
			if err != nil {
				return fmt.Errorf("error creating listener: %s", err)
			}
		}
		if err := waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lb.ID); err != nil {
			return err
		}

		if err := waitForListener(s.logger, s.loadbalancerClient, listener.ID, "ACTIVE"); err != nil {
			return err
		}

		// lb pool
		pool, err := checkIfPoolExists(s.loadbalancerClient, lbPortObjectsName)
		if err != nil {
			return err
		}
		if pool == nil {
			s.logger.Info("Creating load balancer pool", "name", lbPortObjectsName)
			poolCreateOpts := pools.CreateOpts{
				Name:       lbPortObjectsName,
				Protocol:   "TCP",
				LBMethod:   pools.LBMethodRoundRobin,
				ListenerID: listener.ID,
			}
			pool, err = pools.Create(s.loadbalancerClient, poolCreateOpts).Extract()
			if err != nil {
				return fmt.Errorf("error creating pool: %s", err)
			}
		}
		if err := waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lb.ID); err != nil {
			return err
		}

		// lb monitor
		monitor, err := checkIfMonitorExists(s.loadbalancerClient, lbPortObjectsName)
		if err != nil {
			return err
		}
		if monitor == nil {
			s.logger.Info("Creating load balancer monitor", "name", lbPortObjectsName)
			monitorCreateOpts := monitors.CreateOpts{
				Name:       lbPortObjectsName,
				PoolID:     pool.ID,
				Type:       "TCP",
				Delay:      30,
				Timeout:    5,
				MaxRetries: 3,
			}
			_, err = monitors.Create(s.loadbalancerClient, monitorCreateOpts).Extract()
			if err != nil {
				return fmt.Errorf("error creating monitor: %s", err)
			}
		}
		if err = waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lb.ID); err != nil {
			return err
		}
	}

	openStackCluster.Status.Network.APIServerLoadBalancer = &infrav1.LoadBalancer{
		Name:       lb.Name,
		ID:         lb.ID,
		InternalIP: lb.VipAddress,
		IP:         fp.FloatingIP,
	}
	return nil
}

func (s *Service) ReconcileLoadBalancerMember(openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine, clusterName, ip string) error {
	if !util.IsControlPlaneMachine(machine) {
		return nil
	}

	if openStackCluster.Status.Network == nil {
		return errors.New("network is not yet available in openStackCluster.Status")
	}
	if openStackCluster.Status.Network.Subnet == nil {
		return errors.New("network.Subnet is not yet available in openStackCluster.Status")
	}
	if openStackCluster.Status.Network.APIServerLoadBalancer == nil {
		return errors.New("network.APIServerLoadBalancer is not yet available in openStackCluster.Status")
	}

	loadBalancerName := getLoadBalancerName(clusterName)
	s.logger.Info("Reconciling load balancer", "name", loadBalancerName)

	lbID := openStackCluster.Status.Network.APIServerLoadBalancer.ID
	portList := []int{int(openStackCluster.Spec.ControlPlaneEndpoint.Port)}
	portList = append(portList, openStackCluster.Spec.APIServerLoadBalancerAdditionalPorts...)
	for _, port := range portList {
		lbPortObjectsName := fmt.Sprintf("%s-%d", loadBalancerName, port)
		name := lbPortObjectsName + "-" + openStackMachine.Name

		pool, err := checkIfPoolExists(s.loadbalancerClient, lbPortObjectsName)
		if err != nil {
			return err
		}
		if pool == nil {
			return errors.New("load balancer pool does not exist yet")
		}

		lbMember, err := checkIfLbMemberExists(s.loadbalancerClient, pool.ID, name)
		if err != nil {
			return err
		}

		if lbMember != nil {
			// check if we have to recreate the LB Member
			if lbMember.Address == ip {
				// nothing to do return
				return nil
			}

			s.logger.Info("Deleting load balancer member (because the IP of the machine changed)", "name", name)

			// lb member changed so let's delete it so we can create it again with the correct IP
			err = waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lbID)
			if err != nil {
				return err
			}
			err = pools.DeleteMember(s.loadbalancerClient, pool.ID, lbMember.ID).ExtractErr()
			if err != nil {
				return fmt.Errorf("error deleting lbmember: %s", err)
			}
			err = waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lbID)
			if err != nil {
				return err
			}
		}

		s.logger.Info("Creating load balancer member", "name", name)

		// if we got to this point we should either create or re-create the lb member
		lbMemberOpts := pools.CreateMemberOpts{
			Name:         name,
			ProtocolPort: port,
			Address:      ip,
		}

		if err := waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lbID); err != nil {
			return err
		}
		if _, err := pools.CreateMember(s.loadbalancerClient, pool.ID, lbMemberOpts).Extract(); err != nil {
			return fmt.Errorf("error create lbmember: %s", err)
		}
		if err := waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lbID); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLoadBalancer deletes a load balancer identified by its name and all child objects (cascade).
func (s *Service) DeleteLoadBalancer(openStackCluster *infrav1.OpenStackCluster, loadBalancerName string) error {
	lb, err := checkIfLbExists(s.loadbalancerClient, loadBalancerName)
	if err != nil {
		return err
	}
	return s.deleteLoadBalancer(openStackCluster, lb)
}

// deleteLoadBalancer deletes the load balancer and all child objects (cascade).
func (s *Service) deleteLoadBalancer(openStackCluster *infrav1.OpenStackCluster, lb *loadbalancers.LoadBalancer) error {
	if lb == nil {
		// nothing to do
		return nil
	}

	deleteOpts := loadbalancers.DeleteOpts{
		Cascade: true,
	}
	s.logger.Info("Deleting load balancer", "id", lb.ID, "name", lb.Name, "cascade", deleteOpts.Cascade)
	err := loadbalancers.Delete(s.loadbalancerClient, lb.ID, deleteOpts).ExtractErr()
	if err != nil {
		record.Warnf(openStackCluster, "FailedDeleteLoadBalancer", "Failed to delete load balancer %s with id %s: %v", lb.Name, lb.ID, err)
		return err
	}

	record.Eventf(openStackCluster, "SuccessfulDeleteLoadBalancer", "Deleted load balancer %s with id %s", lb.Name, lb.ID)
	return nil
}

func (s *Service) DeleteLoadBalancerMember(openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine, clusterName string) error {
	if openStackMachine == nil || !util.IsControlPlaneMachine(machine) {
		return nil
	}

	loadBalancerName := getLoadBalancerName(clusterName)
	s.logger.Info("Reconciling load balancer", "name", loadBalancerName)

	lbID := openStackCluster.Status.Network.APIServerLoadBalancer.ID

	portList := []int{int(openStackCluster.Spec.ControlPlaneEndpoint.Port)}
	portList = append(portList, openStackCluster.Spec.APIServerLoadBalancerAdditionalPorts...)
	for _, port := range portList {
		lbPortObjectsName := fmt.Sprintf("%s-%d", loadBalancerName, port)
		name := lbPortObjectsName + "-" + openStackMachine.Name

		pool, err := checkIfPoolExists(s.loadbalancerClient, lbPortObjectsName)
		if err != nil {
			return err
		}
		if pool == nil {
			s.logger.Info("Load balancer pool does not exist", "name", lbPortObjectsName)
			continue
		}

		lbMember, err := checkIfLbMemberExists(s.loadbalancerClient, pool.ID, name)
		if err != nil {
			return err
		}

		if lbMember != nil {

			// lb member changed so let's delete it so we can create it again with the correct IP
			err = waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lbID)
			if err != nil {
				return err
			}
			err = pools.DeleteMember(s.loadbalancerClient, pool.ID, lbMember.ID).ExtractErr()
			if err != nil {
				return fmt.Errorf("error deleting load balancer member: %s", err)
			}
			err = waitForLoadBalancerActive(s.logger, s.loadbalancerClient, lbID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteOrphanedLoadBalancers deletes all remaining load balancers and all child objects within an OpenStack project.
// Orphaned load balancers are expected when deleting the cluster because the CCM manages additional load balancers.
func (s *Service) DeleteOrphanedLoadBalancers(openStackCluster *infrav1.OpenStackCluster) error {
	lbList, err := loadbalancers.List(s.loadbalancerClient, loadbalancers.ListOpts{ProjectID: s.projectID}).AllPages()
	if err != nil {
		return fmt.Errorf("error listing loadbalancers: %v", err)
	}

	orphanedLoadbalancers, err := loadbalancers.ExtractLoadBalancers(lbList)
	if err != nil {
		return fmt.Errorf("error extracting loadbalancers: %v", err)
	}

	for _, lb := range orphanedLoadbalancers {
		orphanedLoadbalancer := lb
		if err = s.deleteLoadBalancer(openStackCluster, &orphanedLoadbalancer); err != nil {
			return err
		}
	}
	return nil
}

func getLoadBalancerName(clusterName string) string {
	return fmt.Sprintf("%s-cluster-%s-%s", networkPrefix, clusterName, kubeapiLBSuffix)
}

func checkIfLbExists(client *gophercloud.ServiceClient, name string) (*loadbalancers.LoadBalancer, error) {
	allPages, err := loadbalancers.List(client, loadbalancers.ListOpts{Name: name}).AllPages()
	if err != nil {
		return nil, err
	}
	lbList, err := loadbalancers.ExtractLoadBalancers(allPages)
	if err != nil {
		return nil, err
	}
	if len(lbList) == 0 {
		return nil, nil
	}
	return &lbList[0], nil
}

func checkIfListenerExists(client *gophercloud.ServiceClient, name string) (*listeners.Listener, error) {
	allPages, err := listeners.List(client, listeners.ListOpts{Name: name}).AllPages()
	if err != nil {
		return nil, err
	}
	listenerList, err := listeners.ExtractListeners(allPages)
	if err != nil {
		return nil, err
	}
	if len(listenerList) == 0 {
		return nil, nil
	}
	return &listenerList[0], nil
}

func checkIfPoolExists(client *gophercloud.ServiceClient, name string) (*pools.Pool, error) {
	allPages, err := pools.List(client, pools.ListOpts{Name: name}).AllPages()
	if err != nil {
		return nil, err
	}
	poolList, err := pools.ExtractPools(allPages)
	if err != nil {
		return nil, err
	}
	if len(poolList) == 0 {
		return nil, nil
	}
	return &poolList[0], nil
}

func checkIfMonitorExists(client *gophercloud.ServiceClient, name string) (*monitors.Monitor, error) {
	allPages, err := monitors.List(client, monitors.ListOpts{Name: name}).AllPages()
	if err != nil {
		return nil, err
	}
	monitorList, err := monitors.ExtractMonitors(allPages)
	if err != nil {
		return nil, err
	}
	if len(monitorList) == 0 {
		return nil, nil
	}
	return &monitorList[0], nil
}

func checkIfLbMemberExists(client *gophercloud.ServiceClient, poolID, name string) (*pools.Member, error) {
	allPages, err := pools.ListMembers(client, poolID, pools.ListMembersOpts{Name: name}).AllPages()
	if err != nil {
		return nil, err
	}
	lbMemberList, err := pools.ExtractMembers(allPages)
	if err != nil {
		return nil, err
	}
	if len(lbMemberList) == 0 {
		return nil, nil
	}
	return &lbMemberList[0], nil
}

var backoff = wait.Backoff{
	Steps:    10,
	Duration: 30 * time.Second,
	Factor:   1.0,
	Jitter:   0.1,
}

// Possible LoadBalancer states are documented here: https://developer.openstack.org/api-ref/network/v2/?expanded=show-load-balancer-status-tree-detail#load-balancer-statuses
func waitForLoadBalancerActive(logger logr.Logger, client *gophercloud.ServiceClient, id string) error {
	logger.Info("Waiting for load balancer", "id", id, "targetStatus", "ACTIVE")
	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		lb, err := loadbalancers.Get(client, id).Extract()
		if err != nil {
			return false, err
		}
		return lb.ProvisioningStatus == "ACTIVE", nil
	})
}

func waitForListener(logger logr.Logger, client *gophercloud.ServiceClient, id, target string) error {
	logger.Info("Waiting for load balancer listener", "id", id, "targetStatus", target)
	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		_, err := listeners.Get(client, id).Extract()
		if err != nil {
			return false, err
		}
		// The listener resource has no Status attribute, so a successful Get is the best we can do
		return true, nil
	})
}
