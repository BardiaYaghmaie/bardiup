<h1 align="center">Bardiup</h1>

<p align="center"><b>Kubernetes volume backups that keep the right copies, and nothing else.</b></p>

<p align="center">
  <a href="https://github.com/BardiaYaghmaie/bardiup/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/BardiaYaghmaie/bardiup/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/BardiaYaghmaie/bardiup/releases"><img alt="Release" src="https://img.shields.io/github/v/release/BardiaYaghmaie/bardiup"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache%202.0-blue"></a>
  <a href="https://goreportcard.com/report/github.com/BardiaYaghmaie/bardiup"><img alt="Go Report" src="https://goreportcard.com/badge/github.com/BardiaYaghmaie/bardiup"></a>
</p>

Bardiup is a small Kubernetes operator that backs up PersistentVolumeClaims to any S3-compatible bucket and prunes them with **Grandfather-Father-Son retention**: keep the last 24 hours, 7 days, 4 weeks, 12 months and 5 years, with one line of YAML per tier. Backups are **plain `tar.gz` files** you can restore with `tar` alone. Every namespace manages **its own** backups with the RBAC it already has.

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata: {name: nightly, namespace: shop}
spec:
  schedule: "0 2 * * *"
  pvcSelector: {matchLabels: {app: postgres}}
  storage:
    s3: {bucket: backups, region: eu-central-1, credentialsSecretRef: {name: s3}}
  retention: {daily: 7, weekly: 4, monthly: 12, yearly: 5}
```

```console
$ kubectl -n shop get backups
NAME                                PVC         PHASE       SIZE      RETAINED BY                                     COMPLETED
nightly-data-pg-0-20261119-020000   data-pg-0   Completed   1.2 GiB   ["daily","weekly","monthly","yearly"]           7h
nightly-data-pg-0-20261118-020000   data-pg-0   Completed   1.2 GiB   ["daily"]                                       31h
nightly-data-pg-0-20261116-020000   data-pg-0   Completed   1.1 GiB   ["daily","weekly"]                              3d7h
nightly-data-pg-0-20261031-020000   data-pg-0   Completed   1.1 GiB   ["monthly"]                                     19d
```

After a year of nightly backups, that policy keeps **20 archives instead of 365**, and every one of them tells you why it's still there.

---

## Why Bardiup

**Velero backs up your cluster. Bardiup backs up your data.** If you need to protect the volumes behind your databases, queues and file stores, Bardiup is built for exactly that and nothing more.

| | Bardiup | Velero |
|---|---|---|
| **Retention** | GFS per PVC: `hourly/daily/weekly/monthly/yearly/custom`, calendar- and time-zone-aware | One TTL per schedule (default 30 days). GFS means juggling several schedules. |
| **Who can back up** | Any namespace owner: namespaced CRDs aggregated into `admin`/`edit`/`view` | Cluster admins: Backup and Schedule objects live in Velero's namespace |
| **Backup format** | Plain `tar.gz` + JSON manifest + SHA-256. Restore with `tar`. | Kopia repository: deduplicated and encrypted, readable only through Velero/Kopia |
| **What runs on your nodes** | Nothing between backups. One short-lived Job per volume, mounting just that volume. | File-system backup needs a node-agent DaemonSet on every node with `hostPath` access to `/var/lib/kubelet/pods` |
| **Mover privileges** | UID 0 with only `DAC_OVERRIDE` and a read-only mount of one volume; fits the *baseline* Pod Security Standard | Node-agent runs as root with `hostPath` access to every pod volume on the node (privileged mode on SELinux hosts) |
| **Explainability** | `status.retainedBy` on every backup, conditions, events, Prometheus alerts included | Backups expire by TTL |
| **Footprint** | One Deployment, one ~40 MB distroless image for both controller and mover | Server + node-agent DaemonSet + provider plugin images |

**Use Velero instead** when you need to back up and migrate Kubernetes *resources* (Deployments, ConfigMaps, whole namespaces), want deduplicated incremental backups of very large volumes, or need cloud-provider snapshot plugins and pre/post hooks. The two work well together: Velero (or GitOps) for manifests, Bardiup for volume data with real retention. See the [detailed comparison](docs/comparison.md).

## Features

- **GFS retention per PVC.** `keepLast`, `hourly`, `daily`, `weekly`, `monthly`, `yearly` and custom periods (`every: 90d`). Same semantics as `restic forget`, evaluated in your time zone. → [Retention](docs/retention.md)
- **Self-service and multi-tenant.** Namespaced `BackupConfig`, `Backup` and `Restore`, with per-namespace credentials and buckets. Users can't reach other namespaces.
- **Open, verifiable archives.** Standard POSIX tar + gzip with ownership, permissions, symlinks and hard links. A SHA-256 is stored in status and in a manifest next to the archive, and verified on every restore.
- **Two ways to read data.** `copy` mounts the live volume read-only, pinned to the node that already has a `ReadWriteOnce` volume attached. `snapshot` takes a CSI VolumeSnapshot for crash-consistent backups without touching the workload.
- **Restores that just work.** Into a new PVC (sized and classed like the original) or in place. Disaster recovery straight from a bucket key into a fresh cluster. → [Restore](docs/restore.md)
- **Any S3-compatible store.** AWS S3, MinIO, Ceph RGW, RustFS, Cloudflare R2, Backblaze B2, Wasabi, Hetzner, Arvan and more. Static keys or IRSA / Pod Identity, SSE-S3 / SSE-KMS.
- **Built for large volumes.** Parallel compression, concurrent multipart uploads with part sizes that scale to multi-terabyte volumes, and downloads that resume after network drops.
- **Operable.** Prometheus metrics, a ready-made `PrometheusRule` (stale backups, failures, misconfiguration), Kubernetes events and status conditions. `kubectl annotate ... bardiup.io/backup-now=...` runs a backup immediately.
- **Safe by default.** Deleting a `BackupConfig` never deletes backups. Extraction can't escape the target volume (Go `os.Root`, fuzz-tested). RBAC is least-privilege and generated from code: `get` on Secrets, never `list`.

## Quick start

**1. Install** (Kubernetes 1.27+):

```bash
helm install bardiup oci://ghcr.io/bardiayaghmaie/charts/bardiup -n bardiup-system --create-namespace
# or: kubectl apply --server-side -f https://github.com/BardiaYaghmaie/bardiup/releases/latest/download/bardiup.yaml
```

**2. Store credentials** in the namespace you want to protect:

```bash
kubectl -n shop create secret generic s3 \
  --from-literal=AWS_ACCESS_KEY_ID=... --from-literal=AWS_SECRET_ACCESS_KEY=...
```

**3. Create a `BackupConfig`**, like the one above or from [examples/](examples/), then check it's ready:

```console
$ kubectl -n shop get bcfg
NAME      SCHEDULE    SUSPEND   PVCS   BACKUPS   READY   LAST SUCCESS   AGE
nightly   0 2 * * *   false     1      0         True                   5s
```

**4. Back up now** instead of waiting for 02:00:

```bash
kubectl -n shop annotate bcfg nightly bardiup.io/backup-now="$(date +%s)" --overwrite
kubectl -n shop get backups -w
```

**5. Restore** into a new PVC:

```bash
kubectl -n shop apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: rollback}
spec:
  backupName: $(kubectl -n shop get backups -o jsonpath='{.items[-1:].metadata.name}')
  target: {pvcName: data-pg-0-restored}
EOF
```

## How it works

```
               ┌──────────────────────── bardiup controller (1 Deployment) ───────────────────────┐
               │  BackupConfig ─schedule/annotation─▶ Backup ─▶ Job ─▶ status (size, sha256, files) │
               │       ▲                                │                                          │
               │       └──── GFS retention per PVC ◀────┘  deletes Backup ─▶ finalizer deletes data│
               └───────────────────────────────────────────────────────────────────────────────────┘
                                          │ creates, in the PVC's namespace
                                          ▼
   PVC ──(read-only mount, same node     ┌───────────────────────────┐   tar ─▶ pgzip ─▶ sha256 ─▶ multipart
   or CSI snapshot clone)───────────────▶│ mover Job: bardiup agent  │──────────────────────────────────────▶ S3
                                         └───────────────────────────┘   <prefix>/<ns>/<pvc>/<backup>.tar.gz(.json)
```

The controller and the data mover are the same binary. The controller never touches volume data; a short-lived Job does, in the namespace that owns the data, under that namespace's admission policies. Results come back through the container's termination message, so no extra API access is needed. Every step is a reconcile of persisted state, so killing the controller mid-backup loses nothing, and scheduled runs use deterministic names, so retries never create duplicates.

## Tested for real

Every change runs through:

- **Unit and integration tests** for every controller path, with race detection, plus round-trip tests against an in-memory S3.
- **Fuzzing** of the archive extractor (path-traversal safety) and of the retention engine (invariants: newest always kept, stable, bounded).
- **End-to-end on kind** with a real S3 server. Back up a live RWO volume, prune, verify the archive with GNU `tar` and `sha256sum`, restore into a new PVC and into another namespace from the bucket key alone, then compare content, ownership and modes. Both `copy` and CSI `snapshot` methods.
- **Stress and chaos on a multi-node kind cluster:**
  - dozens of PVCs across tenants at once
  - a multi-GiB volume with 100k files restored byte-for-byte
  - the controller killed twice mid-backup
  - the object store taken down and brought back
  - minute-by-minute schedules with retention, checking that the bucket never holds orphans
  - concurrent restores

## Documentation

| | |
|---|---|
| [Configuration reference](docs/configuration.md) | Every field of `BackupConfig`, `Backup`, `Restore` and the Helm chart |
| [Retention](docs/retention.md) | How GFS works, how to pick a policy, what gets pruned |
| [Restore and disaster recovery](docs/restore.md) | New PVC, in place, cross-cluster, without Bardiup |
| [Security model](docs/security.md) | Multi-tenancy, RBAC, mover privileges, encryption |
| [Operations](docs/operations.md) | Metrics, alerts, troubleshooting, sizing, upgrades |
| [Comparison](docs/comparison.md) | Bardiup vs Velero and others, honestly |
| [Examples](examples/) | Ready-to-apply manifests |

## Roadmap

- Pre/post backup hooks (`exec` into the workload, for example `pg_backup_start`)
- Incremental backups for very large, slowly changing volumes
- Client-side encryption (age)
- Azure Blob and Google Cloud Storage
- A `kubectl bardiup` plugin (`backup now`, `restore`, `ls` the bucket)
- Retention dry-run preview

Ideas and feedback are very welcome. [Open an issue](https://github.com/BardiaYaghmaie/bardiup/issues) and tell us how you back up your volumes today.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). `make test` needs nothing but Go, and `make e2e` brings its own kind cluster.

## License

[Apache 2.0](LICENSE)
