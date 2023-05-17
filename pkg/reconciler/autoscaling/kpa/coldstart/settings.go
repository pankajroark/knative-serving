package coldstart

import (
	"context"
	"time"
)

type RevisionKey struct {
	revisionName string
	namespace string
}

type ColdStartSettings struct {
	ColdStartEnabled bool `json:"cold_start_enabled"`
	// NodeSelector map[string]string
}


type CachedColdStartSettings struct {
	settings ColdStartSettings
	cachedAt time.Time
}

const coldStartSettingsTTL = 5 * time.Minute
var settingsCache = map[RevisionKey]CachedColdStartSettings{}

func GetColdstartSettings(ctx context.Context, namespace, revision string) (*ColdStartSettings, error) {
	now := time.Now()
	key := RevisionKey{
		revisionName: revision,
		namespace: namespace,
	}
	val, ok := settingsCache[key]
	if !ok || val.cachedAt.Add(coldStartSettingsTTL).Before(now){
		settings, err := FetchColdstartSettings(ctx, namespace, revision)
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