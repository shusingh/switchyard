// Package configs holds example configuration files. This test keeps them
// loadable as the configuration schema evolves.
package configs

import (
	"path/filepath"
	"testing"

	"github.com/shusingh/switchyard/internal/config"
)

func TestExampleConfigsLoad(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no example configs found")
	}
	for _, f := range files {
		if _, err := config.Load(f); err != nil {
			t.Errorf("config.Load(%s) error = %v", f, err)
		}
	}
}
