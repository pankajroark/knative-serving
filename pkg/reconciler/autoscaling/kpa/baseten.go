package kpa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"knative.dev/pkg/logging"
	autoscalingv1alpha1 "knative.dev/serving/pkg/apis/autoscaling/v1alpha1"
)


func InformBaseten(ctx context.Context, ps *autoscalingv1alpha1.PodScalable) (error) {
		// Scaling up from zero
		// todo: Access the url from configmap here and call that api with details
		// 1. ksvc name
		// 2. namespace
		// 3. revision id
		logger := logging.FromContext(ctx)
		pod_json, err := json.Marshal(ps)
    if err != nil {
        fmt.Println(err)
    }
		logger.Infof("Pankaj: Scaling up from zero. Scale target %s", string(pod_json))
		url := "http://baseten-django.baseten:8000/notify_scale_from_zero"
		request, err := http.NewRequest("POST", url, bytes.NewBuffer(pod_json))
		request.Header.Set("Content-Type", "application/json; charset=UTF-8")

		client := &http.Client{}
		response, err := client.Do(request)
		if err != nil {
			fmt.Println((err))
		}
		defer response.Body.Close()

		return err
}
