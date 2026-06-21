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
	"context"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cachev1alpha1 "github.com/pedromartinssouza/dapp-operator/api/v1alpha1"
)

var _ = Describe("Dapp Controller", func() {
	Context("When reconciling a Dapp with a Helm spec", func() {
		const resourceName = "test-dapp"
		const repoURL = "https://stefanprodan.github.io/podinfo"

		ctx := context.Background()

		namespacedName := types.NamespacedName{Name: resourceName, Namespace: "default"}
		helmRepoName := types.NamespacedName{Name: resourceName + "-helmrepo", Namespace: "default"}
		helmReleaseName := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			By("creating the Dapp resource")
			dapp := &cachev1alpha1.Dapp{}
			err := k8sClient.Get(ctx, namespacedName, dapp)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &cachev1alpha1.Dapp{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: cachev1alpha1.DappSpec{
						DappName:  "test-dapp",
						Namespace: "test-dapp-system",
						Helm: cachev1alpha1.HelmSpec{
							ReleaseName: "test-dapp",
							ChartName:   "podinfo",
							Version:     ">=6.0.0",
							RepoURL:     repoURL,
						},
					},
				})).To(Succeed())
			}
		})

		AfterEach(func() {
			By("deleting the Dapp resource")
			dapp := &cachev1alpha1.Dapp{}
			err := k8sClient.Get(ctx, namespacedName, dapp)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, dapp)).To(Succeed())
		})

		It("should create a HelmRepository and HelmRelease and set Ready=True", func() {
			By("running the reconciler")
			reconciler := &DappReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking that the HelmRepository was created")
			helmRepo := &sourcev1.HelmRepository{}
			Expect(k8sClient.Get(ctx, helmRepoName, helmRepo)).To(Succeed())
			Expect(helmRepo.Spec.URL).To(Equal(repoURL))

			By("checking that the HelmRelease was created")
			helmRelease := &helmv2.HelmRelease{}
			Expect(k8sClient.Get(ctx, helmReleaseName, helmRelease)).To(Succeed())
			Expect(helmRelease.Spec.Chart.Spec.Chart).To(Equal("podinfo"))
			Expect(helmRelease.Spec.Chart.Spec.Version).To(Equal(">=6.0.0"))
			Expect(helmRelease.Spec.Chart.Spec.SourceRef.Name).To(Equal(resourceName + "-helmrepo"))
			Expect(helmRelease.Spec.TargetNamespace).To(Equal("test-dapp-system"))
			Expect(helmRelease.Spec.ReleaseName).To(Equal("test-dapp"))

			By("checking that the Dapp status reflects Ready=True")
			dapp := &cachev1alpha1.Dapp{}
			Expect(k8sClient.Get(ctx, namespacedName, dapp)).To(Succeed())
			Expect(dapp.Status.HelmRepositoryRef).To(Equal("default/" + resourceName + "-helmrepo"))
			Expect(dapp.Status.HelmReleaseRef).To(Equal("default/" + resourceName))

			readyCond := findCondition(dapp.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
