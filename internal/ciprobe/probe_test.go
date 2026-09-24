package ciprobe

import "testing"

// TestAlwaysFails is a deliberately failing test used to prove that the CI
// gate blocks merges into main. This package is throwaway.
func TestAlwaysFails(t *testing.T) {
	t.Fatal("intentional probe failure: the CI gate must block this merge")
}
