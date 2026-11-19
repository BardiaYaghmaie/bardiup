#!/usr/bin/env bash
# Stress and chaos test. Tunables:
#   STRESS_PVCS=20        PVCs in the fan-out test
#   STRESS_TENANTS=4      namespaces with their own BackupConfig
#   STRESS_BIG_MB=2048    size of the large incompressible volume
#   STRESS_FILES=100000   small files in the large volume
#   STRESS_CHURN_MIN=5    minutes of every-minute scheduling with retention
# See hack/lib.sh for running against an existing cluster.
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

PVCS=${STRESS_PVCS:-20}
TENANTS=${STRESS_TENANTS:-4}
BIG_MB=${STRESS_BIG_MB:-2048}
FILES=${STRESS_FILES:-100000}
CHURN_MIN=${STRESS_CHURN_MIN:-5}
T0=$SECONDS

setup_cluster 2
deploy_s3
install_bardiup

backupconfig() { # backupconfig <ns> <name> <schedule> <selector-yaml> <retention-yaml>
  kubectl -n "$1" apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata: {name: $2}
spec:
  schedule: "$3"
  pvcSelector: $4
  storage: $(s3_storage "$1-$2")
  retention: $5
EOF
}

statefulset() { # statefulset <ns> <name> <replicas> <size> <script>
  kubectl -n "$1" apply -f - <<EOF
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: $2}
spec:
  replicas: $3
  podManagementPolicy: Parallel
  selector: {matchLabels: {app: $2}}
  template:
    metadata: {labels: {app: $2}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: w
          image: busybox:1.37
          command: [sh, -c, "$5; touch /tmp/ready; sleep infinity"]
          readinessProbe: {exec: {command: [cat, /tmp/ready]}, periodSeconds: 2}
          resources: {requests: {cpu: 5m, memory: 8Mi}}
          volumeMounts: [{name: data, mountPath: /data}]
  volumeClaimTemplates:
    - metadata: {name: data, labels: {set: $2}}
      spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: $4}}}
EOF
}

# ---------------------------------------------------------------------------
log "1. Fan-out: $PVCS PVCs in one namespace + $TENANTS tenant namespaces, all at once"
new_ns fanout
# shellcheck disable=SC2016 # expanded inside the pod
statefulset fanout db "$PVCS" 1Gi 'mkdir -p /data/d && for i in $(seq 1 200); do echo $HOSTNAME-$i > /data/d/f$i; done; head -c 5242880 /dev/urandom > /data/blob'
for t in $(seq 1 "$TENANTS"); do
  new_ns "tenant-$t"
  statefulset "tenant-$t" app 2 1Gi 'echo tenant > /data/f; head -c 1048576 /dev/urandom > /data/blob'
done
kubectl -n fanout rollout status sts/db --timeout=600s
for t in $(seq 1 "$TENANTS"); do kubectl -n "tenant-$t" rollout status sts/app --timeout=300s; done

backupconfig fanout all "0 3 1 1 *" "{matchLabels: {set: db}}" "{keepLast: 3}"
for t in $(seq 1 "$TENANTS"); do backupconfig "tenant-$t" nightly "0 3 1 1 *" "{}" "{daily: 7}"; done

start=$SECONDS
kubectl -n fanout annotate backupconfig all bardiup.io/backup-now=1 --overwrite
for t in $(seq 1 "$TENANTS"); do kubectl -n "tenant-$t" annotate backupconfig nightly bardiup.io/backup-now=1 --overwrite; done
wait_all fanout backups Completed "$PVCS" 900
for t in $(seq 1 "$TENANTS"); do wait_all "tenant-$t" backups Completed 2 300; done
echo "$((PVCS + TENANTS * 2)) backups completed in $((SECONDS - start))s"
kubectl -n fanout get backups --no-headers | head -5
pinned=$(kubectl -n fanout get backups -o jsonpath='{range .items[*]}{.status.message}{"\n"}{end}' | grep -c . || true)
echo "backups reporting status: $pinned"
footprint

# ---------------------------------------------------------------------------
log "2. Large volume: ${BIG_MB} MiB incompressible + $FILES small files"
new_ns big
kubectl -n big apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: big}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: $((BIG_MB / 1024 + 3))Gi}}}
---
apiVersion: batch/v1
kind: Job
metadata: {name: fill}
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: fill
          image: busybox:1.37
          command: [sh, -c]
          args:
            - |
              set -e
              head -c $((BIG_MB * 1048576)) /dev/urandom > /data/big.bin
              mkdir -p /data/small
              i=0; while [ \$i -lt $FILES ]; do d=/data/small/\$((i / 1000)); mkdir -p \$d; echo \$i > \$d/\$i; i=\$((i + 1)); done
          volumeMounts: [{name: d, mountPath: /data}]
      volumes: [{name: d, persistentVolumeClaim: {claimName: big}}]
EOF
kubectl -n big wait --for=condition=complete job/fill --timeout=1800s
BIG_SUM=$(checksum_pod big big .)
kubectl -n big apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: Backup
metadata: {name: big}
spec:
  pvcName: big
  storage: $(s3_storage big)
  job: {resources: {requests: {cpu: "1", memory: 256Mi}, limits: {memory: 1Gi}}}
