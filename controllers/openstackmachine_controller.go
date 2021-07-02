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

package controllers

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	capierrors "sigs.k8s.io/cluster-api/errors"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/cloud/services/compute"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/cloud/services/loadbalancer"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/cloud/services/networking"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/cloud/services/provider"
)

// OpenStackMachineReconciler reconciles a OpenStackMachine object.
type OpenStackMachineReconciler struct {
	Client           client.Client
	Recorder         record.EventRecorder
	WatchFilterValue string
}

const (
	waitForClusterInfrastructureReadyDuration = 15 * time.Second
	waitForInstanceBecomeActiveToReconcile    = 60 * time.Second
)

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=openstackmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=openstackmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch

func (r *OpenStackMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the OpenStackMachine instance.
	openStackMachine := &infrav1.OpenStackMachine{}
	err := r.Client.Get(ctx, req.NamespacedName, openStackMachine)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log = log.WithValues("openStackMachine", openStackMachine.Name)

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(ctx, r.Client, openStackMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Machine Controller has not yet set OwnerRef")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("machine", machine.Name)

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		log.Info("Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	if annotations.IsPaused(cluster, openStackMachine) {
		log.Info("OpenStackMachine or linked Cluster is marked as paused. Won't reconcile")
		return ctrl.Result{}, nil
	}

	infraCluster, err := r.getInfraCluster(ctx, cluster, openStackMachine)
	if err != nil {
		return ctrl.Result{}, errors.New("error getting infra provider cluster")
	}
	if infraCluster == nil {
		log.Info("OpenStackCluster is not ready yet")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("openStackCluster", infraCluster.Name)

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(openStackMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always patch the openStackMachine when exiting this function so we can persist any OpenStackMachine changes.
	defer func() {
		if err := patchHelper.Patch(ctx, openStackMachine); err != nil {
			if reterr == nil {
				reterr = err
			}
		}
	}()

	// Handle deleted machines
	if !openStackMachine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, patchHelper, cluster, infraCluster, machine, openStackMachine)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, log, patchHelper, cluster, infraCluster, machine, openStackMachine)
}

func (r *OpenStackMachineReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(
			&infrav1.OpenStackMachine{},
			builder.WithPredicates(
				predicate.Funcs{
					// Avoid reconciling if the event triggering the reconciliation is related to incremental status updates
					UpdateFunc: func(e event.UpdateEvent) bool {
						oldMachine := e.ObjectOld.(*infrav1.OpenStackMachine).DeepCopy()
						newMachine := e.ObjectNew.(*infrav1.OpenStackMachine).DeepCopy()
						oldMachine.Status = infrav1.OpenStackMachineStatus{}
						newMachine.Status = infrav1.OpenStackMachineStatus{}
						oldMachine.ObjectMeta.ResourceVersion = ""
						newMachine.ObjectMeta.ResourceVersion = ""
						return !reflect.DeepEqual(oldMachine, newMachine)
					},
				},
			),
		).
		Watches(
			&source.Kind{Type: &clusterv1.Machine{}},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("OpenStackMachine"))),
		).
		Watches(
			&source.Kind{Type: &infrav1.OpenStackCluster{}},
			handler.EnqueueRequestsFromMapFunc(r.OpenStackClusterToOpenStackMachines(ctx)),
		).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Watches(
			&source.Kind{Type: &clusterv1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(r.requeueOpenStackMachinesForUnpausedCluster(ctx)),
			builder.WithPredicates(predicates.ClusterUnpausedAndInfrastructureReady(ctrl.LoggerFrom(ctx))),
		).
		Complete(r)
}

