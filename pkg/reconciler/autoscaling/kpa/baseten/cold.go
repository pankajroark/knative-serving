package baseten

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"knative.dev/pkg/apis/duck"
	"knative.dev/pkg/logging"
	autoscalingv1alpha1 "knative.dev/serving/pkg/apis/autoscaling/v1alpha1"
	"knative.dev/serving/pkg/apis/serving"
	"k8s.io/apimachinery/pkg/types"
)

type ColdBooster struct {
	podsLister corev1listers.PodLister
	kubeClient kubernetes.Interface
	coldBoost *ColdBoost
}

func NewColdBooster(podsLister corev1listers.PodLister, kubeClient kubernetes.Interface) *ColdBooster {
	return &ColdBooster{
		podsLister: podsLister,
		kubeClient: kubeClient,
	}
}

func (c *ColdBooster) Inform(ctx context.Context, currentScale int32, desiredScale int32, ps *autoscalingv1alpha1.PodScalable) error {
	logger := logging.FromContext(ctx)
	if currentScale == desiredScale {
		return nil
	}

	deployment, err := podScalableToDeployment(ps)
	if err != nil {
		return err
	}
	if currentScale == 0 {
		// Scale from zero, start cold boost if not in progress already
		if c.coldBoost == nil || c.coldBoost.Done() {
			logger.Info("Starting cold boost")
			c.coldBoost = NewColdBoost(c.podsLister, c.kubeClient, deployment, logger)
		}
	}
	if desiredScale == 0 {
		// Scale to zero, shut down cold boost if running
		if c.coldBoost != nil {
			logger.Info("Stopping cold boost")
			c.coldBoost.Stop()
		}
	}
	return nil
}

type ColdBoost struct {
	podsLister corev1listers.PodLister
	kubeClient kubernetes.Interface
	deployment *appsv1.Deployment
	stopCh chan struct{}
	ticker *time.Ticker
	done atomic.Bool
	logger *zap.SugaredLogger
}

func NewColdBoost(podsLister corev1listers.PodLister, kubeClient kubernetes.Interface, deployment *appsv1.Deployment, logger *zap.SugaredLogger) *ColdBoost{
	c := &ColdBoost{
		podsLister: podsLister,
		kubeClient: kubeClient,
		deployment: deployment,
		stopCh: make(chan struct{}),
		ticker: time.NewTicker(2 * time.Second),
		logger: logger,
	}
	logger.Info("Setting cold start replicas to 1")
	err := c.setColdStartReplicaCount(context.TODO(), 1)
	if err != nil {
		logger.Warnf("Unable to start cold start deployment %v", err)
	}
	go func() {
		defer c.End()
		for {
			select {
			case <-c.stopCh:
				c.logger.Info("Received stop, shutting down")
				return
			case <-c.ticker.C:
				// If orig ksvc pod has come up then end cold boost
				c.logger.Info("Ticker received, checking orig ksvc ready pods")
				err, podReady := c.origKsvcPodReady()
				c.logger.Infof("%d pods are ready for original ksvc", podReady)
				if err != nil {
					c.logger.Warnf("Unable to get ready status for pods of %s.%s", c.deployment.Namespace, c.deployment.Name)
					continue
				}
				if podReady {
					c.logger.Info("At least one pod of ksvc is ready, shutting down")
					return
				}
			}
		}
	}()
	return c
}

func (c *ColdBoost) Done() bool {
	return c.done.Load()
}

func (c *ColdBoost) Stop() {
	if !c.Done() {
		c.stopCh <- struct{}{}
	}
}

func (c *ColdBoost) End() {
	if !c.Done() {
		c.logger.Info("Setting cold start replica count to 0")
		c.setColdStartReplicaCount(context.TODO(), 0)
		c.ticker.Stop()
		c.done.Store(true)
		// Close channel after setting done to true, otherwise Stop may try to send to closed channel.
		close(c.stopCh)
	}
}

func (c *ColdBoost) origKsvcPodReady() (error, bool) {
	dep := c.deployment
	// c.logger.Infof("deployment: %v", dep)
	revKey := serving.RevisionLabelKey
	revisionLabelSelector, err := labels.NewRequirement(revKey, selection.Equals, []string{dep.Labels[revKey]})
	if err != nil {
		return err, false
	}
	notColdLabelSelector, err := labels.NewRequirement("cold", selection.DoesNotExist, []string{})
	if err != nil {
		return err, false
	}
	origKsvcPodLabels := labels.Everything().Add(*revisionLabelSelector).Add(*notColdLabelSelector)
	pods, err := c.podsLister.Pods(dep.Namespace).List(origKsvcPodLabels)
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning && isPodReady(p) && p.DeletionTimestamp == nil {
			return nil, true
		}
	}
	return nil, false
}


func (c *ColdBoost) setColdStartReplicaCount(ctx context.Context, desiredScale int32) error {
	deploy := c.deployment
	ns := deploy.Namespace
	deployments := c.kubeClient.AppsV1().Deployments(ns)
	coldDeployment, err := deployments.Get(ctx, coldDeploymentName(deploy.Name), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("Cold deployment not found, creating")
			// Deployment does not exist, create it
			coldDeployment = deploy.DeepCopy()
			coldDeployment.Name = coldDeploymentName(deploy.Name)
			coldDeployment.Spec.Replicas = &desiredScale
			coldDeployment.Labels["cold"] = "true"
			coldDeployment, err = deployments.Create(ctx, coldDeployment, metav1.CreateOptions{})
			if err != nil {
				c.logger.Infof("Unable to create cold deployment %v", err)
			} else {
				c.logger.Info("Successfully created cold deployment")
			}
		} else {
			return err
		}
	} else {
		if coldDeployment.Spec.Replicas != &desiredScale {

			coldDeploymentNew := coldDeployment.DeepCopy()
			coldDeploymentNew.Spec.Replicas = &desiredScale
			patch, err := duck.CreatePatch(coldDeployment, coldDeploymentNew)
			if err != nil {
				return err
			}
			patchBytes, err := patch.MarshalJSON()
			if err != nil {
				return err
			}
			_, err = deployments.Patch(ctx, coldDeployment.Name, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
			if err != nil {
				c.logger.Infof("Unable to update cold deployment %v", err)
			} else {
				c.logger.Info("Successfully updated cold deployment")
			}
		}
	}
	return err
}

func coldDeploymentName(name string) string {
	return fmt.Sprintf("%v-cold", name)
}

func podScalableToDeployment(ps *autoscalingv1alpha1.PodScalable) (*appsv1.Deployment, error) {
	psCopy := ps.DeepCopy()
	psCopy.ResourceVersion = ""
	psCopy.UID = ""
	origJson, err := json.Marshal(psCopy)
	if err != nil {
		return nil, err
	}
	deployment := new(appsv1.Deployment)
	err = json.Unmarshal(origJson, deployment)
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func isPodReady(p *corev1.Pod) bool {
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	// No ready status, probably not even running.
	return false
}