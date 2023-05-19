package baseten

import (
	"context"

	autoscalingv1alpha1 "knative.dev/serving/pkg/apis/autoscaling/v1alpha1"
)

type ColdBooster struct {
}

func (c *ColdBooster) Inform(ctx context.Context, currentScale int32, desiredScale int32, ps *autoscalingv1alpha1.PodScalable) error {
	if currentScale == desiredScale {
		return nil
	}

	if desiredScale == 0 {
		// Scale to zero
	}
	if currentScale == 0 {
		// Scale from zero
	}
	return nil
}

func podScalableToColdStartPodSpec(ps *autoscalingv1alpha1.PodScalable) -> PodSpec
