# Cluster health pre-flight (local k3d)

The macOS sleep/wake cycle pauses the Colima VM and triggers a cluster-wide
probe storm. Pods (controller-manager, kgateway, backstage, postgres) flap;
while the storm is in flight, the OC mutating webhook returns 502 and BFF
component-create / OC API calls fail with `INTERNAL_ERROR`.

**Run before any cluster-touching action** (`kubectl`, `dev-cycle.sh`, BFF
calls, OC API calls, dispatching a task, running tests). Re-run **Detect**
until clean before proceeding.

## Detect

```bash
# (1) Storm signatures in the last hour
kubectl get events -A --sort-by=.lastTimestamp \
  | grep -E "context deadline exceeded|NodeNotReady" | tail -20

# (2) Unhealthy pods anywhere
kubectl get pods -A | grep -vE "Running|Completed|^NAMESPACE"

# (3) OC controller-manager has a fresh leader?
kubectl get lease -n openchoreo-control-plane 43500532.openchoreo.dev \
  -o jsonpath='holder={.spec.holderIdentity} renewed={.spec.renewTime}{"\n"}'

# (4) Host-side GitHub webhook relay alive? (Local dev only — bridges
#     smee.io → BFF. Without it, pull_request.ready_for_review / merged
#     and push events never reach the projector and tasks get stuck
#     in `in_progress` / `merged`.)
pgrep -f 'webhook-relay\.sh|smee-client' >/dev/null && echo "relay: up" || echo "relay: DOWN"
lsof -nP -iTCP:18080 -sTCP:LISTEN 2>/dev/null | awk 'NR==2{print "port-forward: up ("$1" pid "$2")"}' \
  | grep -q . || echo "port-forward 18080: DOWN"

# (5) OpenBao seeded? (runs in dev/inmem — every pod restart wipes the
#     K8s auth role + secret/apps/* data, leaving ESO unable to log in.
#     Symptom downstream: coding-agent pods stuck in
#     CreateContainerConfigError, agent never streams, build/agent
#     ExternalSecrets stay SecretSyncedError.)
kubectl get clustersecretstore default \
  -o jsonpath='store={.status.conditions[-1].reason}{"\n"}'
```

**Unhealthy if any of:**
- (1) returns events from < 10 min ago.
- (2) lists any non-Running pod in `openchoreo-control-plane`,
  `openchoreo-data-plane`, `openchoreo-workflow-plane`, `wso2cloud`,
  `cert-manager`, or `flux-system`.
- (3) reports empty `holder=` or a `renewed=` timestamp older than ~1 min.
- (4) reports `relay: DOWN` or `port-forward 18080: DOWN`. The supervisor
  in `webhook-relay.sh` only restarts its children; if the parent shell
  was closed or the laptop slept long enough to drop the smee TCP
  connection, the whole relay is gone and webhook-driven task transitions
  silently stop working.
- (5) reports anything other than `store=Valid` (typically
  `InvalidProviderConfig` after an OpenBao pod restart).

## Recover

```bash
# (1) Wait 60s — the storm usually self-settles.
sleep 60 && kubectl get pods -A | grep -vE "Running|Completed|^NAMESPACE"

# (2) If still bad, bounce the affected pods (controller-manager + kgateway
#     + backstage are the usual offenders; add others as detected in step 1).
kubectl -n openchoreo-control-plane delete pod \
  -l 'app in (controller-manager,kgateway)' --wait=false
kubectl -n openchoreo-control-plane delete pod \
  -l app.kubernetes.io/name=backstage --wait=false

# (3) Verify apiserver + OC API are serving.
kubectl get --raw='/readyz' && echo
kubectl get --raw='/apis/openchoreo.dev/v1alpha1/components?limit=1' \
  >/dev/null && echo "OC api ok"

# (4) If the relay is down, restart it in the background. Use nohup so it
#     survives the parent shell exit; --pidfile guards against double-start.
#     For any task already stuck because its event was lost: redeliver from
#     the smee.io channel page (URL is GITHUB_WEBHOOK_DELIVERY_URL in
#     deployments-v2/.env) or from the GitHub repo's
#     Settings → Webhooks → Recent Deliveries panel.
rm -f deployments-v2/.webhook-relay.pid  # clear stale pid if process is gone
nohup bash deployments-v2/scripts/webhook-relay.sh \
  >>deployments-v2/.webhook-relay.log 2>&1 &
disown
sleep 3 && pgrep -f 'webhook-relay\.sh' >/dev/null && echo "relay: up"

# (5) Re-seed OpenBao. Idempotent. Re-binds the K8s auth role and
#     re-puts secret/apps/{anthropic,github-webhook,bff-task-signing-key}
#     from values in deployments-v2/.env + keys/.
( set -a; source deployments-v2/.env; set +a
  PEM_B64=$(base64 < deployments-v2/keys/task-signing.pem | tr -d '\n')
  kubectl exec -i -n openbao openbao-0 -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=root \
    ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
    GITHUB_WEBHOOK_DELIVERY_URL="$GITHUB_WEBHOOK_DELIVERY_URL" \
    GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
    PEM_B64="$PEM_B64" sh -s <<'BAO'
set -e
bao write auth/kubernetes/role/openchoreo-secret-reader-role \
  bound_service_account_names="default,external-secrets" \
  bound_service_account_namespaces="dp*,external-secrets,openchoreo-ci-default,workflows-*" \
  policies=openchoreo-secret-reader-policy ttl=20m >/dev/null
bao kv put secret/apps/anthropic      key="$ANTHROPIC_API_KEY" >/dev/null
bao kv put secret/apps/github-webhook delivery_url="$GITHUB_WEBHOOK_DELIVERY_URL" secret="$GITHUB_WEBHOOK_SECRET" >/dev/null
printf %s "$PEM_B64" | base64 -d > /tmp/pem
bao kv put secret/apps/bff-task-signing-key pem=@/tmp/pem >/dev/null
rm -f /tmp/pem
BAO
  kubectl annotate clustersecretstore default force-sync="$(date +%s)" --overwrite >/dev/null )
# Re-check: should print store=Valid within a few seconds.
sleep 5 && kubectl get clustersecretstore default \
  -o jsonpath='store={.status.conditions[-1].reason}{"\n"}'
```

## Why this happens (so future-you can decide whether to fix it upstream)

Root cause: the OC chart's controller-manager / openchoreo-api / cluster-gateway
/ backstage / kgateway Deployments hardcode probes without `timeoutSeconds`
or `failureThreshold`, so k8s defaults to 1 s timeout / 3 failures. On macOS
sleep/wake the Colima VM unfreezes with all timers firing at once against a
still-warming apiserver — every probe in the cluster fails simultaneously,
leases time out (`renewDeadline=10s` default), and the controller-manager
exits with `leader election lost`. The proper upstream fix is parameterising
those probes + leader-election durations in the OC chart values; until then
this runbook is the workaround.

OpenBao (step 5) is a different root cause: `Storage Type: inmem` (dev mode)
means every pod restart loses the K8s auth role + the `secret/apps/*` data
that `setup.sh` and the (since-deleted) Flux seed Job populated. Same
symptom-pattern, different fix: re-seed instead of bouncing pods. The
proper upstream fix is folding both halves of the seed into the OpenBao
HelmRelease's `postStart` block (matching the agent-manager pattern in
`/Users/wso2/repos/agent-manager/deployments/single-cluster/values-openbao.yaml`).
