package manifest

import "testing"

func TestHealthURLPath(t *testing.T) {
	if got := (&Manifest{}).HealthURLPath(); got != "/" {
		t.Errorf("default HealthURLPath = %q, want /", got)
	}
	if got := (&Manifest{HealthPath: "/healthz"}).HealthURLPath(); got != "/healthz" {
		t.Errorf("HealthURLPath = %q, want /healthz", got)
	}
}
