#!/usr/bin/env bash
#
# Smoke-test the proxysql-agent against a real OrbStack Kubernetes cluster.
# Deploys a core/satellite ProxySQL topology matching persona-kubernetes
# proxysql-stack, with the locally-built agent image as the sidecar.
#
# Usage (from repo root):
#   ./scripts/smoke_tests/orbstack_agent_smoke.sh
#
# Environment:
#   SMOKE_TIMEOUT_SECONDS       Overall wait budget per assertion phase (default: 180)
#   SMOKE_KEEP                  If 1, leave namespaces after PASS/FAIL (default: 0)
#   SMOKE_MYSQL_PRIMARY_HOST    Override primary backend host (default: mysql-primary.mysql.svc.cluster.local)
#   SMOKE_MYSQL_REPLICA_HOST    Override replica backend host (default: mysql-replica.mysql.svc.cluster.local)
#   SMOKE_MYSQL_PRIMARY_PORT    Primary MySQL port (default: 3306)
#   SMOKE_MYSQL_REPLICA_PORT    Replica MySQL port (default: 3306)
#   SMOKE_MYSQL_USER            ProxySQL mysql_users primary username (default: smoke)
#   SMOKE_MYSQL_REPLICA_USER    ProxySQL mysql_users replica username (default: smoke-replica)
#   SMOKE_MYSQL_PASSWORD        MySQL user password (default: smoke)
#   SMOKE_CORE_REPLICAS         Core deployment replicas (default: 2)
#   SMOKE_SATELLITE_REPLICAS    Satellite deployment replicas (default: 2)
#   SMOKE_SKIP_BUILD            If 1, skip docker build of proxysql-agent:smoke (default: 0)
#   SMOKE_AGENT_IMAGE           Agent image tag (default: proxysql-agent:smoke)
#
# Prerequisites:
#   - OrbStack installed with Kubernetes enabled (`orb start k8s`)
#   - docker, kubectl, curl available on PATH
#   - For default backends: none (in-cluster MySQL is applied by this script)
#   - For persona-web override: persona-web compose MySQL running, e.g.
#       SMOKE_MYSQL_PRIMARY_HOST=mysql.persona-web.orb.local \
#       SMOKE_MYSQL_REPLICA_HOST=mysql.persona-web.orb.local \
#       SMOKE_MYSQL_USER=root SMOKE_MYSQL_PASSWORD=password \
#       SMOKE_MYSQL_REPLICA_USER=root \
#       ./scripts/smoke_tests/orbstack_agent_smoke.sh

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
readonly DEPLOY_DIR="${REPO_ROOT}/deploy/orbstack"
readonly TEMPLATE_DIR="${DEPLOY_DIR}/templates"
readonly RENDER_DIR="$(mktemp -d "${TMPDIR:-/tmp}/proxysql-agent-smoke.XXXXXX")"

readonly SMOKE_TIMEOUT_SECONDS="${SMOKE_TIMEOUT_SECONDS:-180}"
readonly SMOKE_KEEP="${SMOKE_KEEP:-0}"
readonly SMOKE_SKIP_BUILD="${SMOKE_SKIP_BUILD:-0}"
readonly SMOKE_AGENT_IMAGE="${SMOKE_AGENT_IMAGE:-proxysql-agent:smoke}"
readonly SMOKE_CORE_REPLICAS="${SMOKE_CORE_REPLICAS:-2}"
readonly SMOKE_SATELLITE_REPLICAS="${SMOKE_SATELLITE_REPLICAS:-2}"

# Defaults point at in-cluster MySQL. Override both hosts to skip deploying it.
export SMOKE_MYSQL_PRIMARY_HOST="${SMOKE_MYSQL_PRIMARY_HOST:-mysql-primary.mysql.svc.cluster.local}"
export SMOKE_MYSQL_REPLICA_HOST="${SMOKE_MYSQL_REPLICA_HOST:-mysql-replica.mysql.svc.cluster.local}"
export SMOKE_MYSQL_PRIMARY_PORT="${SMOKE_MYSQL_PRIMARY_PORT:-3306}"
export SMOKE_MYSQL_REPLICA_PORT="${SMOKE_MYSQL_REPLICA_PORT:-3306}"
export SMOKE_MYSQL_USER="${SMOKE_MYSQL_USER:-smoke}"
export SMOKE_MYSQL_REPLICA_USER="${SMOKE_MYSQL_REPLICA_USER:-smoke-replica}"
export SMOKE_MYSQL_PASSWORD="${SMOKE_MYSQL_PASSWORD:-smoke}"

readonly DEFAULT_PRIMARY_HOST="mysql-primary.mysql.svc.cluster.local"
readonly DEFAULT_REPLICA_HOST="mysql-replica.mysql.svc.cluster.local"
readonly ORBSTACK_KUBECONFIG="${HOME}/.orbstack/k8s/config.yml"

POLL_INTERVAL_SECONDS=5
USE_INCLUSTER_MYSQL=1

cleanup() {
  local exit_code=$?
  if [[ "${SMOKE_KEEP}" != "1" ]]; then
    log "==> tearing down smoke namespaces"
    kubectl delete namespace proxysql --ignore-not-found --wait=false >/dev/null 2>&1 || true
    if [[ "${USE_INCLUSTER_MYSQL}" -eq 1 ]]; then
      kubectl delete namespace mysql --ignore-not-found --wait=false >/dev/null 2>&1 || true
    fi
  else
    log "==> SMOKE_KEEP=1; leaving namespaces in place for debugging"
  fi
  rm -rf "${RENDER_DIR}"
  exit "${exit_code}"
}

trap cleanup EXIT

die() {
  echo "FAIL: $*" >&2
  exit 1
}

log() {
  echo "$*"
}

step() {
  log "==> $*"
}

pass() {
  log "PASS: $*"
}

# Prefer OrbStack's dedicated kubeconfig so we do not clobber stack-* contexts.
ensure_orbstack_kubeconfig() {
  if [[ -f "${ORBSTACK_KUBECONFIG}" ]]; then
    export KUBECONFIG="${ORBSTACK_KUBECONFIG}"
  fi
}

preflight() {
  step "preflight"
  command -v docker >/dev/null || die "docker not found on PATH"
  command -v kubectl >/dev/null || die "kubectl not found on PATH"
  command -v curl >/dev/null || die "curl not found on PATH"
  command -v orb >/dev/null || die "orb (OrbStack) not found. Install from https://orbstack.dev/"

  step "ensuring OrbStack Kubernetes is running"
  orb start k8s >/dev/null 2>&1 || true
  ensure_orbstack_kubeconfig

  local attempts=0
  until kubectl --context=orbstack get --raw='/readyz' >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 30 ]]; then
      die "OrbStack Kubernetes did not become ready. Enable Kubernetes in OrbStack Settings or run: orb start k8s"
    fi
    sleep 2
  done
  kubectl config use-context orbstack >/dev/null
  log "kubectl context: $(kubectl config current-context)"

  if [[ "${SMOKE_MYSQL_PRIMARY_HOST}" != "${DEFAULT_PRIMARY_HOST}" ]] \
    || [[ "${SMOKE_MYSQL_REPLICA_HOST}" != "${DEFAULT_REPLICA_HOST}" ]]; then
    USE_INCLUSTER_MYSQL=0
    log "using external MySQL backends: primary=${SMOKE_MYSQL_PRIMARY_HOST}:${SMOKE_MYSQL_PRIMARY_PORT} replica=${SMOKE_MYSQL_REPLICA_HOST}:${SMOKE_MYSQL_REPLICA_PORT}"
  else
    log "using in-cluster MySQL backends"
  fi
}

reset_namespaces() {
  step "resetting prior smoke namespaces"
  kubectl delete namespace proxysql --ignore-not-found --wait=true --timeout=120s >/dev/null 2>&1 || true
  if [[ "${USE_INCLUSTER_MYSQL}" -eq 1 ]]; then
    kubectl delete namespace mysql --ignore-not-found --wait=true --timeout=120s >/dev/null 2>&1 || true
  fi
}

build_agent_image() {
  if [[ "${SMOKE_SKIP_BUILD}" == "1" ]]; then
    step "skipping agent image build (SMOKE_SKIP_BUILD=1)"
    docker image inspect "${SMOKE_AGENT_IMAGE}" >/dev/null 2>&1 \
      || die "image ${SMOKE_AGENT_IMAGE} not found locally; build it or unset SMOKE_SKIP_BUILD"
    return
  fi
  step "building agent image ${SMOKE_AGENT_IMAGE}"
  docker build -f "${REPO_ROOT}/build/dev.Dockerfile" -t "${SMOKE_AGENT_IMAGE}" "${REPO_ROOT}"
}

deploy_mysql() {
  if [[ "${USE_INCLUSTER_MYSQL}" -ne 1 ]]; then
    step "skipping in-cluster MySQL deploy (external hosts configured)"
    return
  fi
  step "deploying in-cluster MySQL"
  kubectl apply -k "${DEPLOY_DIR}/mysql"
  kubectl -n mysql rollout status deployment/mysql-primary --timeout="${SMOKE_TIMEOUT_SECONDS}s"
  kubectl -n mysql rollout status deployment/mysql-replica --timeout="${SMOKE_TIMEOUT_SECONDS}s"
}

render_proxysql_config() {
  step "rendering ProxySQL ConfigMaps (primary=${SMOKE_MYSQL_PRIMARY_HOST} replica=${SMOKE_MYSQL_REPLICA_HOST})"
  local core_cnf="${RENDER_DIR}/proxysql-core.cnf"
  local satellite_cnf="${RENDER_DIR}/proxysql-satellite.cnf"

  # envsubst only replaces exported vars; all SMOKE_MYSQL_* are exported above.
  if command -v envsubst >/dev/null; then
    envsubst <"${TEMPLATE_DIR}/proxysql-core.cnf.tmpl" >"${core_cnf}"
    envsubst <"${TEMPLATE_DIR}/proxysql-satellite.cnf.tmpl" >"${satellite_cnf}"
  else
    # Fallback without gettext: only substitute the known placeholders.
    sed \
      -e "s|\${SMOKE_MYSQL_PRIMARY_HOST}|${SMOKE_MYSQL_PRIMARY_HOST}|g" \
      -e "s|\${SMOKE_MYSQL_REPLICA_HOST}|${SMOKE_MYSQL_REPLICA_HOST}|g" \
      -e "s|\${SMOKE_MYSQL_PRIMARY_PORT}|${SMOKE_MYSQL_PRIMARY_PORT}|g" \
      -e "s|\${SMOKE_MYSQL_REPLICA_PORT}|${SMOKE_MYSQL_REPLICA_PORT}|g" \
      -e "s|\${SMOKE_MYSQL_USER}|${SMOKE_MYSQL_USER}|g" \
      -e "s|\${SMOKE_MYSQL_REPLICA_USER}|${SMOKE_MYSQL_REPLICA_USER}|g" \
      -e "s|\${SMOKE_MYSQL_PASSWORD}|${SMOKE_MYSQL_PASSWORD}|g" \
      "${TEMPLATE_DIR}/proxysql-core.cnf.tmpl" >"${core_cnf}"
    cp "${TEMPLATE_DIR}/proxysql-satellite.cnf.tmpl" "${satellite_cnf}"
  fi

  kubectl apply -f "${DEPLOY_DIR}/proxysql/namespace.yaml"
  kubectl -n proxysql create configmap proxysql-config \
    --from-file=proxysql-core.cnf="${core_cnf}" \
    --from-file=proxysql-satellite.cnf="${satellite_cnf}" \
    --dry-run=client -o yaml | kubectl apply -f -
}

deploy_proxysql() {
  step "deploying ProxySQL core + satellite"
  kubectl apply -k "${DEPLOY_DIR}/proxysql"
  # ConfigMap is applied above; re-apply kustomize may not include it (generated).
  # Scale to requested replica counts.
  kubectl -n proxysql scale deployment/proxysql-core --replicas="${SMOKE_CORE_REPLICAS}"
  kubectl -n proxysql scale deployment/proxysql-satellite --replicas="${SMOKE_SATELLITE_REPLICAS}"

  step "waiting for core pods ready"
  kubectl -n proxysql rollout status deployment/proxysql-core --timeout="${SMOKE_TIMEOUT_SECONDS}s"
  step "waiting for satellite pods ready"
  kubectl -n proxysql rollout status deployment/proxysql-satellite --timeout="${SMOKE_TIMEOUT_SECONDS}s"
}

