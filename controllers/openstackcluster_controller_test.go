package controllers

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	capierrors "sigs.k8s.io/cluster-api/errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Reconcile OpenStackCluster", func() {

	type TestCaseReconcileOSC struct {
		Objects             []runtime.Object
		ErrorType           error
		ErrorExpected       bool
		RequeueExpected     bool
		ErrorReasonExpected bool
		ErrorReason         capierrors.ClusterStatusError
	}

	DescribeTable("OpenStackCluster Reconcile",
		func(tc TestCaseReconcileOSC) {
			_tmpOSCluster := &infrav1.OpenStackCluster{}
			c := fake.NewFakeClientWithScheme(setupScheme(), tc.Objects...)
			ctx := context.Background()

			reconciler := OpenStackClusterReconciler{
				Client: c,
			}
			// res, err := reconciler.Reconcile(req)
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      openstackClusterName,
					Namespace: namespaceName,
				},
			})

			if tc.ErrorExpected {
				Expect(err).To(HaveOccurred())
				if tc.ErrorType != nil {
					Expect(reflect.TypeOf(tc.ErrorType)).To(Equal(reflect.TypeOf(errors.Cause(err))))
				}

			} else {
				Expect(err).NotTo(HaveOccurred())
			}
			if tc.RequeueExpected {
				Expect(result.Requeue).NotTo(BeFalse())
				Expect(result.RequeueAfter).To(Equal(requeueAfter))
			} else {
				Expect(result.Requeue).To(BeFalse())
			}
			if tc.ErrorReasonExpected {
				_ = c.Get(context.TODO(), *getKey(openstackClusterName), _tmpOSCluster)
				//Expect(_tmpOSCluster.Status.FailureReason).NotTo(BeNil())
				//Expect(tc.ErrorReason).To(Equal(*_tmpOSCluster.Status.FailureReason))
				//ToDo, Add status
			}
		},
		//Given a capi cluster, but no OpenStackCluster resource
		Entry("Should not return an error when OpenStackCluster is not found",
			TestCaseReconcileOSC{
				Objects: []runtime.Object{
					newCluster(nil, nil),
				},
				ErrorExpected:   false,
				RequeueExpected: false,
			},
		),
		// Given no capi cluster resource owns this OpenStackCluster, error is expected
		Entry("Should return en error when capi cluster is not found",
			TestCaseReconcileOSC{
				Objects: []runtime.Object{
					newOpenStackCluster(oscSpec(), nil, oscOwnerRef(), false),
				},
				ErrorExpected:       true,
				ErrorReasonExpected: true,
				ErrorReason:         capierrors.InvalidConfigurationClusterError,
				RequeueExpected:     false,
			},
		),
		// Given a capi cluster and an OpenStackCluster with no owner reference
		Entry("Should not return an error if OwnerRef is not set on OpenStackCluster",
			TestCaseReconcileOSC{
				Objects: []runtime.Object{
					newOpenStackCluster(nil, nil, nil, false),
					newCluster(nil, nil),
				},
				ErrorExpected:   false,
				RequeueExpected: false,
			},
		),

		//Given secret with missing relevant fileds, error should occur
		Entry("Should return an error when relevant fields are missing from the secret",
			TestCaseReconcileOSC{
				Objects: []runtime.Object{
					newOpenStackCluster(nil, nil, oscOwnerRef(), false),
					newCluster(nil, nil),
				},
				ErrorExpected:   true,
				RequeueExpected: false,
			},
		),

		// Given: Cluster, OpenStackCluster.
		// Cluster.Spec.Paused=true
		// Expected: Requeue Expected
		// Enable this after creating a cluster template with pause annotation.
		// Entry("Should requeue when owner Cluster is paused",
		// 	TestCaseReconcileOSC{
		// 		Objects: []runtime.Object{
		// 			newCluster(clusterPauseSpec(), nil),
		// 			newOpenStackCluster(oscSpec(), nil, oscOwnerRef(), false),
		// 		},
		// 		ErrorExpected:   false,
		// 		RequeueExpected: false,
		// 	},
		// ),

		//Given: Cluster, OpenStackCluster.
		//OpenStackCluster has cluster.x-k8s.io/paused annotation
		//Expected: Requeue Expected
		// Not ready
		Entry("Should requeue when OpenStackCluster has paused annotation",
			TestCaseReconcileOSC{
				Objects: []runtime.Object{
					newCluster(nil, nil),
					newOpenStackCluster(nil, nil, nil, true),
				},
				ErrorExpected:   false,
				RequeueExpected: false, // needs investigation
			},
		),
		// Reconcile Deletion
		Entry("Should reconcileDelete when deletion timestamp is set.",
			TestCaseReconcileOSC{
				Objects: []runtime.Object{
					deletedOpenStackCluster(),
				},
				ErrorExpected:   false,
				RequeueExpected: false,
			},
		),
	) // end of reconcile
})
