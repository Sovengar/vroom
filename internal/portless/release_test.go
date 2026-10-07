package portless_test

import (
	"errors"
	"testing"
	"time"

	"vroom/internal/portless"
)

type recordingReleaser struct {
	removed []string
	err     error
}

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return r.err
}

var errRemoveFailed = errors.New("portless alias --remove exited 1")

func TestReleaseRemovesThroughTheSeam(t *testing.T) {
	rec := &recordingReleaser{}
	portless.Release(rec, "mi-ruta")

	if len(rec.removed) != 1 || rec.removed[0] != "mi-ruta" {
		t.Errorf("Release must remove exactly the given route, got %v", rec.removed)
	}
}

func TestReleaseSkipsEmptyName(t *testing.T) {
	rec := &recordingReleaser{}
	portless.Release(rec, "")

	if len(rec.removed) != 0 {
		t.Errorf("without a name there is nothing to remove, got %v", rec.removed)
	}
}

// MEASURED (M10): removing a missing route exits 1, so a repeated stop must not be an error and Release returns nothing.
func TestReleaseSwallowsErrors(t *testing.T) {
	rec := &recordingReleaser{err: errRemoveFailed}
	portless.Release(rec, "mi-ruta")

	if len(rec.removed) != 1 {
		t.Errorf("the removal must be attempted even if it fails, got %v", rec.removed)
	}
}

// nil means "build the real client", which would resolve the developer's portless and state dir; with an empty name it must not get that far, so a test that forgets the seam cannot mutate another routes.json.
func TestReleaseWithNilSeamAndEmptyNameIsInert(t *testing.T) {
	portless.Release(nil, "")
}

func TestReleaseIntegrationRemovesForReal(t *testing.T) {
	iso := integrationStateDir(t)

	// 10*time.Second, not a bare 10: the value is nanoseconds and a 10ns deadline expires before the binary starts, skipping the test with a false "no response".
	c := portless.New(
		portless.WithBinary(integrationBin(t)),
		portless.WithStateDir(iso),
		portless.WithTimeout(10*time.Second),
	)
	if c.Apply("vroom.release", 4321, portless.Ownership{}).Succeeded() {
		t.Log("there is a proxy running: the route was registered and will be verified")
	}
	if _, found, err := c.Lookup("vroom.release"); err != nil || !found {
		t.Skipf("portless does not respond as expected: %v", err)
	}

	portless.Release(c, "vroom.release")
	if _, found, _ := c.Lookup("vroom.release"); found {
		t.Error("after removal the route must disappear")
	}
	portless.Release(c, "vroom.release")
}
