# Bardiup vs. Alternatives

A detailed comparison of Kubernetes backup solutions to help you choose the right tool for your needs.

---

## Quick Comparison

| Feature | Bardiup | Velero | Stash | Kasten K10 |
|---------|:-------:|:------:|:-----:|:----------:|
| **License** | Apache 2.0 | Apache 2.0 | Apache 2.0 | Commercial |
| **Cost** | Free | Free | Free | $$$$$ |
| **Primary Focus** | PVC Backups | Full Cluster | App Backups | Enterprise DR |

### Retention & Scheduling

| Feature | Bardiup | Velero | Stash | Kasten K10 |
|---------|:-------:|:------:|:-----:|:----------:|
| Dynamic Retention (GFS) | ✅ Advanced | ❌ | ⚠️ Basic | ✅ |
| Custom Retention Periods | ✅ | ❌ | ❌ | ⚠️ |
| Selection Strategies | ✅ 3 options | ❌ | ❌ | ⚠️ |
| Cron Scheduling | ✅ | ✅ | ✅ | ✅ |
| Retention Preview | 🔜 Planned | ❌ | ❌ | ✅ |

### Backup Capabilities

| Feature | Bardiup | Velero | Stash | Kasten K10 |
|---------|:-------:|:------:|:-----:|:----------:|
| PVC Backups | ✅ | ✅ | ✅ | ✅ |
| Cluster Resources | ❌ | ✅ | ⚠️ | ✅ |
| Namespace Backups | ⚠️ PVCs only | ✅ | ✅ | ✅ |
| Incremental Backups | 🔜 Planned | ⚠️ Plugin | ✅ | ✅ |
| Application-Aware | 🔜 Planned | ✅ Hooks | ✅ | ✅ |
| VolumeSnapshots | 🔜 Planned | ✅ | ✅ | ✅ |

### Storage & Security

| Feature | Bardiup | Velero | Stash | Kasten K10 |
|---------|:-------:|:------:|:-----:|:----------:|
| S3 / S3-Compatible | ✅ | ✅ | ✅ | ✅ |
| Azure Blob | 🔜 Planned | ✅ | ✅ | ✅ |
| Google Cloud Storage | 🔜 Planned | ✅ | ✅ | ✅ |
| Encryption at Rest | 🔜 Planned | ✅ | ✅ | ✅ |
| Compression | 🔜 Planned | ⚠️ | ✅ | ✅ |

### Operations

| Feature | Bardiup | Velero | Stash | Kasten K10 |
|---------|:-------:|:------:|:-----:|:----------:|
| Web UI | ❌ | ❌ | ❌ | ✅ |
| CLI Tool | kubectl | velero CLI | kubectl | CLI + UI |
| Prometheus Metrics | 🔜 Planned | ✅ | ✅ | ✅ |
| Restore Automation | ⚠️ Manual | ✅ | ✅ | ✅ |
| Cross-Cluster Restore | ✅ Manual | ✅ | ✅ | ✅ |

### Resource Usage

| Aspect | Bardiup | Velero | Stash | Kasten K10 |
|--------|:-------:|:------:|:-----:|:----------:|
| Controller Pods | 1 | 1 | 1+ | Multiple |
| Memory Usage | ~50MB | ~100MB | ~100MB | ~500MB+ |
| CRD Count | 2 | 10+ | 10+ | 50+ |
| Complexity | Low | Medium | Medium | High |

---

## Detailed Comparisons

### Bardiup vs. Velero

**Velero** is the most popular open-source Kubernetes backup solution. Here's how Bardiup differs:

#### When to Choose Bardiup

- ✅ **You need sophisticated retention policies** - Bardiup's GFS-style retention is more flexible than Velero's TTL-based approach
- ✅ **You only need PVC backups** - Bardiup is simpler and more focused
- ✅ **Storage costs matter** - Dynamic retention keeps exactly what you need
- ✅ **You want simplicity** - 2 CRDs vs. 10+ for Velero

#### When to Choose Velero

- ✅ **You need full cluster backups** - Velero backs up everything, not just PVCs
- ✅ **You need disaster recovery** - Full cluster migration capabilities
- ✅ **You need VolumeSnapshots now** - Velero has CSI plugin support
- ✅ **You need a mature ecosystem** - Velero has more plugins and integrations

#### Retention Policy Comparison

**Velero (Simple TTL):**
```yaml
apiVersion: velero.io/v1
kind: Schedule
spec:
  schedule: "0 */6 * * *"
  template:
    ttl: 720h  # 30 days - ALL backups deleted after 30 days
```

**Bardiup (Dynamic Retention):**
```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
spec:
  schedule: "0 */6 * * *"
  retentionPolicy:
    daily:   { keep: 7 }   # Recent granularity
    weekly:  { keep: 4 }   # Medium-term
    monthly: { keep: 12 }  # Long-term
    yearly:  { keep: 5 }   # Compliance
    # Result: Keeps strategic backups spanning 5+ years
    # with only ~28 total backups instead of hundreds
```

---

### Bardiup vs. Stash

**Stash** (by AppsCode) is an application-aware backup solution.

#### When to Choose Bardiup

- ✅ **You want simpler retention policies** - Stash's retention is basic
- ✅ **You prefer declarative configuration** - Bardiup is more Kubernetes-native
- ✅ **You want a focused solution** - Stash has many features you may not need

