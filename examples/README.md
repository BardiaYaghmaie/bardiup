# Examples

Ready-to-apply manifests. Each file starts with a comment explaining what it does and what it needs.

| File | What it shows |
|---|---|
| [quickstart.yaml](quickstart.yaml) | Nightly backups of every PVC labelled `backup=enabled`, keeping 7 daily, 4 weekly and 6 monthly. |
| [postgres-hourly-gfs.yaml](postgres-hourly-gfs.yaml) | Hourly crash-consistent CSI-snapshot backups of PostgreSQL, with a full GFS ladder (52 recovery points after a year instead of 8,760). |
| [self-hosted-s3.yaml](self-hosted-s3.yaml) | MinIO, Ceph RGW, RustFS, Garage, R2, B2, Wasabi, Hetzner and other S3-compatible stores. |
| [aws-irsa.yaml](aws-irsa.yaml) | AWS S3 with IRSA: no static keys in the cluster. |
| [restricted-namespace.yaml](restricted-namespace.yaml) | Namespaces that enforce the `restricted` Pod Security Standard. |
| [one-off-backup.yaml](one-off-backup.yaml) | A manual backup before a risky change, kept until you delete it. |
| [restore.yaml](restore.yaml) | Restore a backup into a new PVC next to the original. |
| [disaster-recovery.yaml](disaster-recovery.yaml) | Restore straight from a bucket key into a fresh cluster or another namespace. |

All examples go through the API server's schema validation in CI (`hack/e2e.sh`), so they stay in sync with the CRDs.
