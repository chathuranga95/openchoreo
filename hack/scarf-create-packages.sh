#!/usr/bin/env bash
# Copyright 2026 The OpenChoreo Authors
# SPDX-License-Identifier: Apache-2.0

# Creates the Scarf Gateway docker packages that front OpenChoreo's GHCR images
# and OCI Helm charts, attaches the custom domain, and prints the DNS records
# needed to verify it. Also creates the website tracking pixels and prints their
# embed snippets. Idempotent: existing packages and pixels are reused.
#
# Usage:
#   SCARF_API_TOKEN=... hack/scarf-create-packages.sh            # dry run
#   SCARF_API_TOKEN=... hack/scarf-create-packages.sh --apply    # create
#
# API reference: https://api-docs.scarf.sh/v2.html

set -euo pipefail

SCARF_API="${SCARF_API:-https://api.scarf.sh}"
SCARF_OWNER="${SCARF_OWNER:-openchoreo}"
SCARF_DOMAIN="${SCARF_DOMAIN:-cr.openchoreo.dev}"
SCARF_PIXEL_DOMAIN="${SCARF_PIXEL_DOMAIN:-static.openchoreo.dev}"
BACKEND_REGISTRY="${BACKEND_REGISTRY:-ghcr.io}"
WEBSITE="https://openchoreo.dev"

