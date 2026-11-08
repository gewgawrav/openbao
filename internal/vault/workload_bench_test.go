// Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
// SPDX-License-Identifier: MPL-2.0

package vault

import (
	"encoding/base64"
	"fmt"
	"sync/atomic"
	"testing"

	log "github.com/hashicorp/go-hclog"
	testinginterface "github.com/mitchellh/go-testing-interface"
	"github.com/openbao/openbao/sdk/v2/helper/logging"
	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/openbao/openbao/sdk/v2/physical"
	"github.com/openbao/openbao/v2/internal/builtin/credential/userpass"
	"github.com/openbao/openbao/v2/internal/builtin/logical/pki"
	"github.com/openbao/openbao/v2/internal/builtin/logical/transit"
	"github.com/openbao/openbao/v2/internal/helper/benchhelpers"
	"github.com/openbao/openbao/v2/internal/helper/namespace"
	"github.com/openbao/openbao/v2/internal/helper/testhelpers/corehelpers"
	"github.com/openbao/openbao/v2/internal/physical/inmem"
	"github.com/openbao/openbao/v2/internal/vault/routing"
	"github.com/stretchr/testify/require"
)

func newTestCoreConfigured(t testinginterface.T) (*Core, string) {
	c, _, root := TestCoreUnsealedWithConfig(t, &CoreConfig{
		LogicalBackends: map[string]logical.Factory{
			"transit": transit.Factory,
			"pki":     pki.Factory,
		},
		CredentialBackends: map[string]logical.Factory{
			"userpass": userpass.Factory,
		},
	})
	return c, root
}

// Benchmark 1: Secret Read/Write Operations (KV v1/v2 equivalent in Core)
func BenchmarkWorkload_SecretReadWrite(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	// Write baseline keys
	for i := 0; i < 1000; i++ {
		req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, fmt.Sprintf("secret/key-%d", i))
		req.ClientToken = root
		req.Data = map[string]any{"value": "initial_payload_data_for_benchmarking_128b_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}
		_, err := c.HandleRequest(ctx, req)
		require.NoError(b, err)
	}

	b.Run("Uniform_Read", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var i int
			for pb.Next() {
				i++
				key := fmt.Sprintf("secret/key-%d", i%1000)
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.ReadOperation, key)
				req.ClientToken = root
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || resp == nil || resp.IsError() {
					b.Fatalf("read failed: %v, resp: %v", err, resp)
				}
			}
		})
	})

	b.Run("HotKey_Read", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.ReadOperation, "secret/key-0")
				req.ClientToken = root
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || resp == nil || resp.IsError() {
					b.Fatalf("read failed: %v, resp: %v", err, resp)
				}
			}
		})
	})

	b.Run("Uniform_Write", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		var count atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				idx := count.Add(1)
				key := fmt.Sprintf("secret/bench-write-%d", idx%1000)
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, key)
				req.ClientToken = root
				req.Data = map[string]any{"value": "benchmark_write_payload_data_here"}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("write failed: %v, resp: %v", err, resp)
				}
			}
		})
	})

	b.Run("Contention_HotKey_Write", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "secret/hot-write-key")
				req.ClientToken = root
				req.Data = map[string]any{"value": "hot_contention_payload"}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("write failed: %v, resp: %v", err, resp)
				}
			}
		})
	})
}