func (r *OpenStackMachineReconciler) reconcileDelete(ctx context.Context, logger logr.Logger, patchHelper *patch.Helper, cluster *clusterv1.Cluster, openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine) (ctrl.Result, error) {
	logger.Info("Reconciling Machine delete")

	clusterName := fmt.Sprintf("%s-%s", cluster.ObjectMeta.Namespace, cluster.Name)

	osProviderClient, clientOpts, err := provider.NewClientFromMachine(ctx, r.Client, openStackMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	computeService, err := compute.NewService(osProviderClient, clientOpts, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	networkingService, err := networking.NewService(osProviderClient, clientOpts, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	loadBalancerService, err := loadbalancer.NewService(osProviderClient, clientOpts, logger)
	if err != nil {
		return ctrl.Result{}, err
	}
	if openStackCluster.Spec.ManagedAPIServerLoadBalancer {
		err = loadBalancerService.DeleteLoadBalancerMember(openStackCluster, machine, openStackMachine, clusterName)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	instance, err := computeService.InstanceExists(openStackMachine.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	if instance == nil {
		logger.Info("Skipped deleting machine that is already deleted")
		controllerutil.RemoveFinalizer(openStackMachine, infrav1.MachineFinalizer)
		if err := patchHelper.Patch(ctx, openStackMachine); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if instance.Name != "" {
		if err = computeService.DeleteInstance(openStackMachine, instance.Name); err != nil {
			handleUpdateMachineError(logger, openStackMachine, errors.Errorf("error deleting Openstack instance: %v", err))
			return ctrl.Result{}, nil
		}
	}

	if !openStackCluster.Spec.ManagedAPIServerLoadBalancer && util.IsControlPlaneMachine(machine) && openStackCluster.Spec.APIServerFloatingIP == "" && instance.FloatingIP != "" {
		if err = networkingService.DeleteFloatingIP(openStackCluster, instance.FloatingIP); err != nil {
			handleUpdateMachineError(logger, openStackMachine, errors.Errorf("error deleting Openstack floating IP: %v", err))
			return ctrl.Result{}, nil
		}
	}

	controllerutil.RemoveFinalizer(openStackMachine, infrav1.MachineFinalizer)
	logger.Info("Reconciled Machine delete successfully")
	if err := patchHelper.Patch(ctx, openStackMachine); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *OpenStackMachineReconciler) reconcileNormal(ctx context.Context, logger logr.Logger, patchHelper *patch.Helper, cluster *clusterv1.Cluster, openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine) (_ ctrl.Result, reterr error) {
	// If the OpenStackMachine is in an error state, return early.
	if openStackMachine.Status.FailureReason != nil || openStackMachine.Status.FailureMessage != nil {
		logger.Info("Error state detected, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// If the OpenStackMachine doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(openStackMachine, infrav1.MachineFinalizer)
	// Register the finalizer immediately to avoid orphaning OpenStack resources on delete
	if err := patchHelper.Patch(ctx, openStackMachine); err != nil {
		return ctrl.Result{}, err
	}

	if !cluster.Status.InfrastructureReady {
		logger.Info("Cluster infrastructure is not ready yet, requeuing machine")
		return ctrl.Result{RequeueAfter: waitForClusterInfrastructureReadyDuration}, nil
	}

	// Make sure bootstrap data is available and populated.
	if machine.Spec.Bootstrap.DataSecretName == nil {
		logger.Info("Bootstrap data secret reference is not yet available")
		return ctrl.Result{}, nil
	}
	userData, err := r.getBootstrapData(machine, openStackMachine)
	if err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Reconciling Machine")

	clusterName := fmt.Sprintf("%s-%s", cluster.ObjectMeta.Namespace, cluster.Name)

	osProviderClient, clientOpts, err := provider.NewClientFromMachine(ctx, r.Client, openStackMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	computeService, err := compute.NewService(osProviderClient, clientOpts, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	networkingService, err := networking.NewService(osProviderClient, clientOpts, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	instance, err := r.getOrCreate(logger, cluster, openStackCluster, machine, openStackMachine, computeService, userData)
	if err != nil {
		handleUpdateMachineError(logger, openStackMachine, errors.Errorf("OpenStack instance cannot be created: %v", err))
		return ctrl.Result{}, err
	}

	// Set an error message if we couldn't find the instance.
	if instance == nil {
		handleUpdateMachineError(logger, openStackMachine, errors.New("OpenStack instance cannot be found"))
		return ctrl.Result{}, nil
	}

	// TODO(sbueringer) From CAPA: TODO(ncdc): move this validation logic into a validating webhook (for us: create validation logic in webhook)

	openStackMachine.Spec.ProviderID = pointer.StringPtr(fmt.Sprintf("openstack:///%s", instance.ID))
	openStackMachine.Spec.InstanceID = pointer.StringPtr(instance.ID)

	openStackMachine.Status.InstanceState = &instance.State

	address := []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: instance.IP}}
	if instance.FloatingIP != "" {
		address = append(address, []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: instance.FloatingIP}}...)
	}
	openStackMachine.Status.Addresses = address

	// TODO(sbueringer) From CAPA: TODO(vincepri): Remove this annotation when clusterctl is no longer relevant.
	if openStackMachine.Annotations == nil {
		openStackMachine.Annotations = map[string]string{}
	}
	openStackMachine.Annotations["cluster-api-provider-openstack"] = "true"

	switch instance.State {
	case infrav1.InstanceStateActive:
		logger.Info("Machine instance is ACTIVE", "instance-id", instance.ID)
		openStackMachine.Status.Ready = true
	case infrav1.InstanceStateBuild:
		logger.Info("Machine instance is BUILD", "instance-id", instance.ID)
	case infrav1.InstanceStateError:
		// Error is unexpected, thus we report error and never retry
		handleUpdateMachineError(logger, openStackMachine, errors.Errorf("OpenStack instance state %q is unexpected", instance.State))
		return ctrl.Result{}, nil
	default:
		// The other state is normal (for example, migrating) but we don't want to proceed until it's ACTIVE
		// due to potential conflict or unexpected actions
		return ctrl.Result{RequeueAfter: waitForInstanceBecomeActiveToReconcile}, nil
	}

	if openStackCluster.Spec.ManagedAPIServerLoadBalancer {
		err = r.reconcileLoadBalancerMember(logger, osProviderClient, clientOpts, openStackCluster, machine, openStackMachine, instance, clusterName)
		if err != nil {
			handleUpdateMachineError(logger, openStackMachine, errors.Errorf("LoadBalancerMember cannot be reconciled: %v", err))
			return ctrl.Result{}, nil
		}
	} else if util.IsControlPlaneMachine(machine) {
		fp, err := networkingService.GetOrCreateFloatingIP(openStackCluster, openStackCluster.Spec.ControlPlaneEndpoint.Host)
		if err != nil {
			handleUpdateMachineError(logger, openStackMachine, errors.Errorf("Floating IP cannot be got or created: %v", err))
			return ctrl.Result{}, nil
		}
		err = computeService.AssociateFloatingIP(instance.ID, fp.FloatingIP)
		if err != nil {
			handleUpdateMachineError(logger, openStackMachine, errors.Errorf("Floating IP cannot be associated: %v", err))
			return ctrl.Result{}, nil
		}
	}

	logger.Info("Reconciled Machine create successfully")
	return ctrl.Result{}, nil
}

func (r *OpenStackMachineReconciler) getOrCreate(logger logr.Logger, cluster *clusterv1.Cluster, openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine, computeService *compute.Service, userData string) (*infrav1.Instance, error) {
	instance, err := computeService.InstanceExists(openStackMachine.Name)
	if err != nil {
		return nil, err
	}

	if instance == nil {
		logger.Info("Machine not exist, Creating Machine", "Machine", openStackMachine.Name)
		instance, err = computeService.CreateInstance(openStackCluster, machine, openStackMachine, cluster.Name, userData)
		if err != nil {
			return nil, errors.Errorf("error creating Openstack instance: %v", err)
		}
	}

	return instance, nil
}

func handleUpdateMachineError(logger logr.Logger, openstackMachine *infrav1.OpenStackMachine, message error) {
	err := capierrors.UpdateMachineError
	openstackMachine.Status.FailureReason = &err
	openstackMachine.Status.FailureMessage = pointer.StringPtr(message.Error())
	// TODO remove if this error is logged redundantly
	logger.Error(fmt.Errorf(string(err)), message.Error())
}

func (r *OpenStackMachineReconciler) reconcileLoadBalancerMember(logger logr.Logger, osProviderClient *gophercloud.ProviderClient, clientOpts *clientconfig.ClientOpts, openStackCluster *infrav1.OpenStackCluster, machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine, instance *infrav1.Instance, clusterName string) error {
	ip := instance.IP
	loadbalancerService, err := loadbalancer.NewService(osProviderClient, clientOpts, logger)
	if err != nil {
		return err
	}

	return loadbalancerService.ReconcileLoadBalancerMember(openStackCluster, machine, openStackMachine, clusterName, ip)
}

// OpenStackClusterToOpenStackMachine is a handler.ToRequestsFunc to be used to enqeue requests for reconciliation
// of OpenStackMachines.
func (r *OpenStackMachineReconciler) OpenStackClusterToOpenStackMachines(ctx context.Context) handler.MapFunc {
	log := ctrl.LoggerFrom(ctx)
	return func(o client.Object) []ctrl.Request {
		c, ok := o.(*infrav1.OpenStackCluster)
		if !ok {
			panic(fmt.Sprintf("Expected a OpenStackCluster but got a %T", o))
		}

		log := log.WithValues("objectMapper", "openStackClusterToOpenStackMachine", "namespace", c.Namespace, "openStackCluster", c.Name)

		// Don't handle deleted OpenStackClusters
		if !c.ObjectMeta.DeletionTimestamp.IsZero() {
			log.V(4).Info("OpenStackClusters has a deletion timestamp, skipping mapping.")
			return nil
		}

		cluster, err := util.GetOwnerCluster(context.TODO(), r.Client, c.ObjectMeta)
		switch {
		case apierrors.IsNotFound(err) || cluster == nil:
			log.V(4).Info("Cluster for OpenStackCluster not found, skipping mapping.")
			return nil
		case err != nil:
			log.Error(err, "Failed to get owning cluster, skipping mapping.")
			return nil
		}

		return r.requestsForCluster(log, cluster.Namespace, cluster.Name)
	}
}

func (r *OpenStackMachineReconciler) getBootstrapData(machine *clusterv1.Machine, openStackMachine *infrav1.OpenStackMachine) (string, error) {
	if machine.Spec.Bootstrap.DataSecretName == nil {
		return "", errors.New("error retrieving bootstrap data: linked Machine's bootstrap.dataSecretName is nil")
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Client.Get(context.TODO(), key, secret); err != nil {
		return "", errors.Wrapf(err, "failed to retrieve bootstrap data secret for Openstack Machine %s/%s", machine.Namespace, openStackMachine.Name)
	}

	value, ok := secret.Data["value"]
	if !ok {
		return "", errors.New("error retrieving bootstrap data: secret value key is missing")
	}

	return base64.StdEncoding.EncodeToString(value), nil
}

func (r *OpenStackMachineReconciler) requeueOpenStackMachinesForUnpausedCluster(ctx context.Context) handler.MapFunc {
	log := ctrl.LoggerFrom(ctx)
	return func(o client.Object) []ctrl.Request {
		c, ok := o.(*clusterv1.Cluster)
		if !ok {
			panic(fmt.Sprintf("Expected a Cluster but got a %T", o))
		}

		log := log.WithValues("objectMapper", "clusterToOpenStackMachine", "namespace", c.Namespace, "cluster", c.Name)

		// Don't handle deleted clusters
		if !c.ObjectMeta.DeletionTimestamp.IsZero() {
			log.V(4).Info("Cluster has a deletion timestamp, skipping mapping.")
			return nil
		}

		return r.requestsForCluster(log, c.Namespace, c.Name)
	}
}

func (r *OpenStackMachineReconciler) requestsForCluster(log logr.Logger, namespace, name string) []ctrl.Request {
	labels := map[string]string{clusterv1.ClusterLabelName: name}
	machineList := &clusterv1.MachineList{}
	if err := r.Client.List(context.TODO(), machineList, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
		log.Error(err, "Failed to get owned Machines, skipping mapping.")
		return nil
	}

	result := make([]ctrl.Request, 0, len(machineList.Items))
	for _, m := range machineList.Items {
		if m.Spec.InfrastructureRef.Name != "" {
			result = append(result, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.InfrastructureRef.Name}})
		}
	}
	return result
}

func (r *OpenStackMachineReconciler) getInfraCluster(ctx context.Context, cluster *clusterv1.Cluster, openStackMachine *infrav1.OpenStackMachine) (*infrav1.OpenStackCluster, error) {
	openStackCluster := &infrav1.OpenStackCluster{}
	openStackClusterName := client.ObjectKey{
		Namespace: openStackMachine.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Client.Get(ctx, openStackClusterName, openStackCluster); err != nil {
		return nil, err
	}
	return openStackCluster, nil
}