# name|importance|short description
PACKAGES=(
  # Helm charts (OCI)
  "openchoreo/helm-charts/openchoreo-control-plane|high|OpenChoreo control plane Helm chart"
  "openchoreo/helm-charts/openchoreo-data-plane|high|OpenChoreo data plane Helm chart"
  "openchoreo/helm-charts/openchoreo-workflow-plane|high|OpenChoreo workflow plane Helm chart"
  "openchoreo/helm-charts/openchoreo-observability-plane|high|OpenChoreo observability plane Helm chart"
  "openchoreo/helm-charts/observability-logs-opensearch|mid|OpenChoreo OpenSearch logs module Helm chart"
  "openchoreo/helm-charts/observability-tracing-opensearch|mid|OpenChoreo OpenSearch tracing module Helm chart"
  "openchoreo/helm-charts/observability-metrics-prometheus|mid|OpenChoreo Prometheus metrics module Helm chart"
  "openchoreo/helm-charts/observability-events-otel-collector|mid|OpenChoreo OTel Collector events module Helm chart"
  # Container images
  "openchoreo/controller|high|OpenChoreo controller manager"
  "openchoreo/openchoreo-api|high|OpenChoreo API server"
  "openchoreo/openchoreo-ui|high|OpenChoreo developer portal (Backstage)"
  "openchoreo/cluster-gateway|high|OpenChoreo cluster gateway"
  "openchoreo/cluster-agent|high|OpenChoreo cluster agent"
  "openchoreo/observer|mid|OpenChoreo observability API"
  "openchoreo/event-forwarder|mid|OpenChoreo event forwarder"
  "openchoreo/remote-agent|mid|OpenChoreo remote-connect agent"
  "openchoreo/remote-agent-router|mid|OpenChoreo remote-connect SNI router"
  "openchoreo/portal-assistant|low|OpenChoreo portal AI assistant"
  "openchoreo/sre-agent|low|OpenChoreo SRE AI agent"
  "openchoreo/finops-agent|low|OpenChoreo FinOps AI agent"
  "openchoreo/quick-start|mid|OpenChoreo quick-start container"
  # Community module Helm charts (OCI), openchoreo/community-modules
  "openchoreo/helm-charts/observability-logs-openobserve|mid|OpenChoreo OpenObserve logs module Helm chart"
  "openchoreo/helm-charts/observability-tracing-openobserve|mid|OpenChoreo OpenObserve tracing module Helm chart"
  "openchoreo/helm-charts/observability-logs-aws-cloudwatch|low|OpenChoreo AWS CloudWatch logs module Helm chart"
  "openchoreo/helm-charts/observability-metrics-aws-cloudwatch|low|OpenChoreo AWS CloudWatch metrics module Helm chart"
  "openchoreo/helm-charts/observability-tracing-aws-xray|low|OpenChoreo AWS X-Ray tracing module Helm chart"
  "openchoreo/helm-charts/observability-logs-azure-loganalytics|low|OpenChoreo Azure Log Analytics logs module Helm chart"
  "openchoreo/helm-charts/observability-metrics-azure-monitor|low|OpenChoreo Azure Monitor metrics module Helm chart"
  "openchoreo/helm-charts/observability-tracing-azure-appinsights|low|OpenChoreo Azure App Insights tracing module Helm chart"
  "openchoreo/helm-charts/observability-logs-gcp-cloudlogging|low|OpenChoreo GCP Cloud Logging logs module Helm chart"
  "openchoreo/helm-charts/observability-metrics-gcp-cloudmonitoring|low|OpenChoreo GCP Cloud Monitoring metrics module Helm chart"
  "openchoreo/helm-charts/observability-tracing-gcp-cloudtrace|low|OpenChoreo GCP Cloud Trace tracing module Helm chart"
  "openchoreo/helm-charts/observability-logs-moesif|low|OpenChoreo Moesif logs module Helm chart"
  "openchoreo/helm-charts/finops-opencost|low|OpenChoreo OpenCost FinOps module Helm chart"
  "openchoreo/helm-charts/agent-sandbox|low|OpenChoreo agent sandbox module Helm chart"
  "openchoreo/helm-charts/amp-platform-resources|low|OpenChoreo WSO2 Agent Manager platform resources Helm chart"
  # Community module container images
  "openchoreo/observability-logs-opensearch-adapter|mid|OpenChoreo OpenSearch logs adapter"
  "openchoreo/observability-logs-opensearch-setup|mid|OpenChoreo OpenSearch logs setup job"
  "openchoreo/observability-tracing-opensearch-adapter|mid|OpenChoreo OpenSearch tracing adapter"
  "openchoreo/observability-tracing-opensearch-setup|mid|OpenChoreo OpenSearch tracing setup job"
  "openchoreo/observability-metrics-prometheus-adapter|mid|OpenChoreo Prometheus metrics adapter"
  "openchoreo/observability-events-otel-collector|mid|OpenChoreo OTel Collector events collector"
  "openchoreo/observability-logs-openobserve-adapter|mid|OpenChoreo OpenObserve logs adapter"
  "openchoreo/observability-logs-openobserve-setup|mid|OpenChoreo OpenObserve logs setup job"
  "openchoreo/observability-tracing-openobserve-adapter|mid|OpenChoreo OpenObserve tracing adapter"
  "openchoreo/observability-logs-aws-cloudwatch-adapter|low|OpenChoreo AWS CloudWatch logs adapter"
  "openchoreo/observability-logs-aws-cloudwatch-setup|low|OpenChoreo AWS CloudWatch logs setup job"
  "openchoreo/observability-metrics-aws-cloudwatch-adapter|low|OpenChoreo AWS CloudWatch metrics adapter"
  "openchoreo/observability-tracing-aws-xray-adapter|low|OpenChoreo AWS X-Ray tracing adapter"
  "openchoreo/observability-logs-azure-loganalytics-adapter|low|OpenChoreo Azure Log Analytics logs adapter"
  "openchoreo/observability-metrics-azure-monitor-adapter|low|OpenChoreo Azure Monitor metrics adapter"
  "openchoreo/observability-tracing-azure-appinsights-adapter|low|OpenChoreo Azure App Insights tracing adapter"
  "openchoreo/observability-logs-gcp-cloudlogging-adapter|low|OpenChoreo GCP Cloud Logging logs adapter"
  "openchoreo/observability-metrics-gcp-cloudmonitoring-adapter|low|OpenChoreo GCP Cloud Monitoring metrics adapter"
  "openchoreo/observability-tracing-gcp-cloudtrace-adapter|low|OpenChoreo GCP Cloud Trace tracing adapter"
  "openchoreo/finops-opencost-adapter|low|OpenChoreo OpenCost FinOps adapter"
)

# name|importance|page the pixel is embedded in
PIXELS=(
  "openchoreo-github-readme|mid|https://github.com/openchoreo/openchoreo"
  "openchoreo-website-home|high|https://openchoreo.dev/"
  "openchoreo-website-ecosystem|mid|https://openchoreo.dev/ecosystem/"
  "openchoreo-docs-landing-page|high|https://openchoreo.dev/docs/"
  "openchoreo-docs-quick-start-guide|high|https://openchoreo.dev/docs/getting-started/quick-start-guide/"
  "openchoreo-docs-architecture|mid|https://openchoreo.dev/docs/overview/architecture/"
  "openchoreo-docs-try-it-out-k3d|high|https://openchoreo.dev/docs/getting-started/try-it-out/on-k3d-locally/"
  "openchoreo-docs-try-it-out-your-environment|high|https://openchoreo.dev/docs/getting-started/try-it-out/on-your-environment/"
  "openchoreo-docs-platform-engineer-guide|mid|https://openchoreo.dev/docs/category/platform-engineer-guide/"
  "openchoreo-docs-concepts|mid|https://openchoreo.dev/docs/category/concepts/"
  "openchoreo-website-blog|low|https://openchoreo.dev/blog/"
  "openchoreo-website-community|low|https://openchoreo.dev/community/"
  "openchoreo-website-explore-developer-portal|mid|https://openchoreo.dev/explore/backstage-powered-developer-portal/"
  "openchoreo-website-explore-observability|mid|https://openchoreo.dev/explore/observability/"
  "openchoreo-website-explore-agentic-platform|mid|https://openchoreo.dev/explore/agentic-developer-platform/"
)

APPLY=false

usage() {
  sed -n '5,13p' "$0" | sed 's/^# \{0,1\}//'
  cat <<EOF

Environment:
  SCARF_API_TOKEN   API token from Scarf account settings (required)
  SCARF_OWNER       Scarf organization (default: ${SCARF_OWNER})
  SCARF_DOMAIN      Custom domain to attach (default: ${SCARF_DOMAIN}); empty uses the Scarf domain
  SCARF_PIXEL_DOMAIN  Custom domain for tracking pixels (default: ${SCARF_PIXEL_DOMAIN}); empty uses static.scarf.sh
  BACKEND_REGISTRY  Registry the gateway forwards to (default: ${BACKEND_REGISTRY})
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=true ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
  shift
done

for tool in curl jq; do
  command -v "$tool" >/dev/null || { echo "Missing required tool: $tool" >&2; exit 1; }
done
[[ -n "${SCARF_API_TOKEN:-}" ]] || { echo "SCARF_API_TOKEN is not set" >&2; exit 1; }

# api METHOD PATH [JSON_BODY] -> sets API_STATUS and API_BODY
API_STATUS=""
API_BODY=""
api() {
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -H "Authorization: Bearer ${SCARF_API_TOKEN}" -H "Accept: application/json")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
  [[ "$path" == http* ]] || path="${SCARF_API}${path}"
  out=$(curl "${args[@]}" -w $'\n%{http_code}' "$path")
  API_STATUS="${out##*$'\n'}"
  API_BODY="${out%$'\n'*}"
}

# Prints "<name>\t<id>" for every docker package the owner has.
list_docker_packages() {
  local url="/v2/packages/${SCARF_OWNER}?type=docker&per_page=100" body
  while [[ -n "$url" ]]; do
    api GET "$url"
    body="$API_BODY"
    [[ "$API_STATUS" == 200 ]] || { echo "Listing packages failed (HTTP ${API_STATUS}): ${body}" >&2; exit 1; }
    jq -r '.results[] | "\(.name)\t\(.id)"' <<<"$body"
    url=$(jq -r '._links.next // empty' <<<"$body")
  done
}

ensure_domain() {
  local name="$1" id="$2" body
  [[ -n "$SCARF_DOMAIN" ]] || return 0
  api GET "/v2/packages/${SCARF_OWNER}/${id}/domains"
  body="$API_BODY"
  [[ "$API_STATUS" == 200 ]] || { echo "  ! could not read domains for ${name} (HTTP ${API_STATUS}): ${body}" >&2; return 1; }
  if jq -e --arg d "$SCARF_DOMAIN" 'any(.[]; .name == $d)' <<<"$body" >/dev/null; then
    echo "  = ${SCARF_DOMAIN} already attached"
    return 0
  fi
  if ! $APPLY; then
    echo "  + would attach ${SCARF_DOMAIN}"
    return 0
  fi
  api POST "/v2/packages/${SCARF_OWNER}/${id}/domains" "$(jq -n --arg d "$SCARF_DOMAIN" '{name: $d}')"
  body="$API_BODY"
  if [[ "$API_STATUS" == 201 ]]; then
    echo "  + attached ${SCARF_DOMAIN}"
  else
    echo "  ! attaching ${SCARF_DOMAIN} failed (HTTP ${API_STATUS}): ${body}" >&2
    return 1
  fi
}

