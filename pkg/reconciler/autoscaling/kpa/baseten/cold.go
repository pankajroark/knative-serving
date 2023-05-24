package baseten

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"knative.dev/pkg/apis/duck"
	"knative.dev/pkg/logging"
	autoscalingv1alpha1 "knative.dev/serving/pkg/apis/autoscaling/v1alpha1"
	"knative.dev/serving/pkg/apis/serving"
)

type ColdBooster struct {
	podsLister corev1listers.PodLister
	kubeClient kubernetes.Interface
	coldBoost *ColdBoost
}

const coldLabel = "cold"
const coldDeploymentSuffix = "-cold"

func NewColdBooster(podsLister corev1listers.PodLister, kubeClient kubernetes.Interface) *ColdBooster {
	return &ColdBooster{
		podsLister: podsLister,
		kubeClient: kubeClient,
	}
}

func (c *ColdBooster) Inform(ctx context.Context, currentScale int32, desiredScale int32, ps *autoscalingv1alpha1.PodScalable) error {
	logger := logging.FromContext(ctx)
	coldStartSettings, err := GetColdstartSettings(ctx, ps.Namespace, ps.Name)
	if err != nil {
		return err
	}

	if !coldStartSettings.Enabled {
		return nil
	}

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
			logger.Infof("ColdBoost for %s.%s: starting cold boost", deployment.Namespace, deployment.Name)
			c.coldBoost = NewColdBoost(c.podsLister, c.kubeClient, deployment, *coldStartSettings, logger)
		}
	}
	if desiredScale == 0 {
		// Scale to zero, shut down cold boost if running
		if c.coldBoost != nil {
			logger.Infof("ColdBoost for %s.%s: stopping cold boost", deployment.Namespace, deployment.Name)
			c.coldBoost.Stop()
		}
	}
	return nil
}

type ColdBoost struct {
	podsLister corev1listers.PodLister
	kubeClient kubernetes.Interface
	deployment *appsv1.Deployment
	coldStartSettings ColdStartSettings
	stopCh chan struct{}
	ticker *time.Ticker
	done atomic.Bool
	logger *zap.SugaredLogger
}

