# Bardiup User Guide

This guide walks you through installing, configuring, operating, and testing **Bardiup**, the dynamic PVC backup controller for Kubernetes.

## Table of Contents

1. [Overview](#overview)
2. [Requirements](#requirements)
3. [Architecture](#architecture)
4. [Installation](#installation)
   - [Using Helm](#using-helm)
   - [From Source](#from-source)
   - [Kind (local) deployment](#kind-local-deployment)
5. [Configuring Storage Backends](#configuring-storage-backends)
6. [Creating Backup Configurations](#creating-backup-configurations)
7. [Dynamic Retention Policies](#dynamic-retention-policies)
8. [Running and Monitoring Backups](#running-and-monitoring-backups)
9. [Restoring Data](#restoring-data)
10. [Testing](#testing)
11. [Troubleshooting](#troubleshooting)
12. [FAQ](#faq)

---

## Overview

Bardiup automates Kubernetes PVC backups with **dynamic retention policies**. It captures regular backups (daily/weekly/monthly/yearly) and optionally **custom periods** (e.g., quarterly). Bardiup ensures you keep recent backups and strategically retains older snapshots for compliance and disaster recovery.

Key capabilities:

- Label- or name-based PVC selection
- Cron-style scheduling
- Object storage targets (S3 and compatible, e.g. MinIO)
- Copy-based backups via Kubernetes Jobs (snapshot support planned)
- Automatic retention enforcement

---

## Requirements

- **Kubernetes** v1.25+ (tested on 1.29 via kind)
- **Helm** v3+
- **Go** 1.21+ (for building/testing)
- **Docker** (for building container images)
- **kind** (optional, for local clusters)
- Object storage credentials (AWS S3, MinIO, etc.)

---

## Architecture

| Component | Responsibility |
|-----------|----------------|
| `BackupConfig` CR | Declares which PVCs to protect, schedule, storage backend, and retention policy |
| `Backup` CR | Represents a single backup execution with metadata and retention labels |
| Controller | Reconciles `BackupConfig`, schedules backups, manages status, enforces retention |
| Backup Executor | Spawns Kubernetes Jobs to copy PVC contents and upload to object storage |
| Retention Manager | Determines which backups to keep/delete based on policy |
| Storage Backend | Pluggable interface (currently S3-compatible) handling upload/download/delete |

Data flow:

1. Controller reconciles `BackupConfig`.
2. When schedule triggers, it scans matching PVCs and creates `Backup` objects.
3. Backup executor Job reads the PVC, archives data, uploads to object storage.
4. Retention manager periodically prunes expired backups.

---

## Installation

### Using Helm

```bash
helm install bardiup ./charts/bardiup \
  --namespace bardiup-system \
  --create-namespace \
  --set image.repository=bardiup \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent
```

Customize image references when pushing to a registry.

### From Source

```bash
# Build the manager binary
go build -o bin/bardiup-manager ./cmd

# Build container image
docker build -t bardiup:latest .
```

### Kind (local) deployment

```bash
kind create cluster --name kind-bardiup
docker build -t bardiup:latest .
kind load docker-image bardiup:latest --name kind-bardiup
kubectl apply -f charts/bardiup/crds/
helm upgrade --install bardiup ./charts/bardiup \
  --namespace bardiup-system --create-namespace \
  --set image.repository=bardiup \
  --set image.tag=latest \
  --set image.pullPolicy=Never
```

---

## Configuring Storage Backends

Currently supported: **S3** and S3-compatible services (MinIO, Ceph RGW, etc).

1. Create credentials secret (`examples/s3-credentials-secret.yaml` or `examples/minio-deployment.yaml`).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: bardiup-system
type: Opaque
stringData:
  accessKeyId: minioadmin
  secretAccessKey: minioadmin
```

> **Important:** Backup Jobs run in the same namespace as the PVC they protect.  
> Create the credentials secret **in every namespace that contains protected PVCs**, or use a `Secret` sync mechanism (e.g., Kyverno, Argo CD) to replicate it.

2. Reference the secret in `BackupConfig.spec.storageBackend.s3.credentialsSecret`.

Fields:
- `bucket`: target bucket name.
- `region`: AWS region (use dummy for MinIO).
- `endpoint`: optional custom endpoint (e.g., `http://minio.minio.svc.cluster.local:9000`).
- `forcePathStyle`: set to `true` for MinIO.
- `prefix`: optional path prefix under which backups are stored.

---

## Creating Backup Configurations

Minimal example (`examples/backupconfig-dynamic-retention.yaml`):

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata:
  name: postgres-backup
spec:
  pvcSelector:
    namespace: default
    matchLabels:
      app: postgres
  schedule: "0 */6 * * *"          # every 6 hours
  backupMethod: copy               # snapshot support planned
  storageBackend:
    type: s3
    prefix: postgres
    s3:
      bucket: backups
      region: us-west-2
      endpoint: http://minio.minio.svc.cluster.local:9000
      forcePathStyle: true
      credentialsSecret: minio-credentials
  retentionPolicy:
    daily:   { keep: 7,  selectionStrategy: newest }
    weekly:  { keep: 4,  selectionStrategy: distributed }
    monthly: { keep: 12, selectionStrategy: distributed }
    yearly:  { keep: 5,  selectionStrategy: distributed }
    custom:
      - periodDays: 90
        keep: 8
        selectionStrategy: distributed
```

Apply with `kubectl apply -f backupconfig.yaml`.

---

## Dynamic Retention Policies

Retention tiers:

| Tier | Field | Interval | Example |
|------|-------|----------|---------|
| Daily | `spec.retentionPolicy.daily` | 1 day | Keep last 7 backups |
| Weekly | `weekly` | 7 days | Keep 4 backups, distributed |
| Monthly | `monthly` | 30 days | Keep 12 backups |
| Yearly | `yearly` | 365 days | Keep 5 backups |
| Custom | `custom[]` | `periodDays` | e.g., 90-day quarterly backups |

`selectionStrategy` options:
- `newest` (default): keep most recent backups.
- `oldest`: keep oldest backups (useful for compliance).
- `distributed`: spread backups evenly across the time window.

Backups inherit a `bardiup.io/retention` label to track their tier.

---

## Running and Monitoring Backups

**Check controller health**
```bash
kubectl get pods -n bardiup-system
kubectl logs -n bardiup-system deployment/bardiup
```

**List configs and backups**
```bash
kubectl get backupconfigs
kubectl describe backupconfig <name>
kubectl get backups -A
```

**Force a reconciliation**
```bash
kubectl annotate backupconfig <name> force-reconcile=$(date +%s) --overwrite
```

**Inspect retention decisions**
Controller logs include entries when a backup is marked for deletion.

---

## Restoring Data

1. Identify the `Backup` resource you want to restore.
2. Create (or select) a target PVC with equal or larger capacity.
3. Use the `RestoreBackup` helper (future automation) or craft a job similar to `Executor.createRestoreJob`.
   - Download data from object storage (using credentials).
   - Extract archive into the PVC.

> **Note:** The provided executor restores via a Kubernetes Job. Adapt the job command to your workload requirements.

---

## Testing

Bardiup ships with unit and controller integration tests plus kind-based validation.

### Unit & Controller Tests

```bash
go test ./...
```

This runs:
- Retention manager suites (selection, expiration, custom periods)
- Controller behavior tests using the controller-runtime fake client (PVC selection, backup creation, scheduling logic)

### Integration / E2E (kind)

1. Deploy to `kind-bardiup` as described earlier.
2. Apply `examples/test-pvc.yaml` to generate sample data.
3. Apply `examples/minio-deployment.yaml` to provision MinIO and a `BackupConfig`.
4. Observe `Backup` objects appearing every minute (adjust schedule as needed).
5. Inspect MinIO bucket (`kubectl -n minio port-forward deployment/minio 9000:9000` + `mc` or AWS CLI).

> The `docs/USER_GUIDE.md` scenario is fully automated in `examples/minio-deployment.yaml`.

---

## Troubleshooting

| Symptom | Possible Cause | Resolution |
|---------|----------------|------------|
| `BackupConfig` stuck in `Error` phase | Storage backend misconfigured | Check secret, endpoint, bucket permissions |
| No backups created | PVC selector mismatch or schedule not due | Verify labels/names, set `force-reconcile` annotation |
| Backups never deleted | Retention policy missing tiers | Ensure `keep` > 0 and at least one tier configured |
| Jobs fail | Backup image lacks tooling / permissions | Use a purpose-built image that uploads to S3 (extend job command) |
| Double prefix in object keys | Older configs embedded prefix in the path | Starting with this release only relative paths are stored; reapply configs |

Enable debug logs by increasing verbosity (set `--zap-log-level=-1` via Helm chart).

---

## FAQ

**Q: Does Bardiup support VolumeSnapshots?**  
A: Snapshot mode is planned. Currently, backups use copy jobs that tar the PVC contents.

**Q: Can I store backups outside S3?**  
A: Today the storage backend interface targets S3-compatible APIs. Implementations for Azure/GCS can be added under `pkg/storage/`.

**Q: How can I run backups more frequently?**  
A: Provide any cron schedule (e.g., `"*/5 * * * *"`). Retention policies will ensure older backups roll off automatically.

**Q: How do I restore a specific point-in-time backup?**  
A: List `Backup` resources, find the one with the desired timestamp/retention tier, and run a restore job referencing its `spec.storageLocation`.

---

Need more examples? See the `examples/` directory for ready-to-apply manifests covering secrets, BackupConfigs, test PVCs, and a MinIO stack.

