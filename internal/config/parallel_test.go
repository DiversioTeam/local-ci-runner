package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadParallelismAndTimeoutContracts(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		parallel     string
		timeout      string
		wantError    string
		wantParallel int
	}{
		{name: "legacy defaults", wantParallel: 1},
		{name: "parallel with timeout", parallel: "max_parallel = 3", timeout: "10m", wantParallel: 3},
		{name: "zero", parallel: "max_parallel = 0", wantError: "positive integer"},
		{name: "negative", parallel: "max_parallel = -1", wantError: "positive integer"},
		{name: "wrong table", parallel: "[github]\nmax_parallel = 2", wantError: "unknown fields"},
		{name: "zero timeout", timeout: "0s", wantError: "positive duration"},
		{name: "negative timeout", timeout: "-1s", wantError: "positive duration"},
		{name: "invalid timeout", timeout: "tomorrow", wantError: "positive duration"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			content := fmt.Sprintf("version = 1\n%s\n[[steps]]\nid = 'test'\ncommand = ['true']\ntimeout = %q\n", testCase.parallel, testCase.timeout)
			cfg, err := Load(writeTempConfig(t, content))
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("error = %v, want %s", err, testCase.wantError)
				}
				return
			}
			if err != nil || cfg.MaxParallel != testCase.wantParallel {
				t.Fatalf("parallel = %d, error = %v", cfg.MaxParallel, err)
			}
			if cfg.StaticPlan().Steps[0].Timeout != testCase.timeout {
				t.Fatal("static plan dropped the step timeout")
			}
		})
	}
}
