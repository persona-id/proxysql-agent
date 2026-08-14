#!/usr/bin/env bash
#
# Smoke-test the proxysql-agent against a real OrbStack Kubernetes cluster.
# Deploys a core/satellite ProxySQL topology matching persona-kubernetes
# proxysql-stack, with the locally-built agent image as the sidecar.
#
# Assertions:
#   - agent health endpoints (/healthz/{started,ready,live})
#   - core membership (pod IPs in proxysql_servers)
#   - satellite sync of runtime_mysql_servers from cores
#   - MySQL traffic through the proxysql service
#   - core membership heals after deleting a core pod
#   - NEW CONFIG SURVIVES CORE ROLLING UPDATE: re-render ConfigMap with a new
#     mysql_query_rules marker, rollout-restart cores (maxSurge=1 so old+new
#     coexist), delete a core mid-rollout to fire membership handlers, then
#     assert every core+satellite carries the new marker and none revert to
#     the old one. This is the production "had to kill all core pods" bug.
#   - BACKEND REMOVAL SURVIVES CORE ROLLING UPDATE: drop the HG2 replica from
#     the ConfigMap, roll cores the same way, then assert the removed host is
#     gone from runtime_mysql_servers on every core+satellite (not merely
#     SHUNNED). Catches ProxySQL's FROM CONFIG merge leaving peer-synced
#     ghost backends when stampOwnConfig omits DELETE FROM mysql_servers.
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
#   SMOKE_CORE_REPLICAS         Core deployment replicas (default: 2; rollout phase needs >= 2)
#   SMOKE_SATELLITE_REPLICAS    Satellite deployment replicas (default: 2)
#   SMOKE_SKIP_BUILD            If 1, skip docker build of proxysql-agent:smoke (default: 0)
#   SMOKE_AGENT_IMAGE           Agent image tag (default: proxysql-agent:smoke)
#   SMOKE_CONFIG_MARKER         Fingerprint comment in core mysql_query_rules (default: smoke-baseline)
#   SMOKE_SKIP_CONFIG_ROLLOUT   If 1, skip the rolling-config-revert assertion (default: 0)
#   SMOKE_INCLUDE_MYSQL_REPLICA If 0, omit the HG2 replica from core mysql_servers
#                               when rendering (default: 1; set to 0 by the backend-
#                               removal smoke phase)
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
readonly SMOKE_SKIP_CONFIG_ROLLOUT="${SMOKE_SKIP_CONFIG_ROLLOUT:-0}"

# Defaults point at in-cluster MySQL. Override both hosts to skip deploying it.
export SMOKE_MYSQL_PRIMARY_HOST="${SMOKE_MYSQL_PRIMARY_HOST:-mysql-primary.mysql.svc.cluster.local}"
export SMOKE_MYSQL_REPLICA_HOST="${SMOKE_MYSQL_REPLICA_HOST:-mysql-replica.mysql.svc.cluster.local}"
export SMOKE_MYSQL_PRIMARY_PORT="${SMOKE_MYSQL_PRIMARY_PORT:-3306}"
export SMOKE_MYSQL_REPLICA_PORT="${SMOKE_MYSQL_REPLICA_PORT:-3306}"
export SMOKE_MYSQL_USER="${SMOKE_MYSQL_USER:-smoke}"
export SMOKE_MYSQL_REPLICA_USER="${SMOKE_MYSQL_REPLICA_USER:-smoke-replica}"
export SMOKE_MYSQL_PASSWORD="${SMOKE_MYSQL_PASSWORD:-smoke}"
# Fingerprint written into core mysql_query_rules.comment; re-rendered during the
# rolling-config phase so we can detect stale-peer revert.
export SMOKE_CONFIG_MARKER="${SMOKE_CONFIG_MARKER:-smoke-baseline}"
# When 0, render_proxysql_config strips the HG2 replica stanza from the core cnf.
export SMOKE_INCLUDE_MYSQL_REPLICA="${SMOKE_INCLUDE_MYSQL_REPLICA:-1}"

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

# Drop the marked HG2 replica stanza from a rendered core cnf (in place).
omit_replica_from_core_cnf() {
  local core_cnf="$1"
  # BSD/GNU sed: delete from the begin marker through the end marker inclusive.
  sed -i.bak '/# SMOKE_REPLICA_BEGIN/,/# SMOKE_REPLICA_END/d' "${core_cnf}"
  rm -f "${core_cnf}.bak"
}