EOF
start=$SECONDS
wait_for big backup/big '{.status.phase}' Completed 1800
secs=$((SECONDS - start))
echo "backup: $(kubectl -n big get backup big -o jsonpath='{.status.size}, {.status.files} files') in ${secs}s ($((BIG_MB / (secs > 0 ? secs : 1))) MiB/s)"
kubectl -n big apply -f - <<'EOF'
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: big}
spec: {backupName: big, target: {pvcName: big-restored}}
EOF
start=$SECONDS
wait_for big restore/big '{.status.phase}' Completed 1800
echo "restore: $(kubectl -n big get restore big -o jsonpath='{.status.files}') files in $((SECONDS - start))s"
[[ "$(checksum_pod big big-restored .)" == "$BIG_SUM" ]] || fail "large volume restore differs"
echo "large volume restored byte-for-byte"
kubectl -n big delete pvc big-restored --wait=false

# ---------------------------------------------------------------------------
log "3. Chaos: controller killed while backups run"
kubectl -n fanout annotate backupconfig all bardiup.io/backup-now=2 --overwrite
wait_for fanout backupconfig/all '{.status.lastBackupNowTrigger}' 2 60
sleep 2
kubectl -n bardiup-system delete pod -l app.kubernetes.io/name=bardiup --wait=false
sleep 5
kubectl -n bardiup-system delete pod -l app.kubernetes.io/name=bardiup --wait=false
kubectl -n bardiup-system rollout status deploy/bardiup --timeout=180s
wait_all fanout backups Completed $((PVCS * 2)) 900
dups=$(kubectl -n fanout get backups -o jsonpath='{range .items[*]}{.spec.pvcName}{"\n"}{end}' | sort | uniq -c | awk '$1 != 2' | wc -l | tr -d ' ')
[[ "$dups" == "0" ]] || fail "controller restarts produced missing or duplicate backups"
echo "all $((PVCS * 2)) backups completed exactly once across two controller restarts"

# ---------------------------------------------------------------------------
log "4. Object storage outage and recovery"
kubectl -n s3 scale deploy/rustfs --replicas=0
kubectl -n s3 wait --for=delete pod -l app=rustfs --timeout=120s
kubectl -n tenant-1 annotate backupconfig nightly bardiup.io/backup-now=outage --overwrite
wait_all tenant-1 backups Failed 2 600
kubectl -n tenant-1 get backups -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,MESSAGE:.status.message | head -5
wait_for tenant-1 backupconfig/nightly '{.status.conditions[?(@.type=="LastBackupSucceeded")].status}' False 60
kubectl -n s3 scale deploy/rustfs --replicas=1
kubectl -n s3 rollout status deploy/rustfs --timeout=180s
kubectl -n tenant-1 annotate backupconfig nightly bardiup.io/backup-now=recovered --overwrite
wait_all tenant-1 backups Completed 4 600
wait_for tenant-1 backupconfig/nightly '{.status.conditions[?(@.type=="LastBackupSucceeded")].status}' True 60
echo "failures were reported and the next run recovered"

# ---------------------------------------------------------------------------
log "5. Schedule churn: every minute for ${CHURN_MIN}m, keepLast 3, then verify no orphans"
new_ns churn
statefulset churn web 3 1Gi 'echo churn > /data/f'
kubectl -n churn rollout status sts/web --timeout=300s
backupconfig churn minutely "* * * * *" "{}" "{keepLast: 3}"
sleep $((CHURN_MIN * 60))
kubectl -n churn patch backupconfig minutely --type merge -p '{"spec":{"suspend":true}}'
for ((i = 0; i < 120; i += 3)); do
  active=$(kubectl -n churn get backupconfig minutely -o jsonpath='{.status.activeBackups}')
  [[ "$active" == "0" ]] && break
  sleep 3
done
sleep 5
kubectl -n churn get backups --sort-by=.metadata.creationTimestamp
for p in data-web-0 data-web-1 data-web-2; do
  n=$(kubectl -n churn get backups -o jsonpath="{range .items[?(@.spec.pvcName==\"$p\")]}{.status.phase}{\"\n\"}{end}" | grep -cx Completed || true)
  [[ "$n" == "3" ]] || fail "$p has $n completed backups, want 3"
done
crs=$(kubectl -n churn get backups -o name | wc -l | tr -d ' ')
objs=$(aws s3 ls --recursive s3://backups/churn-minutely/ | grep -c '\.tar\.gz$' || true)
[[ "$objs" == "$crs" ]] || fail "bucket has $objs archives but $crs Backup objects (orphans or missing data)"
pruned=$(kubectl get --raw /api/v1/namespaces/bardiup-system/services/bardiup-metrics:8080/proxy/metrics | awk '/^bardiup_retention_pruned_total\{.*churn/ {print $2}')
echo "retention held at 3 per PVC, $objs archives == $crs Backups, pruned since last restart: ${pruned:-0}"

# ---------------------------------------------------------------------------
log "6. Concurrent restores: $PVCS at once"
for b in $(kubectl -n fanout get backups -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.pvcName}{"\n"}{end}' | sort -k2 -u | awk '{print $1}'); do
  echo "{apiVersion: bardiup.io/v1alpha1, kind: Restore, metadata: {name: r-$b}, spec: {backupName: $b, target: {pvcName: r-$b}}}"
  echo "---"
done | kubectl -n fanout apply -f - >/dev/null
start=$SECONDS
wait_all fanout restores Completed "$PVCS" 900
echo "$PVCS restores completed in $((SECONDS - start))s"
sample=$(kubectl -n fanout get restores -o jsonpath='{.items[0].spec.target.pvcName}')
src=$(kubectl -n fanout get backup "${sample#r-}" -o jsonpath='{.spec.pvcName}')
want=$(kubectl -n fanout exec "${src#data-}" -- sh -c 'cd /data/d && find . -type f -exec md5sum {} + | sort -k2 | md5sum')
[[ "$(checksum_pod fanout "$sample" d)" == "$want" ]] || fail "concurrent restore $sample differs from $src"

footprint
log "Stress test passed in $(((SECONDS - T0) / 60))m"
