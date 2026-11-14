//go:build lockfamily_managed_test

package managed

import (
	"os"

	km "github.com/DavidGodefroid/lockkit/managed"
)

// TestPathVar names the managed file a test build reads instead of the machine's, without the
// ownership check (lockkit's SetTestPath). It exists only in a build with the
// lockfamily_managed_test tag: a release build has no way to move the managed file.
const TestPathVar = "LEA_TEST_MANAGED"

func init() {
	if p := os.Getenv(TestPathVar); p != "" {
		km.SetTestPath(p)
	}
}