# Run a mysql client query against ProxySQL admin on a named pod.
admin_query() {
  local pod="$1"
  local sql="$2"
  kubectl -n proxysql exec "${pod}" -c proxysql -- \
    mysql -h127.0.0.1 -P6032 -uradmin -pradmin -N -B -e "${sql}"
}

# Run a mysql client query against ProxySQL mysql port on a satellite pod.
mysql_query_on_pod() {
  local pod="$1"
  local sql="$2"
  kubectl -n proxysql exec "${pod}" -c proxysql -- \
    mysql -h127.0.0.1 -P6033 -u"${SMOKE_MYSQL_USER}" -p"${SMOKE_MYSQL_PASSWORD}" -N -B -e "${sql}"
}

first_pod() {
  local component="$1"
  kubectl -n proxysql get pods -l "app=proxysql,component=${component}" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}'
}

core_pod_ips() {
  kubectl -n proxysql get pods -l 'app=proxysql,component=core' \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}'
}

# Fetch an agent health endpoint via the proxysql container (shares the pod network;
# the alpine agent image has no wget/curl).
health_get() {
  local pod="$1"
  local endpoint="$2"
  kubectl -n proxysql exec "${pod}" -c proxysql -- \
    wget -qO- "http://127.0.0.1:8080${endpoint}"
}

assert_health() {
  step "assert agent health endpoints"
  local pod body
  pod="$(first_pod core)"
  [[ -n "${pod}" ]] || die "no running core pod found"

  local endpoint
  for endpoint in /healthz/started /healthz/ready /healthz/live; do
    body="$(health_get "${pod}" "${endpoint}")" \
      || die "failed to GET ${endpoint} on ${pod}"
    log "${endpoint}: ${body}"
    echo "${body}" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' \
      || die "${endpoint} missing status=ok: ${body}"
  done

  # Wait until at least one backend is ONLINE (readiness can be 200 with
  # "some backends offline" while MySQL is still settling).
  local deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  while true; do
    body="$(health_get "${pod}" /healthz/ready)"
    if echo "${body}" | grep -Eq '"online"[[:space:]]*:[[:space:]]*[1-9]'; then
      pass "health endpoints ok on ${pod} (${body})"
      return
    fi
    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for backends.online >= 1: ${body}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

assert_core_membership() {
  step "assert core agent registered pod IPs in proxysql_servers"
  local expected_count="${SMOKE_CORE_REPLICAS}"
  local deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  local pod servers

  while true; do
    pod="$(first_pod core)"
    [[ -n "${pod}" ]] || die "no running core pod for membership check"

    servers="$(admin_query "${pod}" "SELECT hostname FROM proxysql_servers" 2>/dev/null || true)"
    log "proxysql_servers on ${pod}: $(echo "${servers}" | tr '\n' ' ')"

    local matched=0
    local ip
    while IFS= read -r ip; do
      [[ -z "${ip}" ]] && continue
      if echo "${servers}" | grep -qxF "${ip}"; then
        matched=$((matched + 1))
      fi
    done < <(core_pod_ips)

    if [[ "${matched}" -ge "${expected_count}" ]]; then
      # Placeholder should be gone once cores register.
      if echo "${servers}" | grep -qxF "proxysql-core"; then
        log "warning: proxysql-core placeholder still present alongside pod IPs"
      fi
      pass "core membership: ${matched}/${expected_count} pod IPs registered"
      return
    fi

    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for ${expected_count} core IPs in proxysql_servers (matched=${matched}, servers=$(echo "${servers}" | tr '\n' ','))"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

assert_satellite_sync() {
  step "assert satellites synced mysql_servers from core"
  local deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  local pod count

  while true; do
    pod="$(first_pod satellite)"
    [[ -n "${pod}" ]] || die "no running satellite pod for sync check"

    count="$(admin_query "${pod}" "SELECT COUNT(*) FROM runtime_mysql_servers" 2>/dev/null || echo 0)"
    log "satellite ${pod} runtime_mysql_servers count=${count}"

    if [[ "${count}" -ge 1 ]]; then
      pass "satellite synced backends (runtime_mysql_servers=${count})"
      return
    fi

    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for satellite runtime_mysql_servers to sync from core"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

assert_traffic() {
  step "assert MySQL traffic through satellite"
  local pod result
  pod="$(first_pod satellite)"
  [[ -n "${pod}" ]] || die "no running satellite pod for traffic check"

  # Client → proxysql service → satellite → MySQL backend.
  kubectl -n proxysql delete pod smoke-mysql-client --ignore-not-found --wait=true >/dev/null 2>&1 || true
  result="$(
    kubectl -n proxysql run smoke-mysql-client --rm -i --restart=Never \
      --image=mysql:8.0.36 \
      --command -- \
      mysql -hproxysql.proxysql.svc.cluster.local -P6033 \
        -u"${SMOKE_MYSQL_USER}" -p"${SMOKE_MYSQL_PASSWORD}" \
        -N -B -e 'SELECT 1' 2>/dev/null \
      | tr -d '\r' | grep -E '^[0-9]+$' | tail -n1 || true
  )"

  if [[ "${result}" != "1" ]]; then
    log "service path query returned '${result}'; falling back to pod-local 6033"
    result="$(mysql_query_on_pod "${pod}" "SELECT 1" | tr -d '\r' | grep -E '^[0-9]+$' | tail -n1 || true)"
  fi

  [[ "${result}" == "1" ]] || die "traffic path failed: expected SELECT 1 => 1, got '${result}'"
  pass "traffic path ok (SELECT 1 through ProxySQL)"
}

assert_core_resync() {
  step "assert core membership heals after deleting a core pod"
  local before_ips deleted_ip remaining_pod deadline servers matched
  before_ips="$(core_pod_ips)"
  [[ -n "${before_ips}" ]] || die "no core pod IPs before delete"

  local victim
  victim="$(first_pod core)"
  deleted_ip="$(kubectl -n proxysql get pod "${victim}" -o jsonpath='{.status.podIP}')"
  log "deleting core pod ${victim} (ip=${deleted_ip}) to force agent reconcile"

  kubectl -n proxysql delete pod "${victim}" --wait=true --timeout="${SMOKE_TIMEOUT_SECONDS}s"

  # Wait until the deleted IP is gone from proxysql_servers on a surviving/new core.
  deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  local stale_gone=0
  while true; do
    remaining_pod="$(first_pod core)"
    [[ -n "${remaining_pod}" ]] || die "no core pods after delete"

    servers="$(admin_query "${remaining_pod}" "SELECT hostname FROM proxysql_servers" 2>/dev/null || true)"
    log "post-delete proxysql_servers on ${remaining_pod}: $(echo "${servers}" | tr '\n' ' ')"

    if ! echo "${servers}" | grep -qxF "${deleted_ip}"; then
      stale_gone=1
      break
    fi

    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for stale core IP ${deleted_ip} to leave proxysql_servers (finished without reconcile?)"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
  [[ "${stale_gone}" -eq 1 ]] || die "stale IP ${deleted_ip} still present"

  # Wait for replacement core to register (back to expected replica count).
  deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  kubectl -n proxysql rollout status deployment/proxysql-core --timeout="${SMOKE_TIMEOUT_SECONDS}s"

  while true; do
    remaining_pod="$(first_pod core)"
    servers="$(admin_query "${remaining_pod}" "SELECT hostname FROM proxysql_servers" 2>/dev/null || true)"
    matched=0
    local ip
    while IFS= read -r ip; do
      [[ -z "${ip}" ]] && continue
      if echo "${servers}" | grep -qxF "${ip}"; then
        matched=$((matched + 1))
      fi
    done < <(core_pod_ips)

    log "post-replace membership matched=${matched}/${SMOKE_CORE_REPLICAS}"
    if [[ "${matched}" -ge "${SMOKE_CORE_REPLICAS}" ]]; then
      pass "core resync ok (stale ${deleted_ip} removed; ${matched} live IPs registered)"
      return
    fi

    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for replacement core to register in proxysql_servers (matched=${matched})"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

main() {
  cd "${REPO_ROOT}"
  preflight
  reset_namespaces
  build_agent_image
  deploy_mysql
  render_proxysql_config
  deploy_proxysql
  assert_health
  assert_core_membership
  assert_satellite_sync
  assert_traffic
  assert_core_resync
  pass "orbstack proxysql-agent smoke test"
}

main "$@"