#### When to Choose Stash

- ✅ **You need database-aware backups** - Stash has built-in database support
- ✅ **You need incremental backups now** - Stash supports restic incremental
- ✅ **You need multiple repositories** - More flexible backup targets

---

### Bardiup vs. Kasten K10

**Kasten K10** is an enterprise-grade data management platform.

#### When to Choose Bardiup

- ✅ **You want a free solution** - K10 is expensive
- ✅ **You need just PVC backups** - K10 is overkill for simple use cases
- ✅ **You want to avoid vendor lock-in** - Bardiup is fully open source
- ✅ **You have limited resources** - K10 is resource-intensive

#### When to Choose Kasten K10

- ✅ **You have enterprise requirements** - Support, SLAs, certifications
- ✅ **You need a UI** - K10 has an excellent web interface
- ✅ **You need multi-cluster management** - Enterprise-scale operations
- ✅ **You need compliance features** - Audit logs, RBAC, etc.

---

## Feature Deep Dives

### Retention Policies

The biggest differentiator for Bardiup is its retention policy system.

#### The Problem with Simple TTL

Most backup solutions use TTL (Time-To-Live):
- Keep all backups for X days
- Delete everything older than X days

This creates a dilemma:
- Short TTL = lose historical recovery points
- Long TTL = exploding storage costs

#### Bardiup's Solution: GFS-Style Retention

GrandFather-Father-Son (GFS) is a classic backup rotation scheme:

```
Week 1: [Daily Daily Daily Daily Daily Daily Daily]
Week 2: [Daily Daily Daily Daily Daily Weekly]
Week 3: [Daily Daily Daily Daily Daily Weekly]
Week 4: [Daily Daily Daily Daily Daily Weekly]
Month 2: [Weekly Weekly Weekly Monthly]
Month 3: [Weekly Weekly Weekly Monthly]
...
Year 2: [Monthly Monthly Monthly ... Yearly]
```

**Benefits:**
- Recent granularity for quick recovery
- Medium-term coverage for week-old issues
- Long-term retention for compliance
- Automatic cleanup = controlled costs

**Storage Comparison (1 year, daily backups):**

| Approach | Backups Stored | Storage Cost* |
|----------|:--------------:|:-------------:|
| Keep All | 365 | $365/year |
| 30-day TTL | 30 | $30/year |
| Bardiup GFS | ~28 | $28/year |

*Assuming $1/backup/month. Bardiup matches TTL cost while providing 5-year coverage!

---

### Selection Strategies

Bardiup offers three strategies for choosing which backups to keep:

#### `newest` Strategy

Keep the most recent backups in each period.

```
Period: Last 7 days
Backups: [Mon Tue Wed Thu Fri Sat Sun]
Keep 3: [         Thu Fri Sat    ]  (newest 3)
```

**Best for:** Recent recovery, where newest data matters most.

#### `oldest` Strategy

Keep the earliest backups in each period.

```
Period: Last 7 days
Backups: [Mon Tue Wed Thu Fri Sat Sun]
Keep 3: [Mon Tue Wed                ]  (oldest 3)
```

**Best for:** Compliance, audit trails, historical record.

#### `distributed` Strategy

Spread backups evenly across the time period.

```
Period: Last 7 days
Backups: [Mon Tue Wed Thu Fri Sat Sun]
Keep 3: [Mon     Wed         Sat    ]  (distributed)
```

**Best for:** Coverage across time, monthly/yearly retention.

---

## Migration Guides

### From Velero to Bardiup

1. **Keep Velero running** for existing backups
2. **Install Bardiup** alongside Velero
3. **Create BackupConfigs** for your PVCs
4. **Gradually transition** as Bardiup takes over
5. **Remove Velero** once satisfied

Note: Bardiup cannot restore Velero backups directly.

### From Stash to Bardiup

Similar approach - run both, transition gradually.

---

## Cost Analysis

### Storage Cost Example

**Scenario:** 100GB PVC, hourly backups, 1-year retention

| Solution | Retention Approach | Backups/Year | Storage Cost* |
|----------|-------------------|:------------:|:-------------:|
| Keep All | None | 8,760 | $8,760 |
| 30-day TTL | Simple | 720 | $720 |
| Bardiup | GFS | ~52 | $52 |

*$1/GB/month, simplified

**Bardiup saves ~99% vs keeping all, ~92% vs 30-day TTL**, while maintaining better recovery point coverage!

---

## Summary: Which Tool is Right for You?

### Choose Bardiup if:

- 🎯 You primarily need PVC backups
- 💰 Storage costs are a concern
- 📊 You want sophisticated retention without complexity
- 🚀 You want a lightweight, focused solution
- 🆓 You need a free, open-source option

### Choose Velero if:

- 🌐 You need full cluster backup/migration
- 🔌 You need the plugin ecosystem
- 📸 You need VolumeSnapshot support now
- 🏢 You need proven enterprise adoption

### Choose Kasten K10 if:

- 💼 You have enterprise budget and requirements
- 🖥️ You need a web UI
- 📋 You need compliance certifications
- 🔧 You want managed, supported software

---

*Have questions about this comparison? Open an issue!*

