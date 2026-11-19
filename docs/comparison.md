# Bardiup compared

Bardiup does one thing: protect the **data in PersistentVolumeClaims**, with retention you'd design yourself, owned by the team that owns the data. This page is meant to help you choose, so it's also explicit about where other tools are the better fit.

Facts about other tools refer to Velero 1.18 (Helm chart 12.x). Corrections are welcome.

## Bardiup vs Velero

Velero is the standard tool for backing up and migrating **whole clusters**: resources and volumes, with plugins for every major cloud. Bardiup is narrower on purpose.

| | Bardiup | Velero |
|---|---|---|
| Scope | PVC data | Kubernetes resources + PVC data |
| Retention | GFS per PVC (`keepLast`, `hourly`…`yearly`, custom periods), time-zone aware, `status.retainedBy` | Per-backup TTL set by the schedule (default 720h). Tiered retention takes several schedules with different TTLs, each taking its own full backup. |
| Self-service | Namespaced CRDs, aggregated into `admin`/`edit`/`view`; per-namespace credentials and buckets | Backup/Schedule/Restore objects live in the Velero namespace; creating them effectively grants access to every namespace. Tenant self-service needs an extra layer (for example OADP's non-admin controller on OpenShift). |
| Data format | `tar.gz` + JSON manifest + SHA-256; restore with `tar` | Kopia repository (deduplicated, encrypted with a repository password); restore through Velero or Kopia |
| Incremental / dedup | No: each backup is a full compressed archive; GFS keeps the count low | Yes (Kopia) |
| Node components | None. One Job per volume, only while it runs. | File-system backup and data mover need the node-agent DaemonSet with `hostPath` mounts of `/var/lib/kubelet/pods` and `/var/lib/kubelet/plugins` |
| Crash-consistent backups | `method: snapshot` (CSI snapshot → clone → archive) | CSI snapshots, optionally with data movement to object storage |
| Application hooks | Not yet (roadmap) | Pre/post exec hooks |
| Storage backends | S3 and S3-compatible | AWS, Azure, GCP and others through plugins |
| Restore | `Restore` CR into a new or existing PVC, or from a raw bucket key | `velero restore` with namespace mapping, resource filtering and more |
| Install | One Deployment, one image (controller and mover are the same binary) | Server Deployment + node-agent DaemonSet + provider plugin init containers |
| Observability | Metrics, ready-made PrometheusRule, events, conditions | Metrics, events |

### Choose Velero when

- you need to back up or migrate Kubernetes **resources**, not just data
- volumes are very large and change slowly, so deduplication saves real money
- you need application hooks today, or a cloud-specific snapshot plugin
- a cluster admin team runs backups centrally and tenancy doesn't matter

### Choose Bardiup when

- the data in your volumes is what you can't recreate (your manifests are already in Git)
- you want "7 daily, 4 weekly, 12 monthly" without juggling several schedules
- application teams should own their backups without cluster-admin
- you want backups any engineer can restore with `tar`, years from now, without the tool or a repository password
- you don't want privileged or `hostPath` components on every node
- you run a self-hosted or regional S3-compatible store

### Using both

A common setup is GitOps (or Velero, with `--snapshot-volumes=false --default-volumes-to-fs-backup=false`) for cluster resources, and Bardiup for volume data and its retention.

## Other tools

| Tool | How it differs from Bardiup |
|---|---|
| **K8up** | Also namespaced, with restic-style `keep*` retention. Stores data in a restic repository (deduplicated and encrypted; restores need restic). A good choice if you want deduplication and restic's format. |
| **VolSync** | Focused on asynchronous replication of volumes between clusters (rsync, rclone, restic movers). Retention is restic-based for the restic mover. |
| **Kasten K10, Portworx Backup, Trilio** | Commercial platforms with UIs, application blueprints and policy engines. Much broader and much heavier. |
| **CSI snapshots alone** | Fast and cheap, but they usually live on the same storage system as the volume, so they don't protect you from losing it. Bardiup's `snapshot` method uses them as a consistent source and still ships the data off-cluster. |
| **Cron + `tar` scripts** | What Bardiup replaces. You keep the open format but gain scheduling, node-aware mounting, retention, checksums, restores, metrics and multi-tenancy. |
