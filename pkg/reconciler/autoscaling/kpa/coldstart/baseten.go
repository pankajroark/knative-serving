package coldstart

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

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
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)

}