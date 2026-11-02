/*
Copyright 2026.

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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1alpha1 "github.com/adover06/k6s/api/v1alpha1"
)

var _ = Describe("Tenant Controller", func() {
	It("provisions a namespace, quota and admin RoleBinding", func() {
		tenant := &platformv1alpha1.Tenant{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: platformv1alpha1.TenantSpec{
				Admins: []rbacv1.Subject{{Kind: "User", Name: "alice", APIGroup: rbacv1.GroupName}},
				Quota: platformv1alpha1.TenantQuota{
					CPU: resource.MustParse("2"), Memory: resource.MustParse("4Gi"), Pods: 10,
				},
			},
		}
		Expect(k8sClient.Create(ctx, tenant)).To(Succeed())
		DeferCleanup(k8sClient.Delete, ctx, tenant)

		r := &TenantReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tenant.Name}})
		Expect(err).NotTo(HaveOccurred())

		ns := tenant.NamespaceName()
		var namespace corev1.Namespace
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, &namespace)).To(Succeed())
		Expect(namespace.Labels).To(HaveKeyWithValue(tenantLabel, tenant.Name))

		var quota corev1.ResourceQuota
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tenant-quota"}, &quota)).To(Succeed())
		Expect(quota.Spec.Hard.Pods().Value()).To(Equal(int64(10)))

		var rb rbacv1.RoleBinding
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tenant-admins"}, &rb)).To(Succeed())
		Expect(rb.RoleRef.Name).To(Equal("admin"))
		Expect(rb.Subjects).To(Equal(tenant.Spec.Admins))
	})
})
