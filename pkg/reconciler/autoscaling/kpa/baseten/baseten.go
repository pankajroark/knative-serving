package baseten

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"knative.dev/pkg/logging"
)


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

func postJsonBaseten(ctx context.Context, path string, payload interface{}) ([]byte, error) {
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
