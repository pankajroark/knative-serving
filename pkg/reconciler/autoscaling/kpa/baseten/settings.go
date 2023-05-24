package baseten

import (
	"context"
	"time"
)

type ServiceKey struct {
	serviceName string
	namespace string
}

type ColdStartSettings struct {
	Enabled bool `json:"enabled"`
	// NodeSelector map[string]string
	PodSpecJsonPatch string `json:"pod_spec_json_patch"`
}


type CachedColdStartSettings struct {
	settings ColdStartSettings
	cachedAt time.Time
}

// TODO(pankaj) Get this from ConfigMap
const cacheEnabled = true  
const coldStartSettingsTTL = 5 * time.Minute
var settingsCache = map[ServiceKey]CachedColdStartSettings{}

func GetColdstartSettings(ctx context.Context, namespace, service string) (*ColdStartSettings, error) {
	if !cacheEnabled {
		return FetchColdstartSettings(ctx, namespace, service)
	}

	now := time.Now()
	key := ServiceKey{
		serviceName: service,
		namespace: namespace,
	}
	val, ok := settingsCache[key]
	if !ok || val.cachedAt.Add(coldStartSettingsTTL).Before(now){
		settings, err := FetchColdstartSettings(ctx, namespace, service)
		if err != nil {
			return nil, err
		}
		settingsCache[key] = CachedColdStartSettings{
			settings: *settings,
			cachedAt: now,
		}
		return settings, err
	}

	return &val.settings, nil
}