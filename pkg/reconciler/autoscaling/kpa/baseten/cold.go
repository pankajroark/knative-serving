package baseten

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	corev1listers "k8s.io/client-go/listers/core/v1"
	kubeclient "knative.dev/pkg/client/injection/kube/client"
	autoscalingv1alpha1 "knative.dev/serving/pkg/apis/autoscaling/v1alpha1"
)

type ColdBooster struct {
	deployment *appsv1.Deployment
	podsLister corev1listers.PodLister
	coldBoost *ColdBoost
}

type ColdBoost struct {
	deployment *appsv1.Deployment
	podsLister corev1listers.PodLister
	stopCh chan struct{}
	ticker *time.Ticker
	done bool
}

func NewColdBoost(podsLister corev1listers.PodLister) *ColdBoost{
	stopCh := make(chan struct{})
	c := &ColdBoost{
		stopCh: stopCh,
		ticker: time.NewTicker(2 * time.Second),
		done: false,
	}
	c.setColdStartReplicaCount(context.TODO(), 1)
	go func() {
		defer c.End()
		for {
			select {
			case <-c.stopCh:
				return
			case <-c.ticker.C:
				// do sth
				// If orig ksvc pod has come up then send stop signal
			}
		}
	}()
	return c
}

func (c *ColdBoost) Stop() {
	if !c.done {
		c.stopCh <- struct{}{}
	}
}

func (c *ColdBoost) End() {
	if !c.done {
		c.setColdStartReplicaCount(context.TODO(), 0)
		c.ticker.Stop()
		close(c.stopCh)
		c.done = true
	}
}

func NewColdBooster(podsLister corev1listers.PodLister) *ColdBooster {
	return &ColdBooster{
		podsLister: podsLister,
	}
}

func (c *ColdBooster) Inform(ctx context.Context, currentScale int32, desiredScale int32, ps *autoscalingv1alpha1.PodScalable) error {
	if currentScale == desiredScale {
		return nil
	}
	ro := runtime.Object(ps)
	c.deployment = ro.(*appsv1.Deployment)
	if currentScale == 0 {
		// Scale from zero, start cold boost if not in progress already
		if c.coldBoost == nil || c.coldBoost.done {
			c.coldBoost = NewColdBoost(c.podsLister)
		}
	}
	if desiredScale == 0 {
		// Scale to zero, shut down cold boost if running
		if c.coldBoost != nil {
			c.coldBoost.Stop()
		}
	}
	return nil
}

func (c *ColdBoost) setColdStartReplicaCount(ctx context.Context, desiredScale int32) error {
	kc := kubeclient.Get(ctx)
	deploy := c.deployment
	ns := deploy.Namespace
	deployments := kc.AppsV1().Deployments(ns)
	coldDeployment, err := deployments.Get(ctx, coldDeploymentName(deploy.Name), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if apierrors.IsNotFound(err) {
		// Deployment does not exist, create it
		coldDeployment = deploy.DeepCopy()
		coldDeployment.Spec.Replicas = &desiredScale
		coldDeployment, err = deployments.Create(ctx, coldDeployment, metav1.CreateOptions{})
	} else {
		if coldDeployment.Spec.Replicas != &desiredScale {
			coldDeployment.Spec.Replicas = &desiredScale
			coldDeployment, err = deployments.Update(ctx, coldDeployment, metav1.UpdateOptions{})
		}
	}
	return err
}

// func podScalableToColdStartPodSpec(ps *autoscalingv1alpha1.PodScalable) *corev1.Pod{
// 	ns := ps.Namespace
// 	return &corev1.Pod{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name: coldPodName(ps),
// 			Namespace: ns,
// 			Labels: ps.Labels, // todo : add cold label
// 			Annotations: ps.Annotations,
// 		},
// 		Spec: ps.Spec.Template.Spec,
// 	} 
// }

// func coldPodName(ps *autoscalingv1alpha1.PodScalable) string {
// 	return fmt.Sprintf("%v-cold", ps.Name)
// }

func coldDeploymentName(name string) string {
	return fmt.Sprintf("%v-cold", name)
}