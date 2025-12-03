# Bardiup - Smart Kubernetes PVC Backups

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.25+-326CE5?style=flat&logo=kubernetes)](https://kubernetes.io/)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

**Stop paying for backups you don't need. Keep the ones you do.**

Bardiup automatically backs up Kubernetes PVCs with intelligent retention policies that keep backups strategically distributed over time—saving storage costs while maintaining comprehensive recovery points.

---

## ✨ The Problem

Traditional backup solutions force you to choose between:

| Approach | Problem |
|----------|---------|
| **Keep everything** | 💸 Exploding storage costs |
| **Simple TTL** (e.g., "keep 30 days") | 📅 Lose important historical recovery points |
| **Manual cleanup** | ⏰ Time-consuming, error-prone |

## ✨ The Solution

Bardiup's **dynamic retention** lets you automatically keep:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           RETENTION TIMELINE                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  NOW ◄─────────── 7 days ───────────► ◄── 4 weeks ──► ◄─── 12 months ───►  │
│   │                                                                         │
│   ▼    Daily backups (7)         Weekly (4)        Monthly (12)    Yearly  │
│  [■■■■■■■]                    [■   ■   ■   ■]    [■ ■ ■ ■ ■ ■ ...]   [■■■] │
│                                                                             │
│  Recent recovery points    Medium-term safety    Long-term compliance      │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

- **7 daily backups** for recent recovery
- **4 weekly backups** for the past month
- **12 monthly backups** for the past year
- **5 yearly backups** for compliance
- **Custom periods** (quarterly, bi-weekly, etc.)
- **All automatically managed** with zero manual cleanup

---

## 🆚 Why Bardiup?

| Feature | Bardiup | Velero | Stash | Kasten K10 |
|---------|:-------:|:------:|:-----:|:----------:|
| **Dynamic Retention** (GFS-style) | ✅ Advanced | ❌ Basic TTL | ⚠️ Simple | ✅ Yes |
| **Cost** | Free | Free | Free | $$$$ |
| **PVC-Focused** | ✅ | ⚠️ Cluster-wide | ✅ | ✅ |
| **Custom Retention Periods** | ✅ | ❌ | ❌ | ⚠️ |
| **Selection Strategies** | ✅ newest/oldest/distributed | ❌ | ❌ | ⚠️ |
| **S3 Compatible** | ✅ | ✅ | ✅ | ✅ |
| **Lightweight** | ✅ Single controller | ⚠️ Multiple components | ⚠️ | ❌ Heavy |

**Bardiup is ideal when you need:**
- Sophisticated backup retention without enterprise costs
- PVC-focused backups with granular control
- GrandFather-Father-Son (GFS) style retention
- Storage cost optimization through smart cleanup

---

## 🚀 Quick Start (5 minutes)

### Prerequisites
- Kubernetes 1.25+
- Helm 3+
- S3-compatible storage (AWS S3, MinIO, etc.)

### 1. Install Bardiup

```bash
# Create namespace and install
helm install bardiup ./charts/bardiup \
  --namespace bardiup-system \
  --create-namespace
```

### 2. Create Storage Credentials

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: s3-credentials
  namespace: default  # Same namespace as your PVCs
type: Opaque
stringData:
  accessKeyId: "your-access-key"
  secretAccessKey: "your-secret-key"
```

### 3. Create Your First BackupConfig

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata:
  name: my-app-backup
spec:
  pvcSelector:
    namespace: default
    matchLabels:
      app: my-app
  
  schedule: "0 */6 * * *"  # Every 6 hours
  
  storageBackend:
    type: s3
    prefix: my-app-backups
    s3:
      bucket: backup-bucket
      region: us-west-2
      credentialsSecret: s3-credentials
  
  retentionPolicy:
    daily:   { keep: 7,  selectionStrategy: newest }
    weekly:  { keep: 4,  selectionStrategy: distributed }
    monthly: { keep: 12, selectionStrategy: distributed }
    yearly:  { keep: 5,  selectionStrategy: distributed }
```

### 4. Verify It's Working

```bash
# Check BackupConfig status
kubectl get backupconfigs

# Watch backups being created
kubectl get backups -w

# Check controller logs
kubectl logs -n bardiup-system deployment/bardiup -f
```

---

## 📋 Key Features

### 🎯 Dynamic Retention Policies

The killer feature! Configure sophisticated retention that keeps backups strategically distributed:

```yaml
retentionPolicy:
  daily:
    keep: 7                    # Keep 7 daily backups
    selectionStrategy: newest  # Most recent ones
  weekly:
    keep: 4
    selectionStrategy: distributed  # Spread across weeks
  monthly:
    keep: 12
    selectionStrategy: distributed
  yearly:
    keep: 5
    selectionStrategy: oldest  # Keep earliest for compliance
  custom:
    - periodDays: 90           # Quarterly backups
      keep: 8
      selectionStrategy: distributed
```

**Selection Strategies:**
- `newest` - Keep the most recent backups in the period
- `oldest` - Keep the earliest backups (compliance use cases)
- `distributed` - Spread backups evenly across the time window

### 🗄️ S3-Compatible Storage

Works with any S3-compatible storage:
- AWS S3
- MinIO
- Ceph RGW
- DigitalOcean Spaces
- Backblaze B2
- Wasabi

### 📅 Flexible Scheduling

Use any cron expression:

```yaml
schedule: "0 */6 * * *"   # Every 6 hours
schedule: "0 2 * * *"     # Daily at 2 AM
schedule: "*/30 * * * *"  # Every 30 minutes (testing)
```

### 🏷️ Smart PVC Selection

Select PVCs by labels or names:

```yaml
pvcSelector:
  namespace: production
  matchLabels:
    tier: database
    backup: enabled
```

Or by specific names:

```yaml
pvcSelector:
  namespace: default
  matchNames:
    - postgres-data
    - redis-data
```

### ⏸️ Pause/Resume

Temporarily disable backups without deleting configuration:

```yaml
spec:
  paused: true
```

---

## 📖 Example Use Cases

### 🗃️ Database Backup Strategy

For production databases requiring comprehensive coverage:

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata:
  name: production-db-backup
spec:
  pvcSelector:
    namespace: production
    matchLabels:
      app: postgresql
  
  schedule: "0 * * * *"  # Hourly
  
  storageBackend:
    type: s3
    prefix: prod-db
    s3:
      bucket: company-backups
      region: us-east-1
      credentialsSecret: aws-credentials
  
  retentionPolicy:
    daily:   { keep: 24, selectionStrategy: newest }     # 24 hourly for today
    weekly:  { keep: 7,  selectionStrategy: distributed } # 7 daily for past week
    monthly: { keep: 4,  selectionStrategy: distributed } # 4 weekly for past month
    yearly:  { keep: 12, selectionStrategy: distributed } # 12 monthly for past year
```

### 💻 Development Environment

Lightweight retention for dev/staging:

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata:
  name: dev-backup
spec:
  pvcSelector:
    namespace: development
    matchLabels:
      env: dev
  
  schedule: "0 9 * * 1-5"  # Weekdays at 9 AM
  
  storageBackend:
    type: s3
    prefix: dev-backups
    s3:
      bucket: dev-bucket
      region: us-west-2
      endpoint: http://minio.minio.svc:9000
      forcePathStyle: true
      credentialsSecret: minio-credentials
  
  retentionPolicy:
    daily: { keep: 3, selectionStrategy: newest }  # Just last 3 backups
```

### 📜 Compliance (7-Year Retention)

For regulatory requirements:

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata:
  name: compliance-backup
spec:
  pvcSelector:
    namespace: finance
    matchLabels:
      compliance: required
  
  schedule: "0 0 1 * *"  # Monthly on the 1st
  
  storageBackend:
    type: s3
    prefix: compliance
    s3:
      bucket: compliance-archive
      region: us-east-1
      credentialsSecret: s3-credentials
  
  retentionPolicy:
    monthly: { keep: 84, selectionStrategy: oldest }  # 7 years of monthly backups
```

---

## 🔄 Restoring Data

> **📚 Full restore documentation:** [docs/RESTORE_GUIDE.md](docs/RESTORE_GUIDE.md)

### Quick Restore Steps

1. **Find the backup to restore:**
   ```bash
   kubectl get backups -n <namespace> -o wide
   ```

2. **Get backup details:**
   ```bash
   kubectl describe backup <backup-name>
   # Note the spec.storageLocation.path
   ```

3. **Create a restore job:**
   ```bash
   kubectl apply -f - <<EOF
   apiVersion: batch/v1
   kind: Job
   metadata:
     name: restore-job
     namespace: <namespace>
   spec:
     template:
       spec:
         containers:
         - name: restore
           image: amazon/aws-cli
           command: ["/bin/sh", "-c"]
           args:
           - |
             aws s3 cp s3://<bucket>/<prefix>/<path> /restore/backup.tar.gz
             tar -xzf /restore/backup.tar.gz -C /restore/data
           volumeMounts:
           - name: target-pvc
             mountPath: /restore/data
           - name: temp
             mountPath: /restore
         volumes:
         - name: target-pvc
           persistentVolumeClaim:
             claimName: <target-pvc>
         - name: temp
           emptyDir: {}
         restartPolicy: Never
   EOF
   ```

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              KUBERNETES CLUSTER                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────┐         ┌─────────────────┐                           │
│  │  BackupConfig   │────────▶│   Controller    │                           │
│  │      CR         │         │                 │                           │
│  └─────────────────┘         └────────┬────────┘                           │
│                                       │                                     │
│                    ┌──────────────────┼──────────────────┐                 │
│                    │                  │                  │                 │
│                    ▼                  ▼                  ▼                 │
│           ┌────────────────┐ ┌───────────────┐ ┌─────────────────┐        │
│           │    Backup      │ │   Retention   │ │     Backup      │        │
│           │   Executor     │ │    Manager    │ │      Jobs       │        │
│           └───────┬────────┘ └───────────────┘ └────────┬────────┘        │
│                   │                                      │                 │
│                   │         ┌─────────────────┐         │                 │
│                   └────────▶│  Storage Backend│◀────────┘                 │
│                             │     (S3 API)    │                            │
│                             └────────┬────────┘                            │
│                                      │                                     │
└──────────────────────────────────────┼─────────────────────────────────────┘
                                       │
                                       ▼
                              ┌─────────────────┐
                              │   S3 / MinIO    │
                              │  Object Storage │
                              └─────────────────┘
```

**Components:**

| Component | Responsibility |
|-----------|----------------|
| **Controller** | Reconciles BackupConfig, schedules backups, manages lifecycle |
| **Retention Manager** | Implements GFS-style retention logic, determines what to keep/delete |
| **Backup Executor** | Creates Kubernetes Jobs to backup PVC data |
| **Storage Backend** | Pluggable interface for object storage operations |

---

## 📚 Documentation

| Document | Description |
|----------|-------------|
| [User Guide](docs/USER_GUIDE.md) | Complete installation, configuration, and operations guide |
| [Restore Guide](docs/RESTORE_GUIDE.md) | Detailed restore procedures and scenarios |
| [Roadmap](docs/ROADMAP.md) | Planned features and development timeline |
| [Examples](examples/) | Ready-to-use YAML manifests |

---

## 🧪 Local Development & Testing

### Quick Local Setup with Kind

```bash
# Create cluster
kind create cluster --name bardiup-dev

# Build and load image
docker build -t bardiup:latest .
kind load docker-image bardiup:latest --name bardiup-dev

# Deploy MinIO for testing
kubectl apply -f examples/minio-deployment.yaml

# Install Bardiup
kubectl apply -f charts/bardiup/crds/
helm install bardiup ./charts/bardiup \
  --namespace bardiup-system --create-namespace \
  --set image.repository=bardiup \
  --set image.tag=latest \
  --set image.pullPolicy=Never

# Create test PVC and BackupConfig
kubectl apply -f examples/test-pvc.yaml
```

### Running Tests

```bash
# Unit tests
go test ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

---

## 🛤️ Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md) for the full roadmap. Highlights:

### Coming Soon
- 🔐 Encryption at rest (client-side encryption before upload)
- 📊 Prometheus metrics & alerting
- 🪝 Pre/post backup hooks (application-aware backups)
- 🔄 Cross-cluster restore support

### Planned
- 📈 Incremental backups
- ☁️ Azure Blob & GCS storage backends
- 🗜️ Compression options
- ✅ Automated backup verification

---

## 🤝 Contributing

Contributions are welcome! Please see our [Contributing Guide](CONTRIBUTING.md) for details.

```bash
# Development workflow
git clone https://github.com/your-org/bardiup.git
cd bardiup
go mod download
go test ./...
```

---

## 📜 License

Apache 2.0 - see [LICENSE](LICENSE) for details.

---

## 🔑 Keywords

`kubernetes` `pvc backup` `s3 backup` `retention policy` `grandfather-father-son` `gfs backup` `velero alternative` `kubernetes backup operator` `automated backup cleanup` `cost-effective kubernetes backup`

---

<p align="center">
  <strong>Bardiup</strong> - Smart PVC backups with dynamic retention for Kubernetes 🚀
  <br>
  <a href="docs/USER_GUIDE.md">Documentation</a> •
  <a href="examples/">Examples</a> •
  <a href="docs/ROADMAP.md">Roadmap</a>
</p>
