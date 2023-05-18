package coldstart

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/apis/duck"
	kubeclient "knative.dev/pkg/client/injection/kube/client"
	"knative.dev/pkg/logging"
	autoscalingv1alpha1 "knative.dev/serving/pkg/apis/autoscaling/v1alpha1"
)

func InformBasetenScaleFromZero(ctx context.Context, ps *autoscalingv1alpha1.PodScalable) error {
	// Scaling up from zero
	// todo: Access the url from configmap here and call that api with details
	// 1. ksvc name
	// 2. namespace
	// 3. revision id
	logger := logging.FromContext(ctx)
	logger.Infof("Pankaj: Scaling up from zero. Scale target %v", ps)
	_, err := postJsonBaseten(ctx, "/notify_scale_from_zero", ps)
	return err
}

func FetchColdstartSettings(ctx context.Context, namespace, revision string) (*ColdStartSettings, error) {
	body, err := postJsonBaseten(ctx, "/ksvc_revision_cold_start_settings", map[string]string{
		"revision": revision,
		"namespace": namespace,
	})
	if err != nil {
		return nil, err
	}
	var result ColdStartSettings
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

func postJsonBaseten(ctx context.Context, path string, payload any) ([]byte, error) {
	logger := logging.FromContext(ctx)
	payload_bytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("http://baseten-django.baseten:8000%s", path)
	logger.Infof("Calling %s", url)
	request, err := http.NewRequest("POST", url, bytes.NewBuffer(payload_bytes))
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")

	client := &http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

func ScaleColdStartPod(ctx context.Context, ps *autoscalingv1alpha1.PodScalable, desiredScale int32) error {
	// Create coldstart deployment spec if not exist
	// Update replicas
	coldPodName := fmt.Sprintf("%v-cold", ps.Name)
	ns := ps.Namespace
	c := kubeclient.Get(ctx)
	_, err := c.CoreV1().Pods(ns).Get(ctx, coldPodName, metav1.GetOptions{})
	podExists := true
	if apierrors.IsNotFound(err) {
		// Pod does not exist, create it
		podExists = false
	}
	if desiredScale == 0 {
		// Create pod if not exist
		if !podExists {
			podSpec := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: coldPodName,
					Namespace: ns,
					Labels: ps.Labels,
					Annotations: ps.Annotations,
				},
				Spec: ps.Spec.Template.Spec,
			} 
			_, err := c.CoreV1().Pods(ns).Create(ctx, podSpec, metav1.CreateOptions{})
			if err != nil {
				return err
			}
		}
	} else {
		// Delete pod if exist
		if podExists {
			err := c.CoreV1().Pods(ns).Delete(ctx, coldPodName, metav1.DeleteOptions{})
			if err != nil {
				return err
			}
		}
	}
	return nil
}