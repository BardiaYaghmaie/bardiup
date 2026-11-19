# shellcheck shell=bash
# Shared helpers for e2e.sh and stress.sh.
#
# By default a throwaway kind cluster is created with its own kubeconfig.
# KIND_IMAGE=kindest/node:<version> picks the Kubernetes version.
# To test an existing cluster instead, set BOTH:
#   E2E_KUBECONFIG=/path/to/kubeconfig   (never defaults to your own ~/.kube/config)
#   E2E_IMAGE=registry/bardiup:tag       (an image the cluster can pull)
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=$(mktemp -d)
CLUSTER=${CLUSTER:-bardiup-e2e}
S3_ENDPOINT="http://rustfs.s3.svc:9000"
CREATED_NS=()

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*"; dump; exit 1; }
dump() {
  kubectl get backupconfigs,backups,restores -A -o wide 2>/dev/null | tail -60 || true
  kubectl get jobs,pods -A -l app.kubernetes.io/managed-by=bardiup 2>/dev/null || true
  kubectl -n bardiup-system logs deploy/bardiup --tail=150 2>/dev/null || true
}

setup_cluster() {
  if [[ -n "${E2E_KUBECONFIG:-}" ]]; then
    [[ -n "${E2E_IMAGE:-}" ]] || { echo "E2E_IMAGE is required with E2E_KUBECONFIG"; exit 1; }
    export KUBECONFIG=$E2E_KUBECONFIG
    IMAGE_REPO=${E2E_IMAGE%:*} IMAGE_TAG=${E2E_IMAGE##*:} PULL=IfNotPresent
    log "Using existing cluster $(kubectl config current-context)"
    return
  fi
  export KUBECONFIG=$WORK/kubeconfig
  IMAGE_REPO=bardiup IMAGE_TAG=e2e PULL=Never
  log "Creating kind cluster ($1 workers)"
  {
    echo "kind: Cluster"
    echo "apiVersion: kind.x-k8s.io/v1alpha4"
    echo "nodes: [{role: control-plane}$(for _ in $(seq 1 "$1"); do printf ', {role: worker}'; done)]"
  } >"$WORK/kind.yaml"
  kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 180s ${KIND_IMAGE:+--image "$KIND_IMAGE"}
  log "Building and loading image"
  docker build -q -t bardiup:e2e --build-arg VERSION=e2e "$ROOT"
  docker image inspect bardiup:e2e --format 'image size: {{.Size}} bytes'
  kind load docker-image bardiup:e2e --name "$CLUSTER"
}

cleanup() {
  [[ -n "${E2E_KEEP:-}" ]] && { rm -rf "$WORK"; return; }
  if [[ -n "${E2E_KUBECONFIG:-}" ]]; then
    for ns in "${CREATED_NS[@]}"; do kubectl delete namespace "$ns" --wait=false >/dev/null 2>&1 || true; done
    helm uninstall bardiup -n bardiup-system >/dev/null 2>&1 || true
  else
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

new_ns() {
  kubectl create namespace "$1" >/dev/null
  CREATED_NS+=("$1")
  kubectl -n "$1" create secret generic s3 --from-literal=AWS_ACCESS_KEY_ID=admin --from-literal=AWS_SECRET_ACCESS_KEY=password123 >/dev/null
}

deploy_s3() {
  log "Deploying RustFS (S3-compatible) and an aws-cli toolbox"
  new_ns s3
  kubectl -n s3 apply -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: rustfs}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 20Gi}}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: rustfs}
spec:
  strategy: {type: Recreate}
  selector: {matchLabels: {app: rustfs}}
  template:
    metadata: {labels: {app: rustfs}}
    spec:
      securityContext: {runAsUser: 0}
      containers:
        - name: rustfs
          image: rustfs/rustfs:1.0.6
          env:
            - {name: RUSTFS_ACCESS_KEY, value: admin}
            - {name: RUSTFS_SECRET_KEY, value: password123}
            - {name: RUSTFS_VOLUMES, value: /data}
            - {name: RUSTFS_ADDRESS, value: ":9000"}
          readinessProbe: {tcpSocket: {port: 9000}, periodSeconds: 2}
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: rustfs}}]
---
apiVersion: v1
kind: Service
metadata: {name: rustfs}
spec: {selector: {app: rustfs}, ports: [{port: 9000}]}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: aws-config}
data:
  config: |
    [default]
    s3 =
      addressing_style = path
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: toolbox}
spec:
  selector: {matchLabels: {app: toolbox}}
  template:
    metadata: {labels: {app: toolbox}}
    spec:
      containers:
        - name: aws
          image: amazon/aws-cli:2.41.1
          command: [sleep, infinity]
          env:
            - {name: AWS_ACCESS_KEY_ID, value: admin}
            - {name: AWS_SECRET_ACCESS_KEY, value: password123}
            - {name: AWS_DEFAULT_REGION, value: us-east-1}
            - {name: AWS_CONFIG_FILE, value: /aws/config}
            - {name: AWS_ENDPOINT_URL, value: "http://rustfs.s3.svc:9000"}
            - {name: AWS_REQUEST_CHECKSUM_CALCULATION, value: when_required}
            - {name: AWS_RESPONSE_CHECKSUM_VALIDATION, value: when_required}
          volumeMounts: [{name: config, mountPath: /aws}]
      volumes: [{name: config, configMap: {name: aws-config}}]
EOF
  kubectl -n s3 rollout status deploy/rustfs --timeout=180s
  kubectl -n s3 rollout status deploy/toolbox --timeout=180s
  for _ in $(seq 1 30); do aws s3 mb s3://backups >/dev/null 2>&1 && return; sleep 2; done
  fail "could not create bucket"
}

aws() { kubectl -n s3 exec -i deploy/toolbox -- aws "$@"; }

# objects <prefix>: number of objects under s3://backups/<prefix>/
objects() { aws s3 ls --recursive "s3://backups/$1/" | grep -c . || true; }

# exists <key>: whether s3://backups/<key> exists
exists() { aws s3api head-object --bucket backups --key "$1" >/dev/null 2>&1; }

install_bardiup() {
  log "Installing Bardiup"
  CREATED_NS+=(bardiup-system)
  helm upgrade --install bardiup "$ROOT/charts/bardiup" -n bardiup-system --create-namespace \
    --set image.repository="$IMAGE_REPO" --set image.tag="$IMAGE_TAG" --set image.pullPolicy="$PULL" --wait --timeout 180s
}

s3_storage() { # s3_storage <prefix>
  echo "{s3: {bucket: backups, endpoint: \"$S3_ENDPOINT\", forcePathStyle: true, prefix: $1, credentialsSecretRef: {name: s3}}}"
}

# wait_for <namespace> <kind/name> <jsonpath> <value> [timeout-seconds]
wait_for() {
  local ns=$1 obj=$2 path=$3 want=$4 timeout=${5:-300} got=""
  for ((i = 0; i < timeout; i += 2)); do
    got=$(kubectl -n "$ns" get "$obj" -o jsonpath="$path" 2>/dev/null || true)
    [[ "$got" == "$want" ]] && return 0
    if [[ "$got" == "Failed" && "$want" != "Failed" ]]; then
      fail "$ns/$obj failed: $(kubectl -n "$ns" get "$obj" -o jsonpath='{.status.message}')"
    fi
    sleep 2
  done
  fail "$ns/$obj: $path is '$got', want '$want'"
}

# wait_all <namespace> <resource> <phase> <count> [timeout] [expected-failures]:
# waits until <count> objects are in <phase>, failing fast on unexpected failures.
wait_all() {
  local ns=$1 res=$2 want=$3 count=$4 timeout=${5:-600} allowed=${6:-0} n=0 failed phases
  for ((i = 0; i < timeout; i += 3)); do
    phases=$(kubectl -n "$ns" get "$res" -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}')
    n=$(grep -cx "$want" <<<"$phases" || true)
    [[ "$n" -ge "$count" ]] && return 0
    failed=$(grep -cx Failed <<<"$phases" || true)
    [[ "$want" != "Failed" && "$failed" -gt "$allowed" ]] && fail "$failed $res in $ns failed"
    sleep 3
  done
  fail "$ns: only $n/$count $res reached $want"
}

# on_pvc <namespace> <pvc> <shell command>: runs the command in a pod with the PVC at /data and
# prints its output. Fails if the command fails.
on_pvc() {
  local ns=$1 pvc=$2 name="probe-$RANDOM$RANDOM"
  kubectl -n "$ns" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: $name}
spec:
  restartPolicy: Never
  containers:
    - name: c
      image: busybox:1.37
      command: [sh, -c, '$3']
      volumeMounts: [{name: d, mountPath: /data}]
  volumes: [{name: d, persistentVolumeClaim: {claimName: $pvc}}]
EOF
  local phase=""
  for _ in $(seq 1 150); do
    phase=$(kubectl -n "$ns" get pod "$name" -o jsonpath='{.status.phase}')
    [[ "$phase" == Succeeded || "$phase" == Failed ]] && break
    sleep 2
  done
  kubectl -n "$ns" logs "$name"
  kubectl -n "$ns" delete pod "$name" --wait=false >/dev/null
  [[ "$phase" == Succeeded ]]
}

# checksum_pod <namespace> <pvc> <dir>: md5 over the sorted md5sums of files under <dir> in the PVC.
checksum_pod() {
  on_pvc "$1" "$2" "cd /data/$3 && find . -type f -exec md5sum {} + | sort -k2 | md5sum"
}

footprint() {
  log "Controller footprint"
  kubectl get --raw /api/v1/namespaces/bardiup-system/services/bardiup-metrics:8080/proxy/metrics |
    grep -E '^(process_resident_memory_bytes|process_cpu_seconds_total|go_goroutines|bardiup_backups_total|bardiup_restores_total|bardiup_retention_pruned_total)' || true
}
