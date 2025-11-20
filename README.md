# Bardiup - Dynamic PVC Backup Controller for Kubernetes

Bardiup is a Kubernetes operator that provides automated backup of Persistent Volume Claims (PVCs) with sophisticated dynamic retention policies and object storage compatibility.

## 🌟 Key Features

### Dynamic Retention Policies (The Killer Feature!)
Unlike traditional backup solutions with simple retention periods, Bardiup allows you to configure sophisticated retention policies that keep backups strategically distributed over time:

- **Multi-tier Retention**: Define different retention rules for daily, weekly, monthly, yearly, and custom periods
- **Smart Selection**: Choose how backups are selected for retention (newest, oldest, or distributed)
- **Custom Periods**: Define your own retention periods (e.g., quarterly, bi-weekly)
- **Automatic Cleanup**: Old backups are automatically deleted based on your retention policy

**Example**: Keep daily backups for 7 days, but also keep 1 backup from a month ago, 1 from three months ago, and 1 from a year ago - all configurable!

### Other Features
- 🗄️ **Object Storage Support**: S3 and S3-compatible storage (MinIO, etc.)
- 📅 **Cron-based Scheduling**: Flexible backup scheduling using cron expressions
- 🏷️ **Label-based Selection**: Select PVCs to backup using labels or names
- 🔄 **Multiple Backup Methods**: Support for copy-based and snapshot-based backups
- 🎯 **Namespace Scoped**: Backup PVCs in specific namespaces
- ⏸️ **Pause/Resume**: Ability to pause and resume backup schedules
- 📊 **Status Tracking**: Track backup status, last backup time, and next scheduled time

## 📦 Installation

### Prerequisites
- Kubernetes 1.19+
- Helm 3+
- Object storage (S3, MinIO, etc.)

### Using Helm

```bash
# Add the bardiup repository (when published)
# helm repo add bardiup https://bardiup.github.io/charts
# helm repo update

# For now, install from local chart
helm install bardiup ./charts/bardiup \
  --namespace bardiup-system \
  --create-namespace \
  --dependency-update
```

## 🚀 Quick Start

### 1. Create S3 Credentials Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: s3-credentials
  namespace: bardiup-system
type: Opaque
stringData:
  accessKeyId: "your-access-key"
  secretAccessKey: "your-secret-key"
```

### 2. Create a BackupConfig with Dynamic Retention

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
  
  # Dynamic retention configuration - THE MAGIC!
  retentionPolicy:
    daily:
      keep: 7              # Keep 7 daily backups
      selectionStrategy: newest
    weekly:
      keep: 4              # Keep 4 weekly backups
      selectionStrategy: distributed
    monthly:
      keep: 12             # Keep 12 monthly backups
      selectionStrategy: distributed
    yearly:
      keep: 5              # Keep 5 yearly backups
      selectionStrategy: distributed
    custom:
      - periodDays: 90     # Quarterly backups
        keep: 8
        selectionStrategy: distributed
```

## 📋 Configuration

### BackupConfig Specification

| Field | Description | Required |
|-------|-------------|----------|
| `pvcSelector` | Selects which PVCs to backup | Yes |
| `schedule` | Cron expression for backup schedule | Yes |
| `storageBackend` | Where to store backups (S3, etc.) | Yes |
| `retentionPolicy` | Dynamic retention configuration | Yes |
| `backupMethod` | Method to use (copy/snapshot) | No |
| `snapshotClassName` | `VolumeSnapshotClass` for snapshot backups | No |
| `paused` | Pause backup schedule | No |

`StorageLocation.Path` always stores a **relative key** (no prefix). The S3 backend automatically prepends the configured prefix, preventing double-prefix bugs.

### Retention Policy Options

Each retention tier (daily, weekly, monthly, yearly, custom) supports:

- `keep`: Number of backups to retain for this period
- `selectionStrategy`: How to select which backups to keep
  - `newest`: Keep the most recent backups
  - `oldest`: Keep the earliest backups
  - `distributed`: Distribute backups across the time period

### Selection Strategy Examples

- **newest**: If you have 100 daily backups and `keep: 7`, keeps the 7 most recent backups.
- **distributed**: If you have 365 daily backups (1 year) and monthly `keep: 12`, it will keep 1 backup per month, distributed evenly.
- **oldest**: Useful for compliance where you need to retain the earliest backups.

## 📚 Documentation

Full, task-oriented documentation (installation, configuration, retention design, testing, troubleshooting, and restore workflows) lives in [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md).

## 🧪 Local Testing with Kind

### 1. Create Kind Cluster

```bash
kind create cluster --name kind-bardiup
```

### 2. Deploy MinIO for Testing

```bash
kubectl apply -f examples/minio-deployment.yaml
```

### 3. Install Bardiup

```bash
# Build and load image to kind
docker build -t bardiup:latest .
kind load docker-image bardiup:latest --name kind-bardiup

# Install with Helm
helm install bardiup ./charts/bardiup \
  --namespace bardiup-system \
  --create-namespace \
  --set image.repository=bardiup \
  --set image.tag=latest \
  --set image.pullPolicy=Never
```

### 4. Create Test PVC and BackupConfig

```yaml
# test-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
  namespace: default
  labels:
    test: backup
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
```

```bash
kubectl apply -f test-pvc.yaml
kubectl apply -f examples/minio-deployment.yaml
```

### 5. Check Backup Status

```bash
# List BackupConfigs
kubectl get backupconfigs

# List Backups
kubectl get backups -A

# Check controller logs
kubectl logs -n bardiup-system deployment/bardiup -f
```

## 🧪 Testing

- **Unit & controller tests** (retention logic, PVC selection, backup creation):

  ```bash
  go test ./...
  ```

- **End-to-end tests on kind**:
  1. Deploy controller and CRDs (`helm install ...`).
  2. Apply `examples/test-pvc.yaml` and `examples/minio-deployment.yaml`.
  3. Wait for backups to appear via `kubectl get backups -A`.

Testing guidance, expected outputs, and troubleshooting steps are fully documented in [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md#testing).

## 🏗️ Architecture

Bardiup consists of:

1. **Controller**: Manages BackupConfig resources and schedules backups
2. **Retention Manager**: Implements sophisticated retention logic
3. **Storage Backend**: Interfaces with object storage (S3, etc.)
4. **Backup Executor**: Performs the actual backup operations

## 🔧 Development

### Prerequisites
- Go 1.21+
- Docker
- Kind (for local testing)

### Building

```bash
# Download dependencies
go mod download

# Run gofmt & tests
gofmt -w $(git ls-files '*.go')
go test ./...

# Build binary
go build -o bin/manager cmd/main.go

# Build Docker image
docker build -t bardiup:latest .
```

### Running Locally

```bash
# Install CRDs
kubectl apply -f charts/bardiup/crds/

# Run controller locally
go run cmd/main.go
```

## 📝 Examples

Check the `examples/` directory for:
- MinIO deployment for local testing
- Various BackupConfig examples with different retention policies
- S3 credentials secret template

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📜 License

Apache 2.0

## 🙏 Acknowledgments

Built with:
- [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [AWS SDK for Go](https://github.com/aws/aws-sdk-go-v2)
- [cron](https://github.com/robfig/cron)

---

**Bardiup** - Smart PVC backups with dynamic retention for Kubernetes 🚀
