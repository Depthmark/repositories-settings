package applier

import "testing"

// TestRouteLabelsMatchRequestedPaths reports anything the lane tests
// mislabelled.
//
// It lives in a file that sorts last because `go test` runs tests in
// source order, files alphabetically — so this only sees a complete
// picture from here. Naming it ZZ inside lanes_test.go would not have
// worked: the position in the file is what decides, not the name.
func TestRouteLabelsMatchRequestedPaths(t *testing.T) {
	routeMismatches.Range(func(path, route any) bool {
		t.Errorf("request to %q was labelled with route %q", path, route)
		return true
	})
}
