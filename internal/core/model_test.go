package core

import (
	"slices"
	"testing"
)

func TestEndpointValidation(t *testing.T) {
	c := Connection{ID: "primary", Name: "Primary", Provider: R2}
	for _, endpoint := range []string{"", "https://example.test", "http://127.0.0.1:9000/", "https://[::1]"} {
		c.Endpoint = endpoint
		if err := c.Validate(); err != nil {
			t.Errorf("rejected origin %q", endpoint)
		}
	}
	for _, endpoint := range []string{"ftp://example.test", "https://example.test/bucket", "https://user@example.test", "https://example.test/?signature=example", "https://example.test/#fragment", "example.test"} {
		c.Endpoint = endpoint
		if c.Validate() == nil {
			t.Errorf("accepted invalid endpoint %q", endpoint)
		}
	}
}

func TestStateTransitions(t *testing.T) {
	states := []TaskState{Queued, Running, Paused, Failed, Completed, Canceled}
	allowed := map[TaskState][]TaskState{
		Queued:  {Running, Paused, Canceled},
		Running: {Paused, Failed, Completed, Canceled},
		Paused:  {Queued, Canceled},
		Failed:  {Queued, Canceled},
	}
	for _, from := range states {
		for _, to := range states {
			if from.CanTransition(to) != slices.Contains(allowed[from], to) {
				t.Errorf("%s -> %s", from, to)
			}
		}
	}
}
