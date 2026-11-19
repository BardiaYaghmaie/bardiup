#!/usr/bin/env bash
# Creates a kind cluster with Bardiup, an S3 server and a small "shop" app for demo.tape.
# Usage: DEMO_KUBECONFIG=/tmp/demo-kubeconfig docs/demo/setup.sh && KUBECONFIG=/tmp/demo-kubeconfig vhs docs/demo/demo.tape
# shellcheck source=hack/lib.sh
E2E_KEEP=1 CLUSTER=bardiup-demo source "$(dirname "$0")/../../hack/lib.sh"

setup_cluster 1
deploy_s3
install_bardiup

new_ns shop
kubectl -n shop apply -f - <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data-postgres-0, labels: {app: postgres}}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: postgres}
spec:
  selector: {matchLabels: {app: postgres}}
  template:
    metadata: {labels: {app: postgres}}
    spec:
      containers:
        - name: db
          image: busybox:1.37
          command: [sh, -c, 'mkdir -p /data/pgdata && head -c 157286400 /dev/urandom > /data/pgdata/base.dat && echo ok > /data/pgdata/PG_VERSION && sleep infinity']
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: data-postgres-0}}]
YAML
kubectl -n shop rollout status deploy/postgres --timeout=180s

cat >"$ROOT/docs/demo/nightly.yaml" <<YAML
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata: {name: nightly}
spec:
  schedule: "0 2 * * *"
  pvcSelector: {matchLabels: {app: postgres}}
  storage: $(s3_storage prod)
  retention: {daily: 7, weekly: 4, monthly: 12, yearly: 5}
YAML
cp "$KUBECONFIG" "$DEMO_KUBECONFIG"
log "Demo ready"
