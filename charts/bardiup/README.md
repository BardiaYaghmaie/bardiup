# Bardiup Helm chart

[Bardiup](https://github.com/BardiaYaghmaie/bardiup) backs up Kubernetes PersistentVolumeClaims to any S3-compatible bucket and prunes them with Grandfather-Father-Son retention (hourly, daily, weekly, monthly, yearly). Backups are plain `tar.gz` files, and every namespace manages its own backups with the RBAC it already has.

## Install

```bash
helm install bardiup oci://ghcr.io/bardiayaghmaie/charts/bardiup -n bardiup-system --create-namespace
```

Requires Kubernetes 1.27+. The chart installs the CRDs, one controller Deployment, least-privilege RBAC generated from the code and, optionally, a ServiceMonitor and alert rules.

## Values

| Value | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/bardiayaghmaie/bardiup` | Controller and mover image. |
| `image.tag` | chart appVersion | |
| `image.pullPolicy` | `IfNotPresent` | Also used for mover pods. |
| `replicaCount` | `1` | Use 2 for faster failover; only the leader works. |
| `logLevel` | `info` | `info`, `debug` or a number. |
| `resources` | 10m / 48Mi request, 256Mi limit | Controller resources. |
| `serviceAccount.annotations` | `{}` | For example an IRSA role. |
| `aggregateToDefaultRoles` | `true` | Grant the built-in `admin`/`edit`/`view` roles access to Bardiup resources. |
| `metrics.enabled` | `true` | Expose Prometheus metrics on a Service. |
| `metrics.port` | `8080` | Metrics port. |
| `metrics.serviceMonitor.enabled` | `false` | Create a ServiceMonitor (Prometheus Operator). |
| `metrics.prometheusRule.enabled` | `false` | Create the default alerts. |
| `metrics.prometheusRule.staleAfterHours` | `26` | Alert when a PVC has no successful backup for this long. |

See the [configuration reference](https://github.com/BardiaYaghmaie/bardiup/blob/main/docs/configuration.md#helm-values) for every value, and the [operations guide](https://github.com/BardiaYaghmaie/bardiup/blob/main/docs/operations.md#upgrading) for upgrades: Helm doesn't upgrade CRDs.

## First backup

```bash
kubectl -n my-app create secret generic s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=... --from-literal=AWS_SECRET_ACCESS_KEY=...
curl -sLO https://raw.githubusercontent.com/BardiaYaghmaie/bardiup/main/examples/quickstart.yaml
# set your bucket and region, then:
kubectl -n my-app apply -f quickstart.yaml
kubectl -n my-app annotate bcfg nightly bardiup.io/backup-now="$(date +%s)" --overwrite
kubectl -n my-app get backups -w
```