// Benchmark 2 & 3 & 4: Transit Encrypt/Decrypt, Batch, Sign/Verify
func BenchmarkWorkload_TransitAll(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	// Mount transit engine
	mountReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "sys/mounts/transit")
	mountReq.ClientToken = root
	mountReq.Data = map[string]any{
		"type": "transit",
	}
	_, err := c.HandleRequest(ctx, mountReq)
	require.NoError(b, err)

	// Create AES-256-GCM key
	createKeyReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/keys/aes-key")
	createKeyReq.ClientToken = root
	createKeyReq.Data = map[string]any{
		"type": "aes256-gcm96",
	}
	_, err = c.HandleRequest(ctx, createKeyReq)
	require.NoError(b, err)

	// Create Ed25519 key for signing
	createSignKeyReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/keys/ed-key")
	createSignKeyReq.ClientToken = root
	createSignKeyReq.Data = map[string]any{
		"type": "ed25519",
	}
	_, err = c.HandleRequest(ctx, createSignKeyReq)
	require.NoError(b, err)

	payloadB64 := base64.StdEncoding.EncodeToString([]byte("The quick brown fox jumps over the lazy dog. 128-byte payload padding test here!"))

	// Pre-encrypt for decryption bench
	encReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/encrypt/aes-key")
	encReq.ClientToken = root
	encReq.Data = map[string]any{"plaintext": payloadB64}
	encResp, err := c.HandleRequest(ctx, encReq)
	require.NoError(b, err)
	ciphertext := encResp.Data["ciphertext"].(string)

	// Pre-sign for verify bench
	signReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/sign/ed-key")
	signReq.ClientToken = root
	signReq.Data = map[string]any{"input": payloadB64}
	signResp, err := c.HandleRequest(ctx, signReq)
	require.NoError(b, err)
	signature := signResp.Data["signature"].(string)

	b.Run("Transit_Encrypt", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/encrypt/aes-key")
				req.ClientToken = root
				req.Data = map[string]any{"plaintext": payloadB64}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("encrypt failed: %v", err)
				}
			}
		})
	})

	b.Run("Transit_Decrypt", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/decrypt/aes-key")
				req.ClientToken = root
				req.Data = map[string]any{"ciphertext": ciphertext}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("decrypt failed: %v", err)
				}
			}
		})
	})

	b.Run("Transit_Batch_Encrypt_10", func(b *testing.B) {
		batchItems := make([]any, 10)
		for i := 0; i < 10; i++ {
			batchItems[i] = map[string]any{"plaintext": payloadB64}
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/encrypt/aes-key")
				req.ClientToken = root
				req.Data = map[string]any{"batch_input": batchItems}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("batch encrypt failed: %v", err)
				}
			}
		})
	})

	b.Run("Transit_Sign_Ed25519", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/sign/ed-key")
				req.ClientToken = root
				req.Data = map[string]any{"input": payloadB64}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("sign failed: %v", err)
				}
			}
		})
	})

	b.Run("Transit_Verify_Ed25519", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "transit/verify/ed-key")
				req.ClientToken = root
				req.Data = map[string]any{"input": payloadB64, "signature": signature}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("verify failed: %v", err)
				}
			}
		})
	})
}

// Benchmark 5: PKI Certificate Issuance
func BenchmarkWorkload_PKI(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	// Mount PKI engine
	mountReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "sys/mounts/pki")
	mountReq.ClientToken = root
	mountReq.Data = map[string]any{"type": "pki"}
	_, err := c.HandleRequest(ctx, mountReq)
	require.NoError(b, err)

	// Generate Root CA
	genCAReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "pki/root/generate/internal")
	genCAReq.ClientToken = root
	genCAReq.Data = map[string]any{
		"common_name": "OpenBao Benchmarking Root CA",
		"ttl":         "8760h",
		"key_type":    "ec",
		"key_bits":    256,
	}
	_, err = c.HandleRequest(ctx, genCAReq)
	require.NoError(b, err)

	// Create PKI role
	roleReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "pki/roles/bench-role")
	roleReq.ClientToken = root
	roleReq.Data = map[string]any{
		"allowed_domains":  []string{"example.com"},
		"allow_subdomains": true,
		"max_ttl":          "72h",
		"key_type":         "ec",
		"key_bits":         256,
	}
	_, err = c.HandleRequest(ctx, roleReq)
	require.NoError(b, err)

	b.Run("PKI_Issue_EC256", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		var counter atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				idx := counter.Add(1)
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "pki/issue/bench-role")
				req.ClientToken = root
				req.Data = map[string]any{
					"common_name": fmt.Sprintf("service-%d.example.com", idx),
					"ttl":         "1h",
				}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("pki issue failed: %v", err)
				}
			}
		})
	})
}

// Benchmark 6: Authentication (Userpass / AppRole)
func BenchmarkWorkload_Authentication(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	// Mount userpass auth
	mountReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "sys/auth/userpass")
	mountReq.ClientToken = root
	mountReq.Data = map[string]any{"type": "userpass"}
	_, err := c.HandleRequest(ctx, mountReq)
	require.NoError(b, err)

	// Create user
	userReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "auth/userpass/users/benchuser")
	userReq.ClientToken = root
	userReq.Data = map[string]any{
		"password": "benchmarkpassword123!",
		"policies": []string{"default"},
	}
	_, err = c.HandleRequest(ctx, userReq)
	require.NoError(b, err)

	b.Run("Userpass_Login", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "auth/userpass/login/benchuser")
				req.Data = map[string]any{"password": "benchmarkpassword123!"}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("login failed: %v", err)
				}
			}
		})
	})
}

