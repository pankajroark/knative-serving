/*
Copyright 2020 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package max

import (
	"math"
	"time"
)

const maxColdBoosts = 600

// Boost window keeps track of boosts done
type BoostWindow struct {
	entries []time.Time
	duration time.Duration
}

// NewBoostWindow creates a new BoostWindow.
func NewBoostWindow(duration time.Duration) *BoostWindow {
	return &BoostWindow{entries: make([]time.Time, maxColdBoosts), duration: duration}
}

// Record records a value in the bucket derived from the given time.
func (b *BoostWindow) Record(now time.Time) {
	b.cleanup();
	b.entries = append(b.entries, now)
}

// Current returns the current maximum value observed in the previous
// window duration.
func (b *BoostWindow) Count() int {
	b.cleanup()
	return len(b.entries)
}

func (b *BoostWindow) cleanup() {
	cutoffIdx := b.cutoffIndex()
	if cutoffIdx != -1 {
		b.entries = b.entries[cutoffIdx:]
	}
}

func (b *BoostWindow) cutoffIndex() int {
	cutoff := time.Now().Add(-b.duration)
	for i, v := range b.entries {
		if v.After(cutoff) {
			return i
		}
	}
	return -1
}

