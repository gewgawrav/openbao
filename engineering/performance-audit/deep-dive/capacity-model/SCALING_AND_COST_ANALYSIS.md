# Infrastructure Scaling & Total Cost of Ownership (TCO) 🤖

- **Audit Classification:** `STATIC-ANALYSIS-CONFIRMED` / `INFERRED`

## Executive Summary
This report analyzes cloud infrastructure costs (AWS / GCP / Bare Metal) and scaling economics for the Cloakit Privacy Vault when using OpenBao as a Layer 2 Subject Key management engine.

---

## 1. Cost Comparison Across Growth Milestones

```
+------------------------------------------------------------------------------------------------------+
| Cardinality | Infrastructure Topology                              | Monthly Cloud Cost (Est.)       |
+------------------------------------------------------------------------------------------------------+
| 10K - 100K  | 2x OpenBao (4 vCPU / 8GB) + AWS RDS db.m6g.large     | ~$380 / month                   |
| 1M Keys     | 3x OpenBao (8 vCPU / 16GB) + AWS RDS db.m6g.xlarge   | ~$950 / month                   |
| 10M Keys    | 5x OpenBao (16 vCPU / 32GB) + AWS RDS db.r6g.2xlarge | ~$2,650 / month                 |
+------------------------------------------------------------------------------------------------------+
```

### 1.1 Envelope Encryption Efficiency & Cloud KMS Cost Elimination
- Because OpenBao executes AES-256-GCM locally using host CPU instructions, there are **$0.00$ external Cloud KMS API costs** for DEK wrapping and unwrapping.
- Cloud KMS providers (AWS KMS, Google Cloud KMS) charge $0.03 per 10,000 API operations.
- For Cloakit processing 10,000 operations/sec ($26 \text{ billion ops/month}$):
  $$\text{Direct AWS KMS Cost} = \frac{26,000,000,000}{10,000} \times \$0.03 = \mathbf{\$78,000 \text{ per month}}.$$
- By deploying OpenBao as Layer 2 envelope engine, Cloakit spends ~\$950/month in compute, delivering an immediate **>98% infrastructure cost reduction** while retaining complete cryptographic tenant isolation.