// Benchmark 7 & 8: Token Lookup and Policy Evaluation
func BenchmarkWorkload_TokenLookupAndPolicy(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	// Create test token via Core request
	createReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "auth/token/create")
	createReq.ClientToken = root
	createReq.Data = map[string]any{
		"policies": []string{"default"},
		"ttl":      "24h",
	}
	resp, err := c.HandleRequest(ctx, createReq)
	require.NoError(b, err)
	testToken := resp.Auth.ClientToken

	b.Run("Token_Lookup_Self", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.ReadOperation, "auth/token/lookup-self")
				req.ClientToken = testToken
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("lookup failed: %v", err)
				}
			}
		})
	})

	b.Run("Policy_Evaluate_ACL", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.ReadOperation, "sys/internal/ui/mounts")
				req.ClientToken = testToken
				_, _ = c.HandleRequest(ctx, req)
			}
		})
	})
}

// Benchmark 9: Lease Creation and Renewal
func BenchmarkWorkload_LeaseRenewal(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	b.Run("Token_Renew_Self", func(b *testing.B) {
		createReq := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "auth/token/create")
		createReq.ClientToken = root
		createReq.Data = map[string]any{
			"policies":  []string{"default"},
			"ttl":       "1h",
			"renewable": true,
		}
		resp, err := c.HandleRequest(ctx, createReq)
		require.NoError(b, err)
		token := resp.Auth.ClientToken

		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.UpdateOperation, "auth/token/renew-self")
				req.ClientToken = token
				req.Data = map[string]any{"increment": "1h"}
				resp, err := c.HandleRequest(ctx, req)
				if err != nil || (resp != nil && resp.IsError()) {
					b.Fatalf("renew failed: %v", err)
				}
			}
		})
	})
}

// Benchmark 10: Audit Logging
func BenchmarkWorkload_AuditLogging(b *testing.B) {
	c, root := newTestCoreConfigured(benchhelpers.TBtoT(b))
	ctx := namespace.RootContext(b.Context())

	c.auditBackends["noop"] = corehelpers.NoopAuditFactory(nil)
	me := &routing.MountEntry{
		Table: auditTableType,
		Path:  "noop-audit",
		Type:  "noop",
	}
	err := c.enableAudit(ctx, me)
	require.NoError(b, err)

	b.Run("Secret_Read_With_Audit", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				req := logical.TestRequest(benchhelpers.TBtoT(b), logical.ReadOperation, "secret/key-0")
				req.ClientToken = root
				_, _ = c.HandleRequest(ctx, req)
			}
		})
	})
}

// Benchmark 11 & 12: Integrated Storage & Physical backend
func BenchmarkWorkload_IntegratedStorageAndBarrier(b *testing.B) {
	logger := logging.NewVaultLogger(log.Error)
	inm, err := inmem.NewInmem(nil, logger)
	require.NoError(b, err)

	b.Run("Physical_Put_Direct", func(b *testing.B) {
		ctx := b.Context()
		b.ReportAllocs()
		b.ResetTimer()
		var cnt atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				idx := cnt.Add(1)
				key := fmt.Sprintf("bench/%d", idx%5000)
				entry := &physical.Entry{
					Key:   key,
					Value: []byte("storage_entry_value_512b_padding_for_barrier_and_raft_wal_benchmarks_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"),
				}
				if err := inm.Put(ctx, entry); err != nil {
					b.Fatalf("put failed: %v", err)
				}
			}
		})
	})

	b.Run("Physical_Get_Direct", func(b *testing.B) {
		ctx := b.Context()
		// Seed entries
		for i := 0; i < 1000; i++ {
			_ = inm.Put(ctx, &physical.Entry{
				Key:   fmt.Sprintf("bench/%d", i),
				Value: []byte("sample"),
			})
		}
		b.ReportAllocs()
		b.ResetTimer()
		var cnt atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				idx := cnt.Add(1)
				key := fmt.Sprintf("bench/%d", idx%1000)
				if _, err := inm.Get(ctx, key); err != nil {
					b.Fatalf("get failed: %v", err)
				}
			}
		})
	})
}
