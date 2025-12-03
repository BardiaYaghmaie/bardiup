# Bardiup Roadmap

This document outlines the planned features and development direction for Bardiup. Features are organized by priority and estimated timeline.

> **Note:** This roadmap is subject to change based on community feedback and contributions. Want to influence priorities? Open an issue or contribute!

---

## Current Version (v0.1.x)

### ✅ Available Now

| Feature | Description |
|---------|-------------|
| **Dynamic Retention Policies** | GFS-style retention with daily/weekly/monthly/yearly/custom tiers |
| **Selection Strategies** | newest, oldest, distributed backup selection |
| **S3-Compatible Storage** | AWS S3, MinIO, and S3-compatible services |
| **Copy-Based Backups** | Full PVC backups via Kubernetes Jobs |
| **Label/Name Selection** | Select PVCs by labels or specific names |
| **Cron Scheduling** | Standard cron expressions for scheduling |
| **Pause/Resume** | Ability to pause and resume backup schedules |
| **Status Tracking** | Track backup status, last/next backup times |

---

## Short-Term (v0.2.x) - Q1 2026

### 🔐 Security & Encryption

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Client-side Encryption** | 🔴 High | Encrypt backups before uploading to S3 |
| **Encryption Key Management** | 🔴 High | Support for Kubernetes secrets and external KMS |
| **TLS Verification Options** | 🟡 Medium | Configurable TLS settings for S3 endpoints |

**Why this matters:** Enterprise adoption requires encryption at rest. Many compliance frameworks (SOC2, HIPAA, GDPR) require encrypted backups.

```yaml
# Planned API
spec:
  encryption:
    enabled: true
    algorithm: AES-256-GCM
    keySecret: encryption-key
    # Or use external KMS
    kmsKeyId: arn:aws:kms:us-west-2:111122223333:key/...
```

### 📊 Monitoring & Observability

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Prometheus Metrics** | 🔴 High | Export backup/retention metrics |
| **Backup Failure Alerts** | 🔴 High | Alerting integration for failed backups |
| **Grafana Dashboard** | 🟡 Medium | Pre-built dashboard for visualization |

**Planned metrics:**
- `bardiup_backups_total` - Total backups created (by config, status)
- `bardiup_backup_duration_seconds` - Backup duration histogram
- `bardiup_backup_size_bytes` - Backup sizes
- `bardiup_retention_deleted_total` - Backups deleted by retention
- `bardiup_backup_last_success_timestamp` - Last successful backup time
- `bardiup_storage_used_bytes` - Total storage usage per config

### 🪝 Application-Aware Backups

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Pre-backup Hooks** | 🔴 High | Run commands before backup (e.g., database flush) |
| **Post-backup Hooks** | 🔴 High | Run commands after backup completion |
| **Hook Timeout/Retry** | 🟡 Medium | Configurable timeouts and retry logic |

**Why this matters:** Consistent backups require applications to be in a known state. Databases need to flush buffers, applications may need to pause writes.

```yaml
# Planned API
spec:
  hooks:
    preBackup:
      - name: flush-postgres
        container: postgres
        command: ["pg_dump", "--format=custom", "-f", "/backup/dump.sql"]
        timeout: 300s
    postBackup:
      - name: notify-slack
        container: notifier
        command: ["/notify.sh", "backup-complete"]
```

---

## Medium-Term (v0.3.x) - Q2 2026

### 📈 Incremental Backups

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Incremental Backup Support** | 🔴 High | Only backup changed files since last backup |
| **Full + Incremental Chains** | 🔴 High | Periodic full backups with incremental chain |
| **Chain Management** | 🟡 Medium | Automatic management of backup chains |

**Why this matters:** Full backups of large PVCs are slow and expensive. Incremental backups can reduce:
- Backup time by 80-90%
- Storage costs by 60-80%
- Network transfer by 80-90%

```yaml
# Planned API
spec:
  backupMethod: incremental
  incrementalConfig:
    fullBackupSchedule: "0 0 * * 0"  # Full backup weekly
    maxIncrementalChain: 7           # Max incrementals before full
```

### 🗜️ Compression Options

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Compression Selection** | 🟡 Medium | Choose compression algorithm |
| **Compression Levels** | 🟢 Low | Configurable compression levels |

**Planned options:**
- `gzip` (current default)
- `zstd` (faster, better compression)
- `lz4` (fastest)
- `none` (for pre-compressed data)

