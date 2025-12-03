# Bardiup Use Case Examples

Real-world backup configuration examples for common scenarios.

## Available Examples

| Example | Scenario | Key Features |
|---------|----------|--------------|
| [database-backup.yaml](database-backup.yaml) | Production database | Hourly backups, 5-year retention, comprehensive GFS |
| [development-environment.yaml](development-environment.yaml) | Dev/staging | Lightweight, minimal retention, weekday-only |
| [compliance-7-year.yaml](compliance-7-year.yaml) | Regulatory compliance | SOX/HIPAA, 7-year retention, immutable storage |
| [multi-tier-application.yaml](multi-tier-application.yaml) | E-commerce app | Different strategies per data tier |
| [disaster-recovery.yaml](disaster-recovery.yaml) | Cross-region DR | 1-hour RPO, recovery procedures |

## Quick Start

1. Choose the example that matches your use case
2. Copy and modify for your environment:
   - Update `pvcSelector` to match your PVCs
   - Update S3 bucket and credentials
   - Adjust retention policy as needed
3. Apply to your cluster:
   ```bash
   kubectl apply -f <example>.yaml
   ```

## Customizing Examples

### Adjust Backup Frequency

```yaml
# Every 6 hours
schedule: "0 */6 * * *"

# Daily at 2 AM
schedule: "0 2 * * *"

# Every 30 minutes (testing)
schedule: "*/30 * * * *"

# Weekdays only at 9 AM
schedule: "0 9 * * 1-5"
```

### Adjust Retention

```yaml
retentionPolicy:
  daily:
    keep: 7                    # Number of backups to keep
    selectionStrategy: newest  # newest, oldest, or distributed
```

### Change Storage Backend

```yaml
# AWS S3
storageBackend:
  type: s3
  s3:
    bucket: my-bucket
    region: us-east-1
    credentialsSecret: aws-creds

# MinIO
storageBackend:
  type: s3
  s3:
    bucket: my-bucket
    region: us-east-1
    endpoint: http://minio.minio.svc:9000
    forcePathStyle: true
    credentialsSecret: minio-creds
```

## Need Help?

- [User Guide](../../docs/USER_GUIDE.md)
- [Restore Guide](../../docs/RESTORE_GUIDE.md)
- [Full Documentation](../../docs/)

