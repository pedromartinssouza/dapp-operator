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
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxkustomize "github.com/fluxcd/pkg/apis/kustomize"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	sigsyaml "sigs.k8s.io/yaml"

	cachev1alpha1 "github.com/pedromartinssouza/dapp-operator/api/v1alpha1"
)

// DappReconciler reconciles a Dapp object
type DappReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cache.dapp-operator.com,resources=dapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cache.dapp-operator.com,resources=dapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cache.dapp-operator.com,resources=dapps/finalizers,verbs=update
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=helmrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete

func (r *DappReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	dapp := &cachev1alpha1.Dapp{}
	if err := r.Get(ctx, req.NamespacedName, dapp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("reconciling dapp", "name", req.NamespacedName)

	if err := r.reconcileHelmRepository(ctx, dapp); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to reconcile HelmRepository")
		r.setReadyCondition(dapp, metav1.ConditionFalse, "HelmRepositoryFailed", err.Error())
		_ = r.Status().Update(ctx, dapp)
		return ctrl.Result{}, err
	}

	if err := r.reconcileHelmRelease(ctx, dapp); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to reconcile HelmRelease")
		r.setReadyCondition(dapp, metav1.ConditionFalse, "HelmReleaseFailed", err.Error())
		_ = r.Status().Update(ctx, dapp)
		return ctrl.Result{}, err
	}

	helmRepoName := helmRepositoryName(dapp)
	r.setReadyCondition(dapp, metav1.ConditionTrue, "Reconciled", "HelmRepository and HelmRelease are configured")
	dapp.Status.HelmRepositoryRef = fmt.Sprintf("%s/%s", dapp.Namespace, helmRepoName)
	dapp.Status.HelmReleaseRef = fmt.Sprintf("%s/%s", dapp.Namespace, dapp.Name)

	r.syncInstalledCondition(ctx, dapp)

	if err := r.Status().Update(ctx, dapp); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	installed := apimeta.FindStatusCondition(dapp.Status.Conditions, "Installed")
	if installed == nil || installed.Status != metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *DappReconciler) reconcileHelmRepository(ctx context.Context, dapp *cachev1alpha1.Dapp) error {
	helmRepo := &sourcev1.HelmRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmRepositoryName(dapp),
			Namespace: dapp.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, helmRepo, func() error {
		spec := sourcev1.HelmRepositorySpec{
			URL:      dapp.Spec.Helm.RepoURL,
			Interval: metav1.Duration{Duration: time.Minute},
		}
		if strings.HasPrefix(dapp.Spec.Helm.RepoURL, "oci://") {
			spec.Type = "oci"
		}
		helmRepo.Spec = spec
		return ctrl.SetControllerReference(dapp, helmRepo, r.Scheme)
	})
	return err
}

func (r *DappReconciler) reconcileHelmRelease(ctx context.Context, dapp *cachev1alpha1.Dapp) error {
	helmRelease := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dapp.Name,
			Namespace: dapp.Namespace,
		},
	}

	helmRepoName := helmRepositoryName(dapp)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, helmRelease, func() error {
		postRenderers, err := buildSchedulingPostRenderer(dapp)
		if err != nil {
			return err
		}
		helmRelease.Spec = helmv2.HelmReleaseSpec{
			Interval: metav1.Duration{Duration: 5 * time.Minute},
			Chart: &helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					Chart:   dapp.Spec.Helm.ChartName,
					Version: dapp.Spec.Helm.Version,
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind: sourcev1.HelmRepositoryKind,
						Name: helmRepoName,
					},
				},
			},
			PostRenderers: postRenderers,
		}
		if dapp.Spec.Helm.ReleaseName != "" {
			helmRelease.Spec.ReleaseName = dapp.Spec.Helm.ReleaseName
		}
		if dapp.Spec.Namespace != "" {
			helmRelease.Spec.TargetNamespace = dapp.Spec.Namespace
		}
		return ctrl.SetControllerReference(dapp, helmRelease, r.Scheme)
	})
	return err
}

func buildSchedulingPostRenderer(dapp *cachev1alpha1.Dapp) ([]helmv2.PostRenderer, error) {
	if len(dapp.Spec.NodeSelector) == 0 && len(dapp.Spec.Tolerations) == 0 {
		return nil, nil
	}

	podSpec := map[string]interface{}{}
	if len(dapp.Spec.NodeSelector) > 0 {
		podSpec["nodeSelector"] = dapp.Spec.NodeSelector
	}
	if len(dapp.Spec.Tolerations) > 0 {
		podSpec["tolerations"] = dapp.Spec.Tolerations
	}

	workloads := []struct{ apiVersion, kind string }{
		{"apps/v1", "Deployment"},
		{"apps/v1", "StatefulSet"},
		{"apps/v1", "DaemonSet"},
		{"batch/v1", "Job"},
	}

	patches := make([]fluxkustomize.Patch, 0, len(workloads))
	for _, w := range workloads {
		obj := map[string]interface{}{
			"apiVersion": w.apiVersion,
			"kind":       w.kind,
			"metadata":   map[string]interface{}{"name": "placeholder"},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": podSpec,
				},
			},
		}
		data, err := sigsyaml.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("marshaling %s patch: %w", w.kind, err)
		}
		patches = append(patches, fluxkustomize.Patch{
			Patch:  string(data),
			Target: &fluxkustomize.Selector{Kind: w.kind},
		})
	}

	return []helmv2.PostRenderer{{Kustomize: &helmv2.Kustomize{Patches: patches}}}, nil
}

func (r *DappReconciler) setReadyCondition(dapp *cachev1alpha1.Dapp, status metav1.ConditionStatus, reason, message string) {
	r.setCondition(dapp, "Ready", status, reason, message)
}

func (r *DappReconciler) setCondition(dapp *cachev1alpha1.Dapp, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&dapp.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: dapp.Generation,
	})
}

func (r *DappReconciler) syncInstalledCondition(ctx context.Context, dapp *cachev1alpha1.Dapp) {
	helmRelease := &helmv2.HelmRelease{}
	if err := r.Get(ctx, types.NamespacedName{Name: dapp.Name, Namespace: dapp.Namespace}, helmRelease); err != nil {
		r.setCondition(dapp, "Installed", metav1.ConditionFalse, "HelmReleaseNotFound", err.Error())
		return
	}

	hrReady := apimeta.FindStatusCondition(helmRelease.Status.Conditions, "Ready")
	if hrReady == nil {
		r.setCondition(dapp, "Installed", metav1.ConditionFalse, "Pending", "Waiting for Flux to install the chart")
		return
	}

	r.setCondition(dapp, "Installed", hrReady.Status, hrReady.Reason, hrReady.Message)
}

func helmRepositoryName(dapp *cachev1alpha1.Dapp) string {
	return dapp.Name + "-helmrepo"
}

// SetupWithManager sets up the controller with the Manager.
func (r *DappReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1alpha1.Dapp{}).
		Owns(&sourcev1.HelmRepository{}).
		Owns(&helmv2.HelmRelease{}).
		Named("dapp").
		Complete(r)
}
