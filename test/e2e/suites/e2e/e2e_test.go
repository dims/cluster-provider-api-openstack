// +build e2e

/*
Copyright 2021 The Kubernetes Authors.

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

package e2e

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachinerytypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha4"
	"sigs.k8s.io/cluster-api/controllers/noderefutil"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-openstack/test/e2e/shared"
)

var _ = Describe("e2e tests", func() {
	var (
		namespace *corev1.Namespace
		ctx       context.Context
		specName  = "e2e"
	)

	BeforeEach(func() {
		Expect(e2eCtx.Environment.BootstrapClusterProxy).ToNot(BeNil(), "Invalid argument. BootstrapClusterProxy can't be nil")
		ctx = context.TODO()
		// Setup a Namespace where to host objects for this spec and create a watcher for the namespace events.
		namespace = shared.SetupSpecNamespace(ctx, specName, e2eCtx)
		Expect(e2eCtx.E2EConfig).ToNot(BeNil(), "Invalid argument. e2eConfig can't be nil when calling %s spec", specName)
		Expect(e2eCtx.E2EConfig.Variables).To(HaveKey(shared.KubernetesVersion))
		shared.SetEnvVar("USE_CI_ARTIFACTS", "true", false)
	})

	Describe("Workload cluster (default)", func() {
		It("It should be creatable and deletable", func() {
			shared.Byf("Creating a cluster")
			clusterName := fmt.Sprintf("cluster-%s", namespace.Name)
			configCluster := defaultConfigCluster(clusterName, namespace.Name)
			configCluster.ControlPlaneMachineCount = pointer.Int64Ptr(3)
			configCluster.WorkerMachineCount = pointer.Int64Ptr(1)
			configCluster.Flavor = shared.FlavorDefault
			md := createCluster(ctx, configCluster, specName)

			workerMachines := framework.GetMachinesByMachineDeployments(ctx, framework.GetMachinesByMachineDeploymentsInput{
				Lister:            e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				ClusterName:       clusterName,
				Namespace:         namespace.Name,
				MachineDeployment: *md[0],
			})
			controlPlaneMachines := framework.GetControlPlaneMachinesByCluster(ctx, framework.GetControlPlaneMachinesByClusterInput{
				Lister:      e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				ClusterName: clusterName,
				Namespace:   namespace.Name,
			})
			Expect(len(workerMachines)).To(Equal(1))
			Expect(len(controlPlaneMachines)).To(Equal(3))
		})
	})

	Describe("Workload cluster (external cloud provider)", func() {
		It("It should be creatable and deletable", func() {
			shared.Byf("Creating a cluster")
			clusterName := fmt.Sprintf("cluster-%s", namespace.Name)
			configCluster := defaultConfigCluster(clusterName, namespace.Name)
			configCluster.ControlPlaneMachineCount = pointer.Int64Ptr(1)
			configCluster.WorkerMachineCount = pointer.Int64Ptr(1)
			configCluster.Flavor = shared.FlavorExternalCloudProvider
			md := createCluster(ctx, configCluster, specName)

			workerMachines := framework.GetMachinesByMachineDeployments(ctx, framework.GetMachinesByMachineDeploymentsInput{
				Lister:            e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				ClusterName:       clusterName,
				Namespace:         namespace.Name,
				MachineDeployment: *md[0],
			})
			controlPlaneMachines := framework.GetControlPlaneMachinesByCluster(ctx, framework.GetControlPlaneMachinesByClusterInput{
				Lister:      e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				ClusterName: clusterName,
				Namespace:   namespace.Name,
			})
			Expect(len(workerMachines)).To(Equal(1))
			Expect(len(controlPlaneMachines)).To(Equal(1))

			shared.Byf("Waiting for worker nodes to be in Running phase")
			statusChecks := []framework.MachineStatusCheck{framework.MachinePhaseCheck(string(clusterv1.MachinePhaseRunning))}
			machineStatusInput := framework.WaitForMachineStatusCheckInput{
				Getter:       e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				Machine:      &workerMachines[0],
				StatusChecks: statusChecks,
			}
			framework.WaitForMachineStatusCheck(ctx, machineStatusInput, e2eCtx.E2EConfig.GetIntervals(specName, "wait-machine-status")...)

			workloadCluster := e2eCtx.Environment.BootstrapClusterProxy.GetWorkloadCluster(ctx, namespace.Name, clusterName)

			waitForDaemonSetRunning(ctx, workloadCluster.GetClient(), "kube-system", "openstack-cloud-controller-manager")

			waitForNodesReadyWithoutCCMTaint(ctx, workloadCluster.GetClient(), 2)


			workloadCluster := e2eCtx.Environment.BootstrapClusterProxy.GetWorkloadCluster(ctx, namespace.Name, clusterName)

                        framework.CreateNamespaceAndWatchEvents(ctx, framework.CreateNamespaceAndWatchEventsInput{
                                Creator:   workloadCluster.GetClient(),
                                ClientSet: workloadCluster.GetClientSet(),
                                Name:      namespace.Name,
                                LogFolder: filepath.Join(e2eCtx.Settings.ArtifactFolder, "clusters", workloadCluster.GetName()),
                        })

                        shared.Byf("Ensuring DaemonSet %s is running", clusterName)
                        shared.Byf("Ensuring DaemonSet %s is running", namespace.Name)
                        By("Initializing the workload cluster")
                        clusterctl.InitManagementClusterAndWatchControllerLogs(ctx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
                                ClusterProxy:            workloadCluster,
                                ClusterctlConfigPath:    e2eCtx.Environment.ClusterctlConfigPath,
                                InfrastructureProviders: e2eCtx.E2EConfig.InfrastructureProviders(),
                                LogFolder:               filepath.Join(e2eCtx.Settings.ArtifactFolder, "clusters", workloadCluster.GetName()),
                        }, e2eCtx.E2EConfig.GetIntervals(workloadCluster.GetName(), "wait-controllers")...)

                        By("Ensure API servers are stable before doing move")
		})
	})

	Describe("Workload cluster (without lb)", func() {
		It("Should create port(s) with custom options", func() {
			shared.Byf("Creating a cluster")
			clusterName := fmt.Sprintf("cluster-%s", namespace.Name)
			configCluster := defaultConfigCluster(clusterName, namespace.Name)
			configCluster.ControlPlaneMachineCount = pointer.Int64Ptr(1)
			configCluster.WorkerMachineCount = pointer.Int64Ptr(1)
			configCluster.Flavor = shared.FlavorDefault
			_ = createCluster(ctx, configCluster, specName)

			shared.Byf("Creating MachineDeployment with custom port options")
			md3Name := clusterName + "-md-3"
			customPortOptions := &[]infrav1.PortOpts{
				{Description: "primary"},
			}

			// Note that as the bootstrap config does not have cloud.conf, the node will not be added to the cluster.
			// We still expect the port for the machine to be created.
			framework.CreateMachineDeployment(ctx, framework.CreateMachineDeploymentInput{
				Creator:                 e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				MachineDeployment:       makeMachineDeployment(namespace.Name, md3Name, clusterName, "", 1),
				BootstrapConfigTemplate: makeJoinBootstrapConfigTemplate(namespace.Name, md3Name),
				InfraMachineTemplate:    makeOpenStackMachineTemplateWithPortOptions(namespace.Name, clusterName, md3Name, customPortOptions),
			})

			shared.Byf("Waiting for custom port to be created")
			var plist *[]ports.Port
			var err error
			Eventually(func() int {
				plist, err = shared.DumpOpenStackPorts(e2eCtx, ports.ListOpts{Description: "primary"})
				Expect(err).To(BeNil())
				return len(*plist)
			}, e2eCtx.E2EConfig.GetIntervals(specName, "wait-worker-nodes")...).Should(Equal(1))

			port := (*plist)[0]
			Expect(port.Description).To(Equal("primary"))
		})
		It("It should be creatable and deletable", func() {
			shared.Byf("Creating a cluster")
			clusterName := fmt.Sprintf("cluster-%s", namespace.Name)
			configCluster := defaultConfigCluster(clusterName, namespace.Name)
			configCluster.ControlPlaneMachineCount = pointer.Int64Ptr(1)
			configCluster.WorkerMachineCount = pointer.Int64Ptr(1)
			configCluster.Flavor = shared.FlavorWithoutLB
			md := createCluster(ctx, configCluster, specName)

			workerMachines := framework.GetMachinesByMachineDeployments(ctx, framework.GetMachinesByMachineDeploymentsInput{
				Lister:            e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				ClusterName:       clusterName,
				Namespace:         namespace.Name,
				MachineDeployment: *md[0],
			})
			controlPlaneMachines := framework.GetControlPlaneMachinesByCluster(ctx, framework.GetControlPlaneMachinesByClusterInput{
				Lister:      e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				ClusterName: clusterName,
				Namespace:   namespace.Name,
			})
			Expect(len(workerMachines)).To(Equal(1))
			Expect(len(controlPlaneMachines)).To(Equal(1))

			workloadCluster := e2eCtx.Environment.BootstrapClusterProxy.GetWorkloadCluster(ctx, namespace.Name, clusterName)

			framework.CreateNamespaceAndWatchEvents(ctx, framework.CreateNamespaceAndWatchEventsInput{
				Creator:   workloadCluster.GetClient(),
				ClientSet: workloadCluster.GetClientSet(),
				Name:      namespace.Name,
				LogFolder: filepath.Join(e2eCtx.Settings.ArtifactFolder, "clusters", workloadCluster.GetName()),
			})

			shared.Byf("Ensuring DaemonSet %s is running", clusterName)
			shared.Byf("Ensuring DaemonSet %s is running", namespace.Name)
			By("Initializing the workload cluster")
			clusterctl.InitManagementClusterAndWatchControllerLogs(ctx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
				ClusterProxy:            workloadCluster,
				ClusterctlConfigPath:    e2eCtx.Environment.ClusterctlConfigPath,
				InfrastructureProviders: e2eCtx.E2EConfig.InfrastructureProviders(),
				LogFolder:               filepath.Join(e2eCtx.Settings.ArtifactFolder, "clusters", workloadCluster.GetName()),
			}, e2eCtx.E2EConfig.GetIntervals(workloadCluster.GetName(), "wait-controllers")...)

			By("Ensure API servers are stable before doing move")
			// Nb. This check was introduced to prevent doing move to self-hosted in an aggressive way and thus avoid flakes.
			// More specifically, we were observing the test failing to get objects from the API server during move, so we
			// are now testing the API servers are stable before starting move.
			Consistently(func() error {
				kubeSystem := &corev1.Namespace{}
				return e2eCtx.Environment.BootstrapClusterProxy.GetClient().Get(ctx, crclient.ObjectKey{Name: "kube-system"}, kubeSystem)
			}, "5s", "100ms").Should(BeNil(), "Failed to assert bootstrap API server stability")
			Consistently(func() error {
				kubeSystem := &corev1.Namespace{}
				return e2eCtx.Environment.BootstrapClusterProxy.GetClient().Get(ctx, crclient.ObjectKey{Name: "kube-system"}, kubeSystem)
			}, "5s", "100ms").Should(BeNil(), "Failed to assert self-hosted API server stability")

			By("Moving the cluster to self hosted")
			clusterctl.Move(ctx, clusterctl.MoveInput{
				LogFolder:            filepath.Join(e2eCtx.Settings.ArtifactFolder, "clusters", e2eCtx.Environment.BootstrapClusterProxy.GetName()),
				ClusterctlConfigPath: e2eCtx.Environment.ClusterctlConfigPath,
				FromKubeconfigPath:   e2eCtx.Environment.BootstrapClusterProxy.GetKubeconfigPath(),
				Namespace:            namespace.Name,
				ToKubeconfigPath:     workloadCluster.GetKubeconfigPath(),
			})
		})
	})

	Describe("MachineDeployment misconfigurations", func() {
		It("Should fail to create MachineDeployment with invalid subnet or invalid availability zone", func() {
			shared.Byf("Creating a cluster")
			clusterName := fmt.Sprintf("cluster-%s", namespace.Name)
			configCluster := defaultConfigCluster(clusterName, namespace.Name)
			configCluster.ControlPlaneMachineCount = pointer.Int64Ptr(1)
			configCluster.WorkerMachineCount = pointer.Int64Ptr(0)
			configCluster.Flavor = shared.FlavorDefault
			_ = createCluster(ctx, configCluster, specName)

			shared.Byf("Creating Machine Deployment with invalid subnet id")
			md1Name := clusterName + "-md-1"
			framework.CreateMachineDeployment(ctx, framework.CreateMachineDeploymentInput{
				Creator:                 e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				MachineDeployment:       makeMachineDeployment(namespace.Name, md1Name, clusterName, "", 1),
				BootstrapConfigTemplate: makeJoinBootstrapConfigTemplate(namespace.Name, md1Name),
				InfraMachineTemplate:    makeOpenStackMachineTemplate(namespace.Name, clusterName, md1Name, "invalid-subnet"),
			})

			shared.Byf("Looking for failure event to be reported")
			Eventually(func() bool {
				eventList := getEvents(namespace.Name)
				subnetError := "Failed to create server: no ports with fixed IPs found on Subnet \"invalid-subnet\""
				return isErrorEventExists(namespace.Name, md1Name, "FailedCreateServer", subnetError, eventList)
			}, e2eCtx.E2EConfig.GetIntervals(specName, "wait-worker-nodes")...).Should(BeTrue())

			shared.Byf("Creating Machine Deployment in an invalid Availability Zone")
			md2Name := clusterName + "-md-2"
			framework.CreateMachineDeployment(ctx, framework.CreateMachineDeploymentInput{
				Creator:                 e2eCtx.Environment.BootstrapClusterProxy.GetClient(),
				MachineDeployment:       makeMachineDeployment(namespace.Name, md2Name, clusterName, "invalid-az", 1),
				BootstrapConfigTemplate: makeJoinBootstrapConfigTemplate(namespace.Name, md2Name),
				InfraMachineTemplate:    makeOpenStackMachineTemplate(namespace.Name, clusterName, md2Name, ""),
			})

			shared.Byf("Looking for failure event to be reported")
			Eventually(func() bool {
				eventList := getEvents(namespace.Name)
				azError := "The requested availability zone is not available"
				return isErrorEventExists(namespace.Name, md2Name, "FailedCreateServer", azError, eventList)
			}, e2eCtx.E2EConfig.GetIntervals(specName, "wait-worker-nodes")...).Should(BeTrue())
		})
	})

	AfterEach(func() {
		shared.SetEnvVar("USE_CI_ARTIFACTS", "false", false)
		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		shared.DumpSpecResourcesAndCleanup(ctx, specName, namespace, e2eCtx)
	})
})

func createCluster(ctx context.Context, configCluster clusterctl.ConfigClusterInput, specName string) []*clusterv1.MachineDeployment {
	result := &clusterctl.ApplyClusterTemplateAndWaitResult{}
	clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
		ClusterProxy:                 e2eCtx.Environment.BootstrapClusterProxy,
		ConfigCluster:                configCluster,
		WaitForClusterIntervals:      e2eCtx.E2EConfig.GetIntervals(specName, "wait-cluster"),
		WaitForControlPlaneIntervals: e2eCtx.E2EConfig.GetIntervals(specName, "wait-control-plane"),
		WaitForMachineDeployments:    e2eCtx.E2EConfig.GetIntervals(specName, "wait-worker-nodes"),
	}, result)

	return result.MachineDeployments
}

func defaultConfigCluster(clusterName, namespace string) clusterctl.ConfigClusterInput {
	return clusterctl.ConfigClusterInput{
		LogFolder:              filepath.Join(e2eCtx.Settings.ArtifactFolder, "clusters", e2eCtx.Environment.BootstrapClusterProxy.GetName()),
		ClusterctlConfigPath:   e2eCtx.Environment.ClusterctlConfigPath,
		KubeconfigPath:         e2eCtx.Environment.BootstrapClusterProxy.GetKubeconfigPath(),
		InfrastructureProvider: clusterctl.DefaultInfrastructureProvider,
		Namespace:              namespace,
		ClusterName:            clusterName,
		KubernetesVersion:      e2eCtx.E2EConfig.GetVariable(shared.KubernetesVersion),
	}
}

func getEvents(namespace string) *corev1.EventList {
	eventsList := &corev1.EventList{}
	if err := e2eCtx.Environment.BootstrapClusterProxy.GetClient().List(context.TODO(), eventsList, crclient.InNamespace(namespace), crclient.MatchingLabels{}); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Got error while fetching events of namespace: %s, %s \n", namespace, err.Error())
	}

	return eventsList
}

func isErrorEventExists(namespace, machineDeploymentName, eventReason, errorMsg string, eList *corev1.EventList) bool {
	ctrlClient := e2eCtx.Environment.BootstrapClusterProxy.GetClient()
	machineDeployment := &clusterv1.MachineDeployment{}
	if err := ctrlClient.Get(context.TODO(), apimachinerytypes.NamespacedName{Namespace: namespace, Name: machineDeploymentName}, machineDeployment); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Got error while getting machinedeployment %s \n", machineDeploymentName)
		return false
	}

	selector, err := metav1.LabelSelectorAsMap(&machineDeployment.Spec.Selector)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Got error while reading lables of machinedeployment: %s, %s \n", machineDeploymentName, err.Error())
		return false
	}

	openStackMachineList := &infrav1.OpenStackMachineList{}
	if err := ctrlClient.List(context.TODO(), openStackMachineList, crclient.InNamespace(namespace), crclient.MatchingLabels(selector)); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Got error while getting openstackmachines of machinedeployment: %s, %s \n", machineDeploymentName, err.Error())
		return false
	}

	eventMachinesCnt := 0
	for _, openStackMachine := range openStackMachineList.Items {
		for _, event := range eList.Items {
			if strings.Contains(event.Name, openStackMachine.Name) && event.Reason == eventReason && strings.Contains(event.Message, errorMsg) {
				eventMachinesCnt++
				break
			}
		}
	}
	return len(openStackMachineList.Items) == eventMachinesCnt
}

func makeOpenStackMachineTemplate(namespace, clusterName, name string, subnetID string) *infrav1.OpenStackMachineTemplate {
	return &infrav1.OpenStackMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: infrav1.OpenStackMachineTemplateSpec{
			Template: infrav1.OpenStackMachineTemplateResource{
				Spec: infrav1.OpenStackMachineSpec{
					Flavor:     e2eCtx.E2EConfig.GetVariable(shared.OpenStackNodeMachineFlavor),
					Image:      e2eCtx.E2EConfig.GetVariable(shared.OpenStackImageName),
					SSHKeyName: shared.DefaultSSHKeyPairName,
					CloudName:  e2eCtx.E2EConfig.GetVariable(shared.OpenStackCloud),
					IdentityRef: &infrav1.OpenStackIdentityReference{
						Kind: "Secret",
						Name: fmt.Sprintf("%s-cloud-config", clusterName),
					},
					Subnet: subnetID,
				},
			},
		},
	}
}

func makeOpenStackMachineTemplateWithPortOptions(namespace, clusterName, name string, portOpts *[]infrav1.PortOpts) *infrav1.OpenStackMachineTemplate {
	return &infrav1.OpenStackMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: infrav1.OpenStackMachineTemplateSpec{
			Template: infrav1.OpenStackMachineTemplateResource{
				Spec: infrav1.OpenStackMachineSpec{
					Flavor:     e2eCtx.E2EConfig.GetVariable(shared.OpenStackNodeMachineFlavor),
					Image:      e2eCtx.E2EConfig.GetVariable(shared.OpenStackImageName),
					SSHKeyName: shared.DefaultSSHKeyPairName,
					CloudName:  e2eCtx.E2EConfig.GetVariable(shared.OpenStackCloud),
					IdentityRef: &infrav1.OpenStackIdentityReference{
						Kind: "Secret",
						Name: fmt.Sprintf("%s-cloud-config", clusterName),
					},
					Ports: *portOpts,
				},
			},
		},
	}
}

// makeJoinBootstrapConfigTemplate returns a KubeadmConfigTemplate which can be used
// to test different error cases. As we're missing e.g. the cloud provider conf it cannot
// be used to successfully add nodes to a cluster.
func makeJoinBootstrapConfigTemplate(namespace, name string) *bootstrapv1.KubeadmConfigTemplate {
	return &bootstrapv1.KubeadmConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: bootstrapv1.KubeadmConfigTemplateSpec{
			Template: bootstrapv1.KubeadmConfigTemplateResource{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: &bootstrapv1.JoinConfiguration{
						NodeRegistration: bootstrapv1.NodeRegistrationOptions{
							Name: "{{ local_hostname }}",
							KubeletExtraArgs: map[string]string{
								"cloud-config":   "/etc/kubernetes/cloud.conf",
								"cloud-provider": "openstack",
							},
						},
					},
				},
			},
		},
	}
}

func makeMachineDeployment(namespace, mdName, clusterName string, failureDomain string, replicas int32) *clusterv1.MachineDeployment {
	if failureDomain == "" {
		failureDomain = e2eCtx.E2EConfig.GetVariable(shared.OpenStackFailureDomain)
	}
	return &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdName,
			Namespace: namespace,
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": clusterName,
				"nodepool":                      mdName,
			},
		},
		Spec: clusterv1.MachineDeploymentSpec{
			Replicas: &replicas,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"cluster.x-k8s.io/cluster-name": clusterName,
					"nodepool":                      mdName,
				},
			},
			ClusterName: clusterName,
			Template: clusterv1.MachineTemplateSpec{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-name": clusterName,
						"nodepool":                      mdName,
					},
				},
				Spec: clusterv1.MachineSpec{
					ClusterName:   clusterName,
					FailureDomain: &failureDomain,
					Bootstrap: clusterv1.Bootstrap{
						ConfigRef: &corev1.ObjectReference{
							Kind:       "KubeadmConfigTemplate",
							APIVersion: bootstrapv1.GroupVersion.String(),
							Name:       mdName,
							Namespace:  namespace,
						},
					},
					InfrastructureRef: corev1.ObjectReference{
						Kind:       "OpenStackMachineTemplate",
						APIVersion: infrav1.GroupVersion.String(),
						Name:       mdName,
						Namespace:  namespace,
					},
					Version: pointer.StringPtr(e2eCtx.E2EConfig.GetVariable(shared.KubernetesVersion)),
				},
			},
		},
	}
}

func waitForDaemonSetRunning(ctx context.Context, ctrlClient crclient.Client, namespace, name string) {
	shared.Byf("Ensuring DaemonSet %s is running", name)
	daemonSet := &appsv1.DaemonSet{}
	Eventually(
		func() (bool, error) {
			if err := ctrlClient.Get(ctx, apimachinerytypes.NamespacedName{Namespace: namespace, Name: name}, daemonSet); err != nil {
				return false, err
			}
			return daemonSet.Status.CurrentNumberScheduled == daemonSet.Status.NumberReady, nil
		}, 10*time.Minute, 30*time.Second,
	).Should(BeTrue())
}

func waitForNodesReadyWithoutCCMTaint(ctx context.Context, ctrlClient crclient.Client, nodeCount int) {
	shared.Byf("Waiting for the workload nodes to be ready")
	Eventually(func() (int, error) {
		nodeList := &corev1.NodeList{}
		if err := ctrlClient.List(ctx, nodeList); err != nil {
			return 0, err
		}
		if len(nodeList.Items) == 0 {
			return 0, errors.New("no nodes were found")
		}

		count := 0
		for _, node := range nodeList.Items {
			n := node
			if noderefutil.IsNodeReady(&n) && isCloudProviderInitialized(node.Spec.Taints) {
				count++
			}
		}
		return count, nil
	}, "10m", "10s").Should(Equal(nodeCount))
}

func isCloudProviderInitialized(taints []corev1.Taint) bool {
	for _, taint := range taints {
		if taint.Key == "node.cloudprovider.kubernetes.io/uninitialized" {
			return false
		}
	}
	return true
}
