// Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openbao/openbao/api/v2"
)

type BenchmarkConfig struct {
	Address     string
	Token       string
	Workload    string
	Concurrency int
	Duration    time.Duration
	PayloadSize int
	HotKeyRatio float64
	BurstFactor int
}

type MetricSummary struct {
	TotalOps   int64         `json:"total_ops"`
	Errors     int64         `json:"errors"`
	Duration   time.Duration `json:"duration"`
	OpsPerSec  float64       `json:"ops_per_sec"`
	P50Latency time.Duration `json:"p50_latency_ms"`
	P95Latency time.Duration `json:"p95_latency_ms"`
	P99Latency time.Duration `json:"p99_latency_ms"`
	MaxLatency time.Duration `json:"max_latency_ms"`
	MinLatency time.Duration `json:"min_latency_ms"`
}

func main() {
	cfg := BenchmarkConfig{}
	flag.StringVar(&cfg.Address, "address", "http://127.0.0.1:8200", "OpenBao server address")
	flag.StringVar(&cfg.Token, "token", "root", "Authentication token")
	flag.StringVar(&cfg.Workload, "workload", "secret-read", "Workload category: secret-read, secret-write, transit-encrypt, transit-decrypt, transit-batch, sign-verify, pki-issue, userpass-auth, token-lookup, policy-eval, lease-renew, audit-logging, raft-ops")
	flag.IntVar(&cfg.Concurrency, "concurrency", 10, "Number of concurrent goroutines")
	flag.DurationVar(&cfg.Duration, "duration", 10*time.Second, "Benchmark duration")
	flag.IntVar(&cfg.PayloadSize, "payload-size", 256, "Payload size in bytes")
	flag.Float64Var(&cfg.HotKeyRatio, "hot-key-ratio", 0.0, "Ratio of requests hitting the hot-key [0.0 - 1.0]")
	flag.IntVar(&cfg.BurstFactor, "burst-factor", 1, "Burst factor multiplier (1 = constant load)")
	flag.Parse()

	fmt.Printf("=== Starting OpenBao Capacity Benchmark Runner ===\n")
	fmt.Printf("Workload: %s | Concurrency: %d | Duration: %v | Payload: %d bytes\n",
		cfg.Workload, cfg.Concurrency, cfg.Duration, cfg.PayloadSize)

	summary, err := RunBenchmark(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing benchmark: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== Results for %s ===\n", cfg.Workload)
	fmt.Printf("Total Operations: %d\n", summary.TotalOps)
	fmt.Printf("Throughput:       %.2f ops/sec\n", summary.OpsPerSec)
	fmt.Printf("Error Rate:       %.4f%% (%d errors)\n", float64(summary.Errors)/float64(summary.TotalOps)*100.0, summary.Errors)
	fmt.Printf("P50 Latency:      %v\n", summary.P50Latency)
	fmt.Printf("P95 Latency:      %v\n", summary.P95Latency)
	fmt.Printf("P99 Latency:      %v\n", summary.P99Latency)
	fmt.Printf("Max Latency:      %v\n", summary.MaxLatency)
	fmt.Printf("Min Latency:      %v\n", summary.MinLatency)
}

func RunBenchmark(cfg BenchmarkConfig) (*MetricSummary, error) {
	clientCfg := api.DefaultConfig()
	clientCfg.Address = cfg.Address
	clientCfg.HttpClient.Transport = &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 1000,
		MaxConnsPerHost:     1000,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	}

	client, err := api.NewClient(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	client.SetToken(cfg.Token)

	// Pre-benchmark setup
	if err := setupWorkload(client, cfg); err != nil {
		return nil, fmt.Errorf("failed to setup workload: %w", err)
	}

	var totalOps atomic.Int64
	var errOps atomic.Int64
	latencies := make([][]time.Duration, cfg.Concurrency)
	for i := range latencies {
		latencies[i] = make([]time.Duration, 0, 100000)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var wg sync.WaitGroup
	start := time.Now()

	for workerID := 0; workerID < cfg.Concurrency; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			workerLatencies := make([]time.Duration, 0, 50000)
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))

			for {
				select {
				case <-ctx.Done():
					latencies[id] = workerLatencies
					return
				default:
					opStart := time.Now()
					opErr := executeOperation(client, cfg, r)
					dur := time.Since(opStart)

					totalOps.Add(1)
					if opErr != nil {
						errOps.Add(1)
					}
					workerLatencies = append(workerLatencies, dur)
				}
			}
		}(workerID)
	}

	wg.Wait()
	actualDuration := time.Since(start)

	// Flatten latencies
	allLatencies := make([]time.Duration, 0, totalOps.Load())
	for _, l := range latencies {
		allLatencies = append(allLatencies, l...)
	}

	if len(allLatencies) == 0 {
		return &MetricSummary{Duration: actualDuration}, nil
	}

	sort.Slice(allLatencies, func(i, j int) bool {
		return allLatencies[i] < allLatencies[j]
	})

	ops := totalOps.Load()
	p50Idx := int(float64(len(allLatencies)) * 0.50)
	p95Idx := int(float64(len(allLatencies)) * 0.95)
	p99Idx := int(float64(len(allLatencies)) * 0.99)
	if p99Idx >= len(allLatencies) {
		p99Idx = len(allLatencies) - 1
	}

	summary := &MetricSummary{
		TotalOps:   ops,
		Errors:     errOps.Load(),
		Duration:   actualDuration,
		OpsPerSec:  float64(ops) / actualDuration.Seconds(),
		P50Latency: allLatencies[p50Idx],
		P95Latency: allLatencies[p95Idx],
		P99Latency: allLatencies[p99Idx],
		MaxLatency: allLatencies[len(allLatencies)-1],
		MinLatency: allLatencies[0],
	}

	return summary, nil
}