render_proxysql_config() {
  local replica_desc="${SMOKE_MYSQL_REPLICA_HOST}"
  if [[ "${SMOKE_INCLUDE_MYSQL_REPLICA}" != "1" ]]; then
    replica_desc="(omitted)"
  fi
  step "rendering ProxySQL ConfigMaps (primary=${SMOKE_MYSQL_PRIMARY_HOST} replica=${replica_desc})"
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
      -e "s|\${SMOKE_CONFIG_MARKER}|${SMOKE_CONFIG_MARKER}|g" \
      "${TEMPLATE_DIR}/proxysql-core.cnf.tmpl" >"${core_cnf}"
    cp "${TEMPLATE_DIR}/proxysql-satellite.cnf.tmpl" "${satellite_cnf}"
  fi

  if [[ "${SMOKE_INCLUDE_MYSQL_REPLICA}" != "1" ]]; then
    omit_replica_from_core_cnf "${core_cnf}"
    grep -qF "${SMOKE_MYSQL_REPLICA_HOST}" "${core_cnf}" \
      && die "SMOKE_INCLUDE_MYSQL_REPLICA=0 but replica host '${SMOKE_MYSQL_REPLICA_HOST}' still present in rendered core cnf"
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

# Read the smoke fingerprint from a core/satellite pod's mysql_query_rules table.
# Prefers runtime_ (what clustering syncs); falls back to disk table.
config_marker_on_pod() {
  local pod="$1"
  local marker=""
  marker="$(admin_query "${pod}" "SELECT comment FROM runtime_mysql_query_rules WHERE rule_id=9001" 2>/dev/null | tr -d '\r' | head -n1 || true)"
  if [[ -z "${marker}" ]]; then
    marker="$(admin_query "${pod}" "SELECT comment FROM mysql_query_rules WHERE rule_id=9001" 2>/dev/null | tr -d '\r' | head -n1 || true)"
  fi
  printf '%s' "${marker}"
}

running_core_pods() {
  kubectl -n proxysql get pods -l 'app=proxysql,component=core' \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
}

# Assert every currently-Running core carries expected_marker.
# Returns 0 on full match, 1 if any core is still missing it (caller polls).
# Logs each pod's marker so the transcript shows convergence / revert.
assert_cores_have_marker() {
  local expected_marker="$1"
  local pod marker
  local matched=0
  local total=0
  local unexpected=0

  while IFS= read -r pod; do
    [[ -z "${pod}" ]] && continue
    total=$((total + 1))
    marker="$(config_marker_on_pod "${pod}")"
    log "core ${pod} config marker='${marker}' (want '${expected_marker}')"

    if [[ "${marker}" == "${expected_marker}" ]]; then
      matched=$((matched + 1))
    else
      unexpected=$((unexpected + 1))
    fi
  done < <(running_core_pods)

  [[ "${total}" -gt 0 ]] || die "no running core pods while asserting marker '${expected_marker}'"
  [[ "${matched}" -eq "${total}" ]] || return 1
  return 0
}

wait_cores_have_marker() {
  local expected_marker="$1"
  local forbidden_marker="${2:-}"
  local deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  local pod marker seen_new=0

  while true; do
    if assert_cores_have_marker "${expected_marker}"; then
      # After convergence, ensure no core still carries the forbidden old marker.
      if [[ -n "${forbidden_marker}" ]]; then
        while IFS= read -r pod; do
          [[ -z "${pod}" ]] && continue
          marker="$(config_marker_on_pod "${pod}")"
          if [[ "${marker}" == "${forbidden_marker}" ]]; then
            die "config reverted on ${pod}: has forbidden marker '${forbidden_marker}' after all cores briefly held '${expected_marker}'"
          fi
        done < <(running_core_pods)
      fi
      return 0
    fi

    # True-revert signal: at least one core already had the new marker, then a
    # later poll shows the forbidden old marker on a Running core.
    if [[ -n "${forbidden_marker}" ]]; then
      while IFS= read -r pod; do
        [[ -z "${pod}" ]] && continue
        marker="$(config_marker_on_pod "${pod}")"
        if [[ "${marker}" == "${expected_marker}" ]]; then
          seen_new=1
        elif [[ "${seen_new}" -eq 1 && "${marker}" == "${forbidden_marker}" ]]; then
          die "config reverted on ${pod}: had peers on '${expected_marker}' then this core shows '${forbidden_marker}' (stale peer re-stamp)"
        fi
      done < <(running_core_pods)
    fi

    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for all cores to carry marker '${expected_marker}' (a stale peer may have re-stamped the old config; see marker logs above)"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

# Force the production failure mode: change core config, roll cores with
# maxSurge=1 so old and new coexist, amplify membership churn mid-rollout, and
# assert the new fingerprint sticks on every core (no stale-peer revert).
assert_config_survives_rolling_update() {
  if [[ "${SMOKE_SKIP_CONFIG_ROLLOUT}" == "1" ]]; then
    step "skipping config-rollout assertion (SMOKE_SKIP_CONFIG_ROLLOUT=1)"
    return
  fi

  step "assert new core config survives rolling update (no stale-peer revert)"
  if [[ "${SMOKE_CORE_REPLICAS}" -lt 2 ]]; then
    die "config-rollout smoke requires SMOKE_CORE_REPLICAS>=2 (got ${SMOKE_CORE_REPLICAS}); the revert only fires when an old core coexists with a new one"
  fi

  local old_marker new_marker
  old_marker="${SMOKE_CONFIG_MARKER}"
  new_marker="smoke-rollout-$(date +%s)"

  # Make the "before" state false first: confirm baseline marker is present, then
  # replace it. Passing because pods never had the old marker would be a tautology.
  wait_cores_have_marker "${old_marker}"
  pass "baseline config marker '${old_marker}' present on all cores before rollout"

  log "rendering + applying new core ConfigMap with marker='${new_marker}'"
  export SMOKE_CONFIG_MARKER="${new_marker}"
  render_proxysql_config

  # Tie the pod template to the marker so the rollout must replace every core
  # (ConfigMap content alone does not change the pod template hash). Rolling
  # restart with maxSurge=1 means a new-config pod boots alongside old-config
  # peers — exactly the window where membership LOAD used to re-stamp stale
  # epochs; mid-rollout delete amplifies that churn.
  roll_cores_with_membership_churn "${new_marker}"

  # New marker must win on every core; seeing the old marker after rollout is the
  # revert bug (would previously require kill-all cores to recover).
  wait_cores_have_marker "${new_marker}" "${old_marker}"
  pass "all cores kept new config marker '${new_marker}' through rolling update (old '${old_marker}' gone)"

  # Satellites pull mysql_query_rules from cores — confirm the new marker landed
  # on the data plane too, not just the control plane.
  step "assert satellites pulled new config marker from cores"
  local deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
  local sat_pod sat_marker
  while true; do
    sat_pod="$(first_pod satellite)"
    [[ -n "${sat_pod}" ]] || die "no running satellite after config rollout"
    sat_marker="$(config_marker_on_pod "${sat_pod}")"
    log "satellite ${sat_pod} config marker='${sat_marker}'"
    if [[ "${sat_marker}" == "${new_marker}" ]]; then
      pass "satellite synced new config marker '${new_marker}'"
      break
    fi
    if [[ "${sat_marker}" == "${old_marker}" ]]; then
      die "satellite ${sat_pod} still has old marker '${old_marker}' after cores rolled — cores may have republished stale config"
    fi
    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for satellite to sync marker '${new_marker}' (got '${sat_marker}')"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done

  # Leave SMOKE_CONFIG_MARKER at the new value so later debugging matches runtime.
  export SMOKE_CONFIG_MARKER="${new_marker}"
}

# Count rows for hostname in hostgroup on a pod's runtime_mysql_servers.
# Asserts presence/absence of a specific backend — status alone is not enough
# (a ghost host can sit SHUNNED after certs/connectivity fail).
backend_hg_count_on_pod() {
  local pod="$1"
  local hostname="$2"
  local hostgroup_id="$3"
  admin_query "${pod}" \
    "SELECT COUNT(*) FROM runtime_mysql_servers WHERE hostname='${hostname}' AND hostgroup_id=${hostgroup_id}" \
    2>/dev/null | tr -d '\r' | head -n1 || echo 0
}

running_satellite_pods() {
  kubectl -n proxysql get pods -l 'app=proxysql,component=satellite' \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
}

# Returns 0 when every Running pod of the given component has the expected
# COUNT for hostname@hostgroup. Logs each pod so the transcript shows ghosts.
assert_pods_backend_hg_count() {
  local component="$1"
  local hostname="$2"
  local hostgroup_id="$3"
  local expected_count="$4"
  local pod count
  local matched=0
  local total=0

  local pods
  if [[ "${component}" == "core" ]]; then
    pods="$(running_core_pods)"
  else
    pods="$(running_satellite_pods)"
  fi

  while IFS= read -r pod; do
    [[ -z "${pod}" ]] && continue
    total=$((total + 1))
    count="$(backend_hg_count_on_pod "${pod}" "${hostname}" "${hostgroup_id}")"
    log "${component} ${pod} runtime_mysql_servers hostname=${hostname} hg=${hostgroup_id} count=${count} (want ${expected_count})"
    if [[ "${count}" == "${expected_count}" ]]; then
      matched=$((matched + 1))
    fi
  done <<<"${pods}"

  [[ "${total}" -gt 0 ]] || die "no running ${component} pods while asserting ${hostname}@hg${hostgroup_id}"
  [[ "${matched}" -eq "${total}" ]] || return 1
  return 0
}

wait_pods_backend_hg_count() {
  local component="$1"
  local hostname="$2"
  local hostgroup_id="$3"
  local expected_count="$4"
  local deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))

  while true; do
    if assert_pods_backend_hg_count "${component}" "${hostname}" "${hostgroup_id}" "${expected_count}"; then
      return 0
    fi
    if [[ "${SECONDS}" -ge "${deadline}" ]]; then
      die "timed out after ${SMOKE_TIMEOUT_SECONDS}s waiting for all ${component}s to have ${hostname}@hg${hostgroup_id} count=${expected_count} (ghost host from FROM CONFIG merge without DELETE?)"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

# Shared mid-rollout membership churn used by both config-stickiness phases.
roll_cores_with_membership_churn() {
  local annotation_value="$1"

  kubectl -n proxysql annotate deployment/proxysql-core \
    "smoke.withpersona.com/config-marker=${annotation_value}" --overwrite

  step "rolling restart proxysql-core to pick up new ConfigMap"
  kubectl -n proxysql rollout restart deployment/proxysql-core

  sleep 8
  local victim
  victim="$(first_pod core || true)"
  if [[ -n "${victim}" ]]; then
    log "deleting core pod ${victim} mid-rollout to force membership LOAD handlers"
    kubectl -n proxysql delete pod "${victim}" --wait=false >/dev/null 2>&1 || true
  else
    log "warning: no core pod available mid-rollout to delete; continuing with rollout churn alone"
  fi

  step "waiting for core rollout to complete"
  kubectl -n proxysql rollout status deployment/proxysql-core --timeout="${SMOKE_TIMEOUT_SECONDS}s"
  kubectl -n proxysql scale deployment/proxysql-core --replicas="${SMOKE_CORE_REPLICAS}" >/dev/null
  kubectl -n proxysql rollout status deployment/proxysql-core --timeout="${SMOKE_TIMEOUT_SECONDS}s"
}

# Force the stack-0015 failure mode: remove an HG2 backend from the ConfigMap,
# roll cores while old (still advertising the host) and new coexist, and assert
# the removed host is gone from runtime_mysql_servers — not merely SHUNNED.
# Without DELETE FROM mysql_servers before FROM CONFIG, peer sync re-injects
# the host and the merge reload leaves it forever.
assert_backend_removal_survives_rolling_update() {
  if [[ "${SMOKE_SKIP_CONFIG_ROLLOUT}" == "1" ]]; then
    step "skipping backend-removal assertion (SMOKE_SKIP_CONFIG_ROLLOUT=1)"
    return
  fi

  step "assert removed backend stays gone after rolling update (no FROM CONFIG merge ghost)"
  if [[ "${SMOKE_CORE_REPLICAS}" -lt 2 ]]; then
    die "backend-removal smoke requires SMOKE_CORE_REPLICAS>=2 (got ${SMOKE_CORE_REPLICAS}); the ghost only lands when an old core coexists with a new one"
  fi

  local removed_host="${SMOKE_MYSQL_REPLICA_HOST}"
  local primary_host="${SMOKE_MYSQL_PRIMARY_HOST}"

  # Assert false first: replica must be present in HG2 before we remove it.
  wait_pods_backend_hg_count core "${removed_host}" 2 1
  pass "replica ${removed_host} present in HG2 on all cores before removal"

  log "rendering + applying core ConfigMap with HG2 replica omitted"
  export SMOKE_INCLUDE_MYSQL_REPLICA=0
  render_proxysql_config

  roll_cores_with_membership_churn "backend-removal-$(date +%s)"

  # Gone means count=0 in runtime — SHUNNED still counts as present and is a fail.
  wait_pods_backend_hg_count core "${removed_host}" 2 0
  pass "all cores dropped ${removed_host} from HG2 runtime_mysql_servers"

  wait_pods_backend_hg_count core "${primary_host}" 1 1
  pass "primary ${primary_host} still present in HG1 on all cores"

  step "assert satellites dropped removed backend from HG2"
  wait_pods_backend_hg_count satellite "${removed_host}" 2 0
  pass "all satellites dropped ${removed_host} from HG2 runtime_mysql_servers"
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
  assert_config_survives_rolling_update
  assert_backend_removal_survives_rolling_update
  pass "orbstack proxysql-agent smoke test"
}

main "$@"