```yaml
# Planned API
spec:
  compression:
    algorithm: zstd
    level: 3  # 1-19, higher = smaller but slower
```

### ☁️ Additional Storage Backends

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Azure Blob Storage** | 🟡 Medium | Native Azure Blob support |
| **Google Cloud Storage** | 🟡 Medium | Native GCS support |
| **Generic HTTP** | 🟢 Low | Any HTTP-compatible storage |

```yaml
# Planned API - Azure
spec:
  storageBackend:
    type: azure
    azure:
      container: backups
      accountName: myaccount
      credentialsSecret: azure-credentials

# Planned API - GCS
spec:
  storageBackend:
    type: gcs
    gcs:
      bucket: my-backup-bucket
      project: my-project
      credentialsSecret: gcs-credentials
```

---

## Long-Term (v0.4.x+) - Q3-Q4 2026

### ✅ Backup Verification

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Automated Restore Testing** | 🟡 Medium | Periodic restore to verify backups |
| **Integrity Checksums** | 🟡 Medium | SHA-256 checksums for all backups |
| **Verification Reports** | 🟢 Low | Reports on backup health |

```yaml
# Planned API
spec:
  verification:
    enabled: true
    schedule: "0 4 * * 0"  # Weekly verification
    restoreTestPVC: verify-test-pvc
    checksumValidation: true
```

### 📸 VolumeSnapshot Integration

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **CSI Snapshot Backups** | 🟡 Medium | Use VolumeSnapshots instead of copy |
| **Snapshot-to-S3 Pipeline** | 🟡 Medium | Convert snapshots to S3 archives |
| **Cross-AZ Snapshots** | 🟢 Low | Multi-AZ snapshot support |

### 🔄 Restore Automation

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **RestoreConfig CRD** | 🟡 Medium | Declarative restore configuration |
| **Point-in-Time UI** | 🟢 Low | Select restore point from timeline |
| **Cross-Cluster Restore** | 🟡 Medium | Built-in cross-cluster restore |

```yaml
# Planned API
apiVersion: bardiup.io/v1alpha1
kind: RestoreConfig
metadata:
  name: restore-postgres
spec:
  sourceBackup: postgres-backup-20240115
  # Or select by time
  pointInTime: "2024-01-15T10:00:00Z"
  targetPVC:
    name: postgres-data-restored
    create: true
    storageClass: fast-ssd
  postRestore:
    scaleDeployment: postgres
    replicas: 1
```

### 💰 Cost Management

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Storage Cost Estimation** | 🟢 Low | Estimate storage costs per config |
| **Cost Optimization Tips** | 🟢 Low | Suggestions to reduce storage costs |
| **Budget Alerts** | 🟢 Low | Alert when storage costs exceed threshold |

### 🖥️ Web Dashboard

| Feature | Priority | Description |
|---------|:--------:|-------------|
| **Basic Dashboard** | 🟢 Low | View backups, configs, status |
| **Restore UI** | 🟢 Low | Point-and-click restore |
| **Analytics** | 🟢 Low | Backup trends, storage growth |

---

## Community Requested Features

These features have been requested by the community but are not yet scheduled:

| Feature | Votes | Discussion |
|---------|:-----:|------------|
| Multi-cluster management | - | [#TBD]() |
| Backup scheduling windows | - | [#TBD]() |
| Bandwidth throttling | - | [#TBD]() |
| Deduplication | - | [#TBD]() |
| Backup catalog/search | - | [#TBD]() |

**Want to request a feature?** Open a GitHub issue with the `enhancement` label!

---

## Version History

| Version | Date | Highlights |
|---------|------|------------|
| v0.1.0 | 2025-12 | Initial release with dynamic retention |

---

## Contributing

We welcome contributions! Here's how you can help:

1. **Pick a feature** from this roadmap
2. **Open an issue** to discuss implementation
3. **Submit a PR** with your implementation

See [CONTRIBUTING.md](../CONTRIBUTING.md) for detailed guidelines.

### Development Priorities

When choosing what to work on, consider:

1. 🔴 **High Priority** - Core functionality, security, reliability
2. 🟡 **Medium Priority** - Significant user value
3. 🟢 **Low Priority** - Nice to have, quality of life

---

## Feedback

Have thoughts on this roadmap? We'd love to hear from you:

- **GitHub Issues** - Feature requests and bug reports
- **Discussions** - General questions and ideas

---

*Last updated: December 2025*