create_package() {
  local name="$1" importance="$2" desc="$3" payload body
  payload=$(jq -n \
    --arg name "$name" --arg registry "$BACKEND_REGISTRY" --arg domain "$SCARF_DOMAIN" \
    --arg importance "$importance" --arg desc "$desc" --arg website "$WEBSITE" \
    '{type: "docker", name: $name, backend_registry: $registry, importance: $importance,
      short_description: $desc, website: $website}
     + (if $domain == "" then {} else {domain: $domain} end)')
  if ! $APPLY; then
    echo "  + would create: ${payload}"
    return 0
  fi
  api POST "/v2/packages/${SCARF_OWNER}" "$payload"
  body="$API_BODY"
  if [[ "$API_STATUS" == 201 ]]; then
    echo "  + created (id $(jq -r .id <<<"$body"))"
  else
    echo "  ! create failed (HTTP ${API_STATUS}): ${body}" >&2
    return 1
  fi
}

# Prints "<name>\t<id>\t<importance>\t<short_description>" for every tracking pixel the owner has.
list_tracking_pixels() {
  local url="/v2/tracking-pixels/${SCARF_OWNER}?per_page=100" body
  while [[ -n "$url" ]]; do
    api GET "$url"
    body="$API_BODY"
    [[ "$API_STATUS" == 200 ]] || { echo "Listing tracking pixels failed (HTTP ${API_STATUS}): ${body}" >&2; exit 1; }
    jq -r '.results[] | "\(.name)\t\(.id)\t\(.importance // "")\t\(.short_description // "")"' <<<"$body"
    url=$(jq -r '._links.next // empty' <<<"$body")
  done
}

pixel_payload() {
  jq -n --arg name "$1" --arg importance "$2" --arg desc "$3" \
    '{name: $name, importance: $importance, short_description: $desc}'
}

create_pixel() {
  local name="$1" importance="$2" desc="$3" payload body
  payload=$(pixel_payload "$name" "$importance" "$desc")
  if ! $APPLY; then
    echo "  + would create: ${payload}"
    return 0
  fi
  api POST "/v2/tracking-pixels/${SCARF_OWNER}" "$payload"
  body="$API_BODY"
  if [[ "$API_STATUS" == 201 ]]; then
    PIXEL_ID=$(jq -r .id <<<"$body")
    echo "  + created (id ${PIXEL_ID})"
  else
    echo "  ! create failed (HTTP ${API_STATUS}): ${body}" >&2
    return 1
  fi
}

update_pixel() {
  local id="$1" name="$2" importance="$3" desc="$4" payload body
  payload=$(pixel_payload "$name" "$importance" "$desc")
  if ! $APPLY; then
    echo "  ~ would update: ${payload}"
    return 0
  fi
  api PUT "/v2/tracking-pixels/${SCARF_OWNER}/${id}" "$payload"
  body="$API_BODY"
  if [[ "$API_STATUS" == 200 ]]; then
    echo "  ~ updated importance/description"
  else
    echo "  ! update failed (HTTP ${API_STATUS}): ${body}" >&2
    return 1
  fi
}

ensure_pixel_domain() {
  local name="$1" id="$2" body
  [[ -n "$SCARF_PIXEL_DOMAIN" ]] || return 0
  api GET "/v2/tracking-pixels/${SCARF_OWNER}/${id}/domains"
  body="$API_BODY"
  [[ "$API_STATUS" == 200 ]] || { echo "  ! could not read domains for ${name} (HTTP ${API_STATUS}): ${body}" >&2; return 1; }
  if jq -e --arg d "$SCARF_PIXEL_DOMAIN" 'any(.[]; .name == $d)' <<<"$body" >/dev/null; then
    echo "  = ${SCARF_PIXEL_DOMAIN} already attached"
    return 0
  fi
  if ! $APPLY; then
    echo "  + would attach ${SCARF_PIXEL_DOMAIN}"
    return 0
  fi
  api POST "/v2/tracking-pixels/${SCARF_OWNER}/${id}/domains" "$(jq -n --arg d "$SCARF_PIXEL_DOMAIN" '{name: $d}')"
  body="$API_BODY"
  if [[ "$API_STATUS" == 200 || "$API_STATUS" == 201 ]]; then
    echo "  + attached ${SCARF_PIXEL_DOMAIN}"
  else
    echo "  ! attaching ${SCARF_PIXEL_DOMAIN} failed (HTTP ${API_STATUS}): ${body}" >&2
    return 1
  fi
}

