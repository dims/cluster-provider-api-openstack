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
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog"
	"k8s.io/klog/klogr"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
)

const (
	clusterName          = "testcluster1"
	openstackClusterName = "testopenstackcluster1"
	machineName          = "testmachine1"
	openstackmachineName = "testopenstackmachine1"
	namespaceName        = "default" // change me
	secretName           = "testsecret1"
	requeueAfter         = time.Second * 30
)

func init() {
	klog.InitFlags(nil)
	logf.SetLogger(klogr.New())

	// Register required object kinds with global scheme.
	_ = apiextensionsv1.AddToScheme(scheme.Scheme)
	_ = clusterv1.AddToScheme(scheme.Scheme)
	_ = infrav1.AddToScheme(scheme.Scheme)
	_ = corev1.AddToScheme(scheme.Scheme)
}

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clusterv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := infrav1.AddToScheme(s); err != nil {
		panic(err)
	}

	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}

	return s
}
func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func(done Done) {
	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	err = infrav1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = infrav1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(k8sClient).ToNot(BeNil())

	close(done)
}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

var deletionTimestamp = metav1.Now()

func clusterPauseSpec() *clusterv1.ClusterSpec {
	return &clusterv1.ClusterSpec{
		Paused: true,
		InfrastructureRef: &v1.ObjectReference{
			Name:       openstackClusterName,
			Namespace:  namespaceName,
			Kind:       "OpenStackCluster",
			APIVersion: infrav1.GroupVersion.String(),
		},
	}
}

func oscSpec() *infrav1.OpenStackClusterSpec {
	return &infrav1.OpenStackClusterSpec{
		CloudName:    openstackClusterName,
		CloudsSecret: &v1.SecretReference{Name: secretName, Namespace: namespaceName},
	}
}
func oscOwnerRef() *metav1.OwnerReference {
	return &metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       clusterName,
	}
}
func getKey(objectName string) *client.ObjectKey {
	return &client.ObjectKey{
		Name:      objectName,
		Namespace: namespaceName,
	}
}

func newCluster(spec *clusterv1.ClusterSpec, status *clusterv1.ClusterStatus) *clusterv1.Cluster {
	if spec == nil {
		spec = &clusterv1.ClusterSpec{
			InfrastructureRef: &v1.ObjectReference{
				Name:       openstackClusterName,
				Namespace:  namespaceName,
				Kind:       "OpenStackCluster",
				APIVersion: infrav1.GroupVersion.String(),
			},
		}
	}
	if status == nil {
		status = &clusterv1.ClusterStatus{
			InfrastructureReady: true,
		}
	}
	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespaceName,
		},
		Spec:   *spec,
		Status: *status,
	}
}

func newOpenStackCluster(spec *infrav1.OpenStackClusterSpec, status *infrav1.OpenStackClusterStatus,
	ownerRef *metav1.OwnerReference, pausedAnnotation bool) *infrav1.OpenStackCluster {
	if spec == nil {

		spec = &infrav1.OpenStackClusterSpec{
			CloudsSecret: &v1.SecretReference{Name: secretName, Namespace: "default"},
		}
	}

	if status == nil {
		status = &infrav1.OpenStackClusterStatus{}
	}
	ownerRefs := []metav1.OwnerReference{}
	if ownerRef != nil {
		ownerRefs = []metav1.OwnerReference{*ownerRef}
	}
	objMeta := &metav1.ObjectMeta{
		Name:              openstackClusterName,
		Namespace:         namespaceName,
		OwnerReferences:   ownerRefs,
		DeletionTimestamp: &deletionTimestamp,
	}
	if pausedAnnotation == true {
		objMeta = &metav1.ObjectMeta{
			Name:      openstackClusterName,
			Namespace: namespaceName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       clusterName,
					UID:        "68cba693-d6fc-4a10-b68c-8329570b1209",
				},
			},
			Annotations: map[string]string{
				clusterv1.PausedAnnotation: "true",
			},
		}
	}

	return &infrav1.OpenStackCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "OpenStackCluster",
			APIVersion: infrav1.GroupVersion.String(),
		},
		ObjectMeta: *objMeta,
		Spec:       *spec,
		Status:     *status,
	}
}

func newSecret() *v1.Secret {
	return &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: infrav1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespaceName,
		},
		Data: map[string][]byte{
			"clouds.yaml": []byte("ewogICJjbG91ZHMiOiB7CiAgICAib3BlbnN0YWNrLTEiOiB7CiAgICAgICJhdXRoIjogewogICAgICAgICJhdXRoX3VybCI6ICJodHRwczovL3Rlc3RjbG91ZC5jb206NTAwMCIsCiAgICAgICAgInByb2plY3RfbmFtZSI6ICJ0ZXN0cHJvamVjdCIsCiAgICAgICAgInVzZXJuYW1lIjogInRlc3R1c2VyIiwKICAgICAgICAicGFzc3dvcmQiOiAidGVzdHBhc3N3b3JkIiwKICAgICAgICAidmVyc2lvbiI6ICIzIiwKICAgICAgICAiZG9tYWluX25hbWUiOiAidGVzdHByb2plY3QiLAogICAgICAgICJ1c2VyX2RvbWFpbl9uYW1lIjogInRlc3Rwcm9qZWN0IiwKICAgICAgICAicHJvamVjdF9uYW1lIjogInRlc3QxIiwKICAgICAgICAidGVuYW50X25hbWUiOiAidGVzdDEiCiAgICAgIH0sCiAgICAgICJyZWdpb25fbmFtZSI6ICJyZWdpb24xIiwKICAgICAgImNhY2VydCI6ICIvdG1wL2NhY2VydC5wZW0iLAogICAgICAidmVyaWZ5IjogZmFsc2UKICAgIH0KICB9Cn0K"),
		},
		Type: "Opaque",
	}
}

func deletedOpenStackCluster() *infrav1.OpenStackCluster {

	spec := &infrav1.OpenStackClusterSpec{
		CloudsSecret: &v1.SecretReference{Name: secretName, Namespace: "default"},
	}
	status := &infrav1.OpenStackClusterStatus{}
	ownerRefs := []metav1.OwnerReference{}
	objMeta := &metav1.ObjectMeta{
		Name:              openstackClusterName,
		Namespace:         namespaceName,
		OwnerReferences:   ownerRefs,
		DeletionTimestamp: &deletionTimestamp,
	}

	return &infrav1.OpenStackCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "OpenStackCluster",
			APIVersion: infrav1.GroupVersion.String(),
		},
		ObjectMeta: *objMeta,
		Spec:       *spec,
		Status:     *status,
	}
}