func NewColdBoost(
	podsLister corev1listers.PodLister, 
	kubeClient kubernetes.Interface, 
	deployment *appsv1.Deployment, 
	coldStartSettings ColdStartSettings,
	logger *zap.SugaredLogger) *ColdBoost{
	c := &ColdBoost{
		podsLister: podsLister,
		kubeClient: kubeClient,
		deployment: deployment.DeepCopy(),
		coldStartSettings: coldStartSettings,
		stopCh: make(chan struct{}),
		ticker: time.NewTicker(2 * time.Second),
		logger: logger,
	}
	logger.Infof("%s setting cold boost deployment replicas to 1", c.logPrefix())
	err := c.setColdStartReplicaCount(context.TODO(), 1)
	if err != nil {
		logger.Warnf("%s unable to set cold boost deployment replicas to 1: %v", c.logPrefix(), err)
	}
	go func() {
		defer c.End()
		for {
			select {
			case <-c.stopCh:
				c.logger.Infof("%s received stop message, shutting down", c.logPrefix())
				return
			case <-c.ticker.C:
				// If orig ksvc pod has come up then end cold boost
				c.logger.Debugf("%s tick received, checking orig ksvc ready pods", c.logPrefix())
				err, podReady := c.origKsvcPodReady()
				if err != nil {
					c.logger.Warnf("%s unable to get ready status for pods", c.logPrefix())
					continue
				}
				if podReady {
					c.logger.Infof("%s at least one pod of ksvc is ready, shutting down", c.logPrefix())
					return
				}
				c.logger.Debugf("%s pods are not ready for original ksvc", c.logPrefix())
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
		c.logger.Infof("%s setting cold start replica count to 0", c.logPrefix())
		c.setColdStartReplicaCount(context.TODO(), 0)
		c.ticker.Stop()
		c.done.Store(true)
		// Close channel after setting done to true, otherwise Stop may try to send to closed channel.
		close(c.stopCh)
	}
}

func (c *ColdBoost) origKsvcPodReady() (error, bool) {
	dep := c.deployment
	revKey := serving.RevisionLabelKey
	revisionLabelSelector, err := labels.NewRequirement(revKey, selection.Equals, []string{dep.Labels[revKey]})
	if err != nil {
		return err, false
	}
	notColdLabelSelector, err := labels.NewRequirement(coldLabel, selection.DoesNotExist, []string{})
	if err != nil {
		return err, false
	}
	origKsvcPodLabels := labels.Everything().Add(*revisionLabelSelector).Add(*notColdLabelSelector)
	pods, err := c.podsLister.Pods(dep.Namespace).List(origKsvcPodLabels)
	if err != nil {
		return err, false
	}
	return nil, atLeastOneReadyPod(pods)
}

func (c *ColdBoost) setColdStartReplicaCount(ctx context.Context, desiredScale int32) error {
	origDeployment := c.deployment
	ns := origDeployment.Namespace
	deployments := c.kubeClient.AppsV1().Deployments(ns)
	coldDeployment, err := existingDeployment(ctx, c.kubeClient, ns, genColdDeploymentName(origDeployment.Name))
	if err != nil {
		return err
	}
	if coldDeployment == nil {
			c.logger.Infof("%s cold deployment not found, creating", c.logPrefix())
			return c.createColdDeployment(ctx, deployments, desiredScale)
	}
	// Cold deployment exists
	if coldDeployment.Spec.Replicas == &desiredScale {
		return nil
	}
	return c.patchColdDeployment(ctx, deployments, coldDeployment, desiredScale)
}

func (c *ColdBoost) logPrefix() string {
	if c.deployment != nil {
		fullDeploymentName := fmt.Sprintf("%s.%s", c.deployment.Namespace, c.deployment.Name)
		return fmt.Sprintf("ColdBoost for %s:", fullDeploymentName)
	}
	return "ColdBoost:"
}

func genColdDeploymentName(name string) string {
	return fmt.Sprintf("%v%s", name, coldDeploymentSuffix)
}

func podScalableToDeployment(ps *autoscalingv1alpha1.PodScalable) (*appsv1.Deployment, error) {
	// Use json Marshal/Unmarshal to convert from PodScalable to Deployment.
	// [Pankaj] At least for baseten's set up PodScalable is always the deployment, but can't
	// seem to find an easy way to convert, so resorting to this.
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

func isPodConditionReady(p *corev1.Pod) bool {
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	// No ready status, probably not even running.
	return false
}

func atLeastOneReadyPod(pods []*corev1.Pod) bool {
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning && isPodConditionReady(p) && p.DeletionTimestamp == nil {
			return true
		}
	}
	return false
}

func existingDeployment(ctx context.Context, kubeClient kubernetes.Interface, namespace string, name string) (*appsv1.Deployment, error) {
	deployments := kubeClient.AppsV1().Deployments(namespace)
	deployment, err := deployments.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return deployment, err
	}
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return nil, err
}

func (c *ColdBoost) generateColdDeploymentSpec(desiredScale int32) (*appsv1.Deployment, error) {
	coldDeployment := c.deployment.DeepCopy()
	coldDeployment.Name = genColdDeploymentName(c.deployment.Name)
	coldDeployment.Spec.Replicas = &desiredScale
	coldDeployment.Spec.Template.Labels[coldLabel] = "true"
	err := c.applyColdPatch(coldDeployment)
	if err != nil {
		return nil, err
	}
	return coldDeployment, nil
}

func (c *ColdBoost) createColdDeployment(ctx context.Context, deployments v1.DeploymentInterface, desiredScale int32) error {
	// Deployment does not exist, create it
	coldDeployment, err := c.generateColdDeploymentSpec(desiredScale)
	if err != nil {
		return err
	}
	_, err = deployments.Create(ctx, coldDeployment, metav1.CreateOptions{})
	if err != nil {
		c.logger.Infof("%s unable to create cold deployment %v", c.logPrefix(), err)
		return err
	}
	c.logger.Infof("%s successfully created cold deployment", c.logPrefix())
	return nil
}

func (c *ColdBoost) patchColdDeployment(ctx context.Context, deployments v1.DeploymentInterface, coldDeployment *appsv1.Deployment, desiredScale int32) error {
	// Generate the expected new state of cold deployment
	coldDeploymentNew, err := c.generateColdDeploymentSpec(desiredScale)
	if err != nil {
		return err
	}
	// Now take a difference from current cold deployment
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
		c.logger.Infof("%s unable to update cold deployment %v", c.logPrefix(), err)
		return err
	}
	c.logger.Infof("%s successfully updated cold deployment", c.logPrefix())
	return nil
}

func (c *ColdBoost) applyColdPatch(coldDeployment *appsv1.Deployment) (error) {
	json_patch_string := c.coldStartSettings.PodSpecJsonPatch
	c.logger.Infof("json patch: %s", json_patch_string)
	podSpec := coldDeployment.Spec.Template.Spec
	origPodBytes, err := json.Marshal(podSpec)
	if err != nil {
		return err
	}
	patch, err := jsonpatch.DecodePatch([]byte(json_patch_string))
	if err != nil {
		return err
	}
	modified, err := patch.Apply(origPodBytes)
	if err != nil {
		return err
	}
	newPodSpec := new(corev1.PodSpec)	
	err = json.Unmarshal(modified, newPodSpec)
	if err != nil {
		return err
	}
	coldDeployment.Spec.Template.Spec = *newPodSpec
	return nil
}