print_domain_status() {
  local body
  [[ -n "$SCARF_DOMAIN" ]] || return 0
  api GET "/v2/domains/${SCARF_OWNER}/${SCARF_DOMAIN}/status"
  body="$API_BODY"
  if [[ "$API_STATUS" != 200 ]]; then
    echo "Domain status unavailable yet (HTTP ${API_STATUS}). Re-run after the domain is attached."
    return 0
  fi
  echo
  echo "Domain ${SCARF_DOMAIN}:"
  jq -r '"  CNAME status:        \(.cname_status // "unknown")",
         "  Verification status: \(.verification_status // "unknown")",
         "  Certificate status:  \(.certificate_status // "unknown")"' <<<"$body"
  echo
  echo "DNS records to add:"
  echo "  CNAME  ${SCARF_DOMAIN}  ->  gateway.scarf.sh"
  jq -r '"  TXT    \(.verification_challenge_hostname // "<pending>")  ->  \(.verification_challenge_txt_record // "<pending>")"' <<<"$body"
}

$APPLY || echo "Dry run: no changes will be made. Pass --apply to create packages."
echo "Owner: ${SCARF_OWNER}  Domain: ${SCARF_DOMAIN:-<scarf default>}  Backend: ${BACKEND_REGISTRY}"
echo

existing=$(list_docker_packages)
failures=0

for entry in "${PACKAGES[@]}"; do
  IFS='|' read -r name importance desc <<<"$entry"
  echo "${name}"
  id=$(awk -F'\t' -v n="$name" '$1 == n {print $2; exit}' <<<"$existing")
  if [[ -n "$id" ]]; then
    echo "  = exists (id ${id})"
    ensure_domain "$name" "$id" || failures=$((failures + 1))
  else
    create_package "$name" "$importance" "$desc" || failures=$((failures + 1))
  fi
done

print_domain_status

echo
echo "Tracking pixels (domain: ${SCARF_PIXEL_DOMAIN:-static.scarf.sh})"
echo

existing_pixels=$(list_tracking_pixels)
snippets=()

for entry in "${PIXELS[@]}"; do
  IFS='|' read -r name importance page <<<"$entry"
  desc="Page views for ${page}"
  echo "${name}"
  IFS=$'\t' read -r _ id cur_importance cur_desc < <(awk -F'\t' -v n="$name" '$1 == n {print; exit}' <<<"$existing_pixels") || true
  PIXEL_ID=""
  if [[ -n "${id:-}" ]]; then
    PIXEL_ID="$id"
    echo "  = exists (id ${id})"
    if [[ "$cur_importance" != "$importance" || "$cur_desc" != "$desc" ]]; then
      update_pixel "$id" "$name" "$importance" "$desc" || failures=$((failures + 1))
    fi
  else
    create_pixel "$name" "$importance" "$desc" || { failures=$((failures + 1)); continue; }
  fi
  if [[ -n "$PIXEL_ID" ]]; then
    ensure_pixel_domain "$name" "$PIXEL_ID" || failures=$((failures + 1))
    snippets+=("${page}"$'\t'"<img referrerpolicy=\"no-referrer-when-downgrade\" src=\"https://${SCARF_PIXEL_DOMAIN:-static.scarf.sh}/a.png?x-pxid=${PIXEL_ID}\" />")
  fi
  id=""
done

if [[ ${#snippets[@]} -gt 0 ]]; then
  echo
  echo "Pixel snippets:"
  printf '  %s\n' "${snippets[@]}"
fi

if [[ $failures -gt 0 ]]; then
  echo
  echo "${failures} operation(s) failed." >&2
  exit 1
fi
