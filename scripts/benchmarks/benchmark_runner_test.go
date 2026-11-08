// Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBenchmarkConfigValidation(t *testing.T) {
	cfg := BenchmarkConfig{
		Address:     "http://127.0.0.1:8200",
		Token:       "root",
		Workload:    "token-lookup",
		Concurrency: 2,
		Duration:    100 * time.Millisecond,
		PayloadSize: 64,
	}

	require.Equal(t, "token-lookup", cfg.Workload)
	require.Equal(t, 2, cfg.Concurrency)
}