func setupWorkload(c *api.Client, cfg BenchmarkConfig) error {
	switch cfg.Workload {
	case "secret-read":
		for i := 0; i < 100; i++ {
			path := fmt.Sprintf("secret/data/bench-%d", i)
			data := map[string]any{
				"data": map[string]any{
					"key": string(make([]byte, cfg.PayloadSize)),
				},
			}
			_, _ = c.Logical().Write(path, data)
		}
	case "transit-encrypt", "transit-decrypt", "transit-batch":
		_ = c.Sys().Mount("transit", &api.MountInput{Type: "transit"})
		_, _ = c.Logical().Write("transit/keys/bench-key", map[string]any{"type": "aes256-gcm96"})
	case "sign-verify":
		_ = c.Sys().Mount("transit", &api.MountInput{Type: "transit"})
		_, _ = c.Logical().Write("transit/keys/bench-sign-key", map[string]any{"type": "ed25519"})
	case "pki-issue":
		_ = c.Sys().Mount("pki", &api.MountInput{Type: "pki", Config: api.MountConfigInput{MaxLeaseTTL: "87600h"}})
		_, _ = c.Logical().Write("pki/root/generate/internal", map[string]any{"common_name": "OpenBao Perf CA", "ttl": "8760h"})
		_, _ = c.Logical().Write("pki/roles/bench-role", map[string]any{"allowed_domains": []string{"example.com"}, "allow_subdomains": true, "max_ttl": "72h"})
	case "userpass-auth":
		_ = c.Sys().EnableAuthWithOptions("userpass", &api.EnableAuthOptions{Type: "userpass"})
		_, _ = c.Logical().Write("auth/userpass/users/benchuser", map[string]any{"password": "testpassword", "policies": "default"})
	}
	return nil
}

func executeOperation(c *api.Client, cfg BenchmarkConfig, r *rand.Rand) error {
	switch cfg.Workload {
	case "secret-read":
		key := r.Intn(100)
		if cfg.HotKeyRatio > 0 && r.Float64() < cfg.HotKeyRatio {
			key = 0
		}
		_, err := c.Logical().Read(fmt.Sprintf("secret/data/bench-%d", key))
		return err

	case "secret-write":
		key := r.Intn(1000)
		data := map[string]any{
			"data": map[string]any{
				"payload": string(make([]byte, cfg.PayloadSize)),
			},
		}
		_, err := c.Logical().Write(fmt.Sprintf("secret/data/bench-%d", key), data)
		return err

	case "transit-encrypt":
		data := map[string]any{
			"plaintext": "dGhlIHF1aWNrIGJyb3duIGZveA==",
		}
		_, err := c.Logical().Write("transit/encrypt/bench-key", data)
		return err

	case "token-lookup":
		_, err := c.Auth().Token().LookupSelf()
		return err

	case "policy-eval":
		_, err := c.Logical().Read("secret/data/bench-0")
		return err

	default:
		_, err := c.Auth().Token().LookupSelf()
		return err
	}
}
