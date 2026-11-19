# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/). The `v1alpha1` API may change in minor releases before `v1`.

## [0.1.1] - 2026-11-19

### Fixed

- A backup or restore whose mover pod was on a node that went down stayed `Running` forever. It now fails once the Job's deadline passes, so the next run can start.

## [0.1.0] - 2026-11-19

First public release.

### Added

- `BackupConfig`, `Backup` and `Restore` namespaced APIs (`bardiup.io/v1alpha1`).
- Grandfather-Father-Son retention per PVC: `keepLast`, `hourly`, `daily`, `weekly`, `monthly`, `yearly` and fixed-length `custom` periods, evaluated in a configurable time zone. Each retained backup reports the rules keeping it in `status.retainedBy`.
- Backups are standard `tar.gz` archives with a JSON manifest and SHA-256, restorable with `tar` alone.
- `copy` method with automatic node pinning for `ReadWriteOnce` volumes in use, and `snapshot` method using CSI VolumeSnapshots for crash-consistent backups.
- Restores into new or existing PVCs, with checksum verification, preserved ownership, permissions, symlinks and hard links, optional `cleanTarget`, and disaster recovery straight from a bucket key (`spec.source`).
- On-demand backups via the `bardiup.io/backup-now` annotation; `suspend`; excludes; per-config Job resources, security context, service account, tolerations and node selector.
- Any S3-compatible object store, static credentials or the AWS default chain (IRSA, Pod Identity), SSE-S3 and SSE-KMS.
- Prometheus metrics, optional ServiceMonitor and alert rules, Kubernetes events, status conditions.
- Helm chart with RBAC generated from code, aggregated `admin`/`edit`/`view` roles for namespace self-service, and a single-file install manifest.
- Multi-arch (amd64, arm64), distroless, signed container image with SBOM and provenance.
