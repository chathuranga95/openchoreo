#!/usr/bin/env bash

# OpenChoreo Cluster Backup Script
# Backs up all planes (CP, DP, BP, OP) including persistent volumes

set -eo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-openchoreo}"
KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
BACKUP_TIMESTAMP=$(date +%Y-%m-%d-%H%M%S)
OUTPUT_DIR="${OUTPUT_DIR:-${SCRIPT_DIR}/backups/backup-${BACKUP_TIMESTAMP}}"
COMPRESS="${COMPRESS:-false}"
SKIP_VOLUMES="${SKIP_VOLUMES:-false}"
SKIP_SECRETS="${SKIP_SECRETS:-false}"

# Selective backup flags
BACKUP_CP="${BACKUP_CP:-true}"
BACKUP_DP="${BACKUP_DP:-true}"
BACKUP_BP="${BACKUP_BP:-true}"
BACKUP_OP="${BACKUP_OP:-true}"

# Namespaces
CP_NAMESPACE="openchoreo-control-plane"
DP_NAMESPACE="openchoreo-data-plane"
BP_NAMESPACE="openchoreo-build-plane"
OP_NAMESPACE="openchoreo-observability-plane"

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${RESET} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${RESET} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${RESET} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${RESET} $1"
}

log_step() {
    echo -e "\n${CYAN}${BOLD}==>${RESET} ${BOLD}$1${RESET}\n"
}

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Validate prerequisites
validate_prerequisites() {
    log_step "Validating prerequisites..."
    
    local missing_tools=()
    
    if ! command_exists kubectl; then
        missing_tools+=("kubectl")
    fi
    
    if ! command_exists helm; then
        missing_tools+=("helm")
    fi
    
    if [ ${#missing_tools[@]} -gt 0 ]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        exit 1
    fi
    
    # Check cluster access
    if ! kubectl --context="${KUBE_CONTEXT}" cluster-info >/dev/null 2>&1; then
        log_error "Cannot access cluster '${CLUSTER_NAME}' with context '${KUBE_CONTEXT}'"
        log_info "Available contexts:"
        kubectl config get-contexts
        exit 1
    fi
    
    log_success "Prerequisites validated"
}

# Create backup directory structure
create_backup_structure() {
    log_step "Creating backup directory structure..."
    
    mkdir -p "${OUTPUT_DIR}"/{control-plane,data-plane,build-plane,observability-plane,cluster-info}
    mkdir -p "${OUTPUT_DIR}"/{control-plane/secrets,build-plane/volumes,observability-plane/volumes}
    
    log_success "Backup directory created: ${OUTPUT_DIR}"
}

# Save backup metadata
save_metadata() {
    log_step "Saving backup metadata..."
    
    local metadata_file="${OUTPUT_DIR}/metadata.json"
    
    cat > "${metadata_file}" <<EOF
{
  "backup_timestamp": "${BACKUP_TIMESTAMP}",
  "cluster_name": "${CLUSTER_NAME}",
  "kubernetes_version": "$(kubectl --context="${KUBE_CONTEXT}" version -o json 2>/dev/null | grep -oP '"gitVersion": "\K[^"]+' | head -n1)",
  "backup_location": "${OUTPUT_DIR}",
  "planes_backed_up": {
    "control_plane": ${BACKUP_CP},
    "data_plane": ${BACKUP_DP},
    "build_plane": ${BACKUP_BP},
    "observability_plane": ${BACKUP_OP}
  },
  "volumes_backed_up": $([ "$SKIP_VOLUMES" = "false" ] && echo "true" || echo "false"),
  "secrets_backed_up": $([ "$SKIP_SECRETS" = "false" ] && echo "true" || echo "false")
}
EOF
    
    log_success "Metadata saved"
}

# Backup cluster information
backup_cluster_info() {
    log_step "Backing up cluster information..."
    
    local cluster_dir="${OUTPUT_DIR}/cluster-info"
    
    # Cluster info
    kubectl --context="${KUBE_CONTEXT}" cluster-info dump > "${cluster_dir}/cluster-info.yaml" 2>/dev/null || true
    
    # Node information
    kubectl --context="${KUBE_CONTEXT}" get nodes -o yaml > "${cluster_dir}/nodes.yaml"
    
    # Version information
    kubectl --context="${KUBE_CONTEXT}" version -o yaml > "${cluster_dir}/version-info.yaml" 2>/dev/null || true
    
    # Namespaces
    kubectl --context="${KUBE_CONTEXT}" get namespaces -o yaml > "${cluster_dir}/namespaces.yaml"
    
    log_success "Cluster information backed up"
}

# Backup CRDs
backup_crds() {
    log_step "Backing up Custom Resource Definitions..."
    
    local crds_file="${OUTPUT_DIR}/control-plane/crds.yaml"
    
    kubectl --context="${KUBE_CONTEXT}" get crds -o yaml > "${crds_file}"
    
    log_success "CRDs backed up"
}

# Backup Control Plane
backup_control_plane() {
    if [ "$BACKUP_CP" != "true" ]; then
        log_info "Skipping Control Plane backup"
        return 0
    fi
    
    log_step "Backing up Control Plane..."
    
    local cp_dir="${OUTPUT_DIR}/control-plane"
    
    # Check if namespace exists
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${CP_NAMESPACE}" >/dev/null 2>&1; then
        log_warning "Control Plane namespace '${CP_NAMESPACE}' not found, skipping"
        return 0
    fi
    
    # Backup all resources
    kubectl --context="${KUBE_CONTEXT}" get all,configmaps,secrets,ingress,serviceaccounts,roles,rolebindings \
        -n "${CP_NAMESPACE}" -o yaml > "${cp_dir}/resources.yaml" 2>/dev/null || true
    
    # Backup OpenChoreo CRDs resources
    log_info "Backing up OpenChoreo custom resources..."
    kubectl --context="${KUBE_CONTEXT}" get projects,organizations,environments \
        -A -o yaml > "${cp_dir}/openchoreo-resources.yaml" 2>/dev/null || true
    
    # Backup Helm releases
    helm --kube-context="${KUBE_CONTEXT}" list -n "${CP_NAMESPACE}" -o yaml > "${cp_dir}/helm-releases.yaml" 2>/dev/null || true
    
    # Backup persistent volume claims
    kubectl --context="${KUBE_CONTEXT}" get pvc -n "${CP_NAMESPACE}" -o yaml > "${cp_dir}/pvcs.yaml" 2>/dev/null || true
    
    # Backup secrets separately (if not skipped)
    if [ "$SKIP_SECRETS" != "true" ]; then
        kubectl --context="${KUBE_CONTEXT}" get secrets -n "${CP_NAMESPACE}" -o yaml > "${cp_dir}/secrets/secrets.yaml" 2>/dev/null || true
    fi
    
    log_success "Control Plane backed up"
}

# Backup Data Plane
backup_data_plane() {
    if [ "$BACKUP_DP" != "true" ]; then
        log_info "Skipping Data Plane backup"
        return 0
    fi
    
    log_step "Backing up Data Plane..."
    
    local dp_dir="${OUTPUT_DIR}/data-plane"
    
    # Check if namespace exists
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${DP_NAMESPACE}" >/dev/null 2>&1; then
        log_warning "Data Plane namespace '${DP_NAMESPACE}' not found, skipping"
        return 0
    fi
    
    # Backup all resources
    kubectl --context="${KUBE_CONTEXT}" get all,configmaps,secrets,serviceaccounts,roles,rolebindings \
        -n "${DP_NAMESPACE}" -o yaml > "${dp_dir}/resources.yaml" 2>/dev/null || true
    
    # Backup Gateway resources
    log_info "Backing up Gateway configurations..."
    kubectl --context="${KUBE_CONTEXT}" get gateways,httproutes,grpcroutes,tcproutes \
        -A -o yaml > "${dp_dir}/gateway-config.yaml" 2>/dev/null || true
    
    # Backup DataPlane resources
    kubectl --context="${KUBE_CONTEXT}" get dataplanes -A -o yaml > "${dp_dir}/dataplane-resources.yaml" 2>/dev/null || true
    
    # Backup Helm releases
    helm --kube-context="${KUBE_CONTEXT}" list -n "${DP_NAMESPACE}" -o yaml > "${dp_dir}/helm-releases.yaml" 2>/dev/null || true
    
    # Backup certificates
    kubectl --context="${KUBE_CONTEXT}" get certificates,certificaterequests \
        -n "${DP_NAMESPACE}" -o yaml > "${dp_dir}/certificates.yaml" 2>/dev/null || true
    
    log_success "Data Plane backed up"
}

# Backup Build Plane
backup_build_plane() {
    if [ "$BACKUP_BP" != "true" ]; then
        log_info "Skipping Build Plane backup"
        return 0
    fi
    
    log_step "Backing up Build Plane..."
    
    local bp_dir="${OUTPUT_DIR}/build-plane"
    
    # Check if namespace exists
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${BP_NAMESPACE}" >/dev/null 2>&1; then
        log_warning "Build Plane namespace '${BP_NAMESPACE}' not found, skipping"
        return 0
    fi
    
    # Backup all resources
    kubectl --context="${KUBE_CONTEXT}" get all,configmaps,secrets,serviceaccounts,roles,rolebindings \
        -n "${BP_NAMESPACE}" -o yaml > "${bp_dir}/resources.yaml" 2>/dev/null || true
    
    # Backup Argo Workflows
    log_info "Backing up Argo Workflows..."
    kubectl --context="${KUBE_CONTEXT}" get workflows,workflowtemplates,cronworkflows,clusterworkflowtemplates \
        -n "${BP_NAMESPACE}" -o yaml > "${bp_dir}/workflows.yaml" 2>/dev/null || true
    
    # Backup BuildPlane resources
    kubectl --context="${KUBE_CONTEXT}" get buildplanes -A -o yaml > "${bp_dir}/buildplane-resources.yaml" 2>/dev/null || true
    
    # Backup Helm releases
    helm --kube-context="${KUBE_CONTEXT}" list -n "${BP_NAMESPACE}" -o yaml > "${bp_dir}/helm-releases.yaml" 2>/dev/null || true
    
    # Backup persistent volumes
    if [ "$SKIP_VOLUMES" != "true" ]; then
        backup_build_plane_volumes
    fi
    
    log_success "Build Plane backed up"
}

# Backup Build Plane persistent volumes
backup_build_plane_volumes() {
    log_info "Backing up Build Plane persistent volumes..."
    
    local bp_volumes_dir="${OUTPUT_DIR}/build-plane/volumes"
    
    # Get registry PVCs
    local registry_pvcs=$(kubectl --context="${KUBE_CONTEXT}" get pvc -n "${BP_NAMESPACE}" \
        -l app.kubernetes.io/name=docker-registry -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    if [ -n "$registry_pvcs" ]; then
        log_info "Backing up container registry data..."
        for pvc in $registry_pvcs; do
            backup_pvc_data "${BP_NAMESPACE}" "$pvc" "${bp_volumes_dir}/registry-${pvc}.tar.gz"
        done
    fi
    
    # Get Argo archive PVCs
    local argo_pvcs=$(kubectl --context="${KUBE_CONTEXT}" get pvc -n "${BP_NAMESPACE}" \
        -l app.kubernetes.io/name=argo -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    if [ -n "$argo_pvcs" ]; then
        log_info "Backing up Argo Workflows archive..."
        for pvc in $argo_pvcs; do
            backup_pvc_data "${BP_NAMESPACE}" "$pvc" "${bp_volumes_dir}/argo-${pvc}.tar.gz"
        done
    fi
}

# Backup Observability Plane
backup_observability_plane() {
    if [ "$BACKUP_OP" != "true" ]; then
        log_info "Skipping Observability Plane backup"
        return 0
    fi
    
    log_step "Backing up Observability Plane..."
    
    local op_dir="${OUTPUT_DIR}/observability-plane"
    
    # Check if namespace exists
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${OP_NAMESPACE}" >/dev/null 2>&1; then
        log_warning "Observability Plane namespace '${OP_NAMESPACE}' not found, skipping"
        return 0
    fi
    
    # Backup all resources
    kubectl --context="${KUBE_CONTEXT}" get all,configmaps,secrets,serviceaccounts,roles,rolebindings \
        -n "${OP_NAMESPACE}" -o yaml > "${op_dir}/resources.yaml" 2>/dev/null || true
    
    # Backup Prometheus resources
    log_info "Backing up Prometheus configurations..."
    kubectl --context="${KUBE_CONTEXT}" get prometheuses,alertmanagers,servicemonitors,podmonitors,prometheusrules \
        -n "${OP_NAMESPACE}" -o yaml > "${op_dir}/prometheus-config.yaml" 2>/dev/null || true
    
    # Backup OpenSearch Dashboard configurations
    log_info "Backing up OpenSearch Dashboard configurations..."
    kubectl --context="${KUBE_CONTEXT}" get configmaps -n "${OP_NAMESPACE}" \
        -l app.kubernetes.io/name=opensearch-dashboards -o yaml > "${op_dir}/dashboards-config.yaml" 2>/dev/null || true
    
    # Backup Helm releases
    helm --kube-context="${KUBE_CONTEXT}" list -n "${OP_NAMESPACE}" -o yaml > "${op_dir}/helm-releases.yaml" 2>/dev/null || true
    
    # Backup persistent volumes
    if [ "$SKIP_VOLUMES" != "true" ]; then
        backup_observability_volumes
    fi
    
    log_success "Observability Plane backed up"
}

# Backup Observability Plane persistent volumes
backup_observability_volumes() {
    log_info "Backing up Observability Plane persistent volumes..."
    
    local op_volumes_dir="${OUTPUT_DIR}/observability-plane/volumes"
    
    # Backup OpenSearch data
    log_info "Backing up OpenSearch data..."
    backup_opensearch_data
    
    # Backup Prometheus data
    log_info "Backing up Prometheus metrics data..."
    backup_prometheus_data
}

# Backup OpenSearch data using snapshot API or PVC copy
backup_opensearch_data() {
    local op_volumes_dir="${OUTPUT_DIR}/observability-plane/volumes"
    
    # Try to use OpenSearch snapshot API first
    local opensearch_pod=$(kubectl --context="${KUBE_CONTEXT}" get pods -n "${OP_NAMESPACE}" \
        -l app.kubernetes.io/name=opensearch -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [ -n "$opensearch_pod" ]; then
        log_info "Creating OpenSearch snapshot via API..."
        
        # Create snapshot repository (using shared filesystem)
        kubectl --context="${KUBE_CONTEXT}" exec -n "${OP_NAMESPACE}" "$opensearch_pod" -- \
            curl -X PUT "localhost:9200/_snapshot/backup_repo" -H 'Content-Type: application/json' -d'
            {
              "type": "fs",
              "settings": {
                "location": "/tmp/opensearch-backup"
              }
            }' 2>/dev/null || log_warning "Failed to create snapshot repository"
        
        # Create snapshot
        local snapshot_name="backup-${BACKUP_TIMESTAMP}"
        kubectl --context="${KUBE_CONTEXT}" exec -n "${OP_NAMESPACE}" "$opensearch_pod" -- \
            curl -X PUT "localhost:9200/_snapshot/backup_repo/${snapshot_name}?wait_for_completion=true" \
            2>/dev/null || log_warning "Failed to create snapshot"
        
        # Export indices metadata
        kubectl --context="${KUBE_CONTEXT}" exec -n "${OP_NAMESPACE}" "$opensearch_pod" -- \
            curl -X GET "localhost:9200/_all" > "${op_volumes_dir}/opensearch-indices.json" 2>/dev/null || true
    fi
    
    # Fallback: Backup OpenSearch PVCs
    local opensearch_pvcs=$(kubectl --context="${KUBE_CONTEXT}" get pvc -n "${OP_NAMESPACE}" \
        -l app.kubernetes.io/name=opensearch -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    if [ -n "$opensearch_pvcs" ]; then
        log_info "Backing up OpenSearch PVCs..."
        for pvc in $opensearch_pvcs; do
            backup_pvc_data "${OP_NAMESPACE}" "$pvc" "${op_volumes_dir}/opensearch-${pvc}.tar.gz"
        done
    else
        log_warning "No OpenSearch PVCs found"
    fi
}

# Backup Prometheus data
backup_prometheus_data() {
    local op_volumes_dir="${OUTPUT_DIR}/observability-plane/volumes"
    
    # Backup Prometheus PVCs
    local prometheus_pvcs=$(kubectl --context="${KUBE_CONTEXT}" get pvc -n "${OP_NAMESPACE}" \
        -l app.kubernetes.io/name=prometheus -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    if [ -n "$prometheus_pvcs" ]; then
        log_info "Backing up Prometheus PVCs..."
        for pvc in $prometheus_pvcs; do
            backup_pvc_data "${OP_NAMESPACE}" "$pvc" "${op_volumes_dir}/prometheus-${pvc}.tar.gz"
        done
    else
        log_warning "No Prometheus PVCs found"
    fi
    
    # Backup Prometheus configuration
    kubectl --context="${KUBE_CONTEXT}" get configmaps -n "${OP_NAMESPACE}" \
        -l app.kubernetes.io/name=prometheus -o yaml > "${op_volumes_dir}/prometheus-config.yaml" 2>/dev/null || true
}

# Backup PVC data using a temporary pod
backup_pvc_data() {
    local namespace="$1"
    local pvc_name="$2"
    local output_file="$3"
    
    log_info "Backing up PVC: ${namespace}/${pvc_name}"
    
    # Create a temporary pod to access the PVC
    local backup_pod="backup-pod-$(date +%s)"
    
    cat <<EOF | kubectl --context="${KUBE_CONTEXT}" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${backup_pod}
  namespace: ${namespace}
spec:
  containers:
  - name: backup
    image: busybox:latest
    command: ['sh', '-c', 'sleep 3600']
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: ${pvc_name}
  restartPolicy: Never
EOF
    
    # Wait for pod to be ready
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready \
        pod/${backup_pod} -n "${namespace}" --timeout=300s >/dev/null 2>&1 || {
        log_warning "Backup pod failed to start for PVC ${pvc_name}"
        kubectl --context="${KUBE_CONTEXT}" delete pod ${backup_pod} -n "${namespace}" --force --grace-period=0 >/dev/null 2>&1 || true
        return 1
    }
    
    # Create tar archive of the data
    kubectl --context="${KUBE_CONTEXT}" exec -n "${namespace}" ${backup_pod} -- \
        tar czf - -C /data . > "${output_file}" 2>/dev/null || {
        log_warning "Failed to create archive for PVC ${pvc_name}"
    }
    
    # Clean up backup pod
    kubectl --context="${KUBE_CONTEXT}" delete pod ${backup_pod} -n "${namespace}" --force --grace-period=0 >/dev/null 2>&1 || true
    
    if [ -f "${output_file}" ]; then
        local size=$(du -h "${output_file}" | cut -f1)
        log_success "PVC ${pvc_name} backed up (${size})"
    fi
}

# Compress backup
compress_backup() {
    if [ "$COMPRESS" != "true" ]; then
        return 0
    fi
    
    log_step "Compressing backup..."
    
    local archive_file="${OUTPUT_DIR}.tar.gz"
    
    tar czf "${archive_file}" -C "$(dirname "${OUTPUT_DIR}")" "$(basename "${OUTPUT_DIR}")" 2>/dev/null || {
        log_error "Failed to compress backup"
        return 1
    }
    
    log_success "Backup compressed: ${archive_file}"
    
    # Optionally remove uncompressed directory
    read -p "Remove uncompressed backup directory? (y/N): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        rm -rf "${OUTPUT_DIR}"
        log_info "Uncompressed backup removed"
    fi
}

# Display backup summary
show_summary() {
    log_step "Backup Summary"
    
    echo -e "${BOLD}Backup completed successfully!${RESET}\n"
    echo -e "${CYAN}Backup Location:${RESET} ${OUTPUT_DIR}"
    echo -e "${CYAN}Timestamp:${RESET} ${BACKUP_TIMESTAMP}"
    
    if [ -d "${OUTPUT_DIR}" ]; then
        echo -e "\n${CYAN}Backup Size:${RESET}"
        du -sh "${OUTPUT_DIR}"
        
        echo -e "\n${CYAN}Backup Contents:${RESET}"
        tree -L 2 "${OUTPUT_DIR}" 2>/dev/null || find "${OUTPUT_DIR}" -maxdepth 2 -type d
    fi
    
    echo -e "\n${GREEN}${BOLD}Backup completed successfully!${RESET}"
    echo -e "\n${CYAN}Next Steps:${RESET}"
    echo -e "  1. Verify backup: ${YELLOW}./verify-backup.sh ${OUTPUT_DIR}${RESET}"
    echo -e "  2. Store backup in safe location"
    echo -e "  3. Test restore procedure: ${YELLOW}../restore/restore-cluster.sh${RESET}"
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Backup OpenChoreo cluster data including all planes and persistent volumes.

Options:
  --output-dir DIR              Custom backup directory (default: ./backups/backup-TIMESTAMP)
  --cluster-name NAME           Cluster name (default: openchoreo)
  --control-plane-only          Backup only Control Plane
  --data-plane-only            Backup only Data Plane
  --build-plane-only           Backup only Build Plane
  --observability-only         Backup only Observability Plane
  --skip-volumes               Skip persistent volume backups
  --skip-secrets               Skip secrets backup
  --compress                   Compress backup after creation
  --help, -h                   Show this help message

Environment Variables:
  CLUSTER_NAME                 Cluster name (default: openchoreo)
  OUTPUT_DIR                   Custom backup directory
  SKIP_VOLUMES=true           Skip volume backups
  COMPRESS=true               Enable compression

Examples:
  # Full backup
  $0
  
  # Backup to custom location
  $0 --output-dir /mnt/backups/openchoreo
  
  # Backup only observability plane with volumes
  $0 --observability-only
  
  # Backup all planes, skip volumes
  $0 --skip-volumes
  
  # Backup and compress
  $0 --compress

EOF
}

# Parse command line arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --output-dir)
                OUTPUT_DIR="$2"
                shift 2
                ;;
            --cluster-name)
                CLUSTER_NAME="$2"
                KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
                shift 2
                ;;
            --control-plane-only)
                BACKUP_CP=true
                BACKUP_DP=false
                BACKUP_BP=false
                BACKUP_OP=false
                shift
                ;;
            --data-plane-only)
                BACKUP_CP=false
                BACKUP_DP=true
                BACKUP_BP=false
                BACKUP_OP=false
                shift
                ;;
            --build-plane-only)
                BACKUP_CP=false
                BACKUP_DP=false
                BACKUP_BP=true
                BACKUP_OP=false
                shift
                ;;
            --observability-only)
                BACKUP_CP=false
                BACKUP_DP=false
                BACKUP_BP=false
                BACKUP_OP=true
                shift
                ;;
            --skip-volumes)
                SKIP_VOLUMES=true
                shift
                ;;
            --skip-secrets)
                SKIP_SECRETS=true
                shift
                ;;
            --compress)
                COMPRESS=true
                shift
                ;;
            --help|-h)
                show_usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                echo "Use --help for usage information"
                exit 1
                ;;
        esac
    done
}

# Main backup flow
main() {
    echo -e "${CYAN}${BOLD}"
    cat << 'EOF'
   ___                   ____ _                            
  / _ \ _ __   ___ _ __ / ___| |__   ___  _ __ ___  ___  
 | | | | '_ \ / _ \ '_ \| |   | '_ \ / _ \| '__/ _ \/ _ \ 
 | |_| | |_) |  __/ | | | |___| | | | (_) | | |  __/ (_) |
  \___/| .__/ \___|_| |_|\____|_| |_|\___/|_|  \___|\___/ 
       |_|                                                  
EOF
    echo -e "${RESET}"
    echo -e "${BOLD}Cluster Backup Script${RESET}\n"
    
    parse_args "$@"
    
    # Display configuration
    log_info "Backup Configuration:"
    log_info "  Cluster: ${CLUSTER_NAME}"
    log_info "  Context: ${KUBE_CONTEXT}"
    log_info "  Output: ${OUTPUT_DIR}"
    log_info "  Control Plane: ${BACKUP_CP}"
    log_info "  Data Plane: ${BACKUP_DP}"
    log_info "  Build Plane: ${BACKUP_BP}"
    log_info "  Observability Plane: ${BACKUP_OP}"
    log_info "  Backup Volumes: $([ "$SKIP_VOLUMES" = "false" ] && echo "Yes" || echo "No")"
    log_info "  Backup Secrets: $([ "$SKIP_SECRETS" = "false" ] && echo "Yes" || echo "No")"
    echo ""
    
    # Execute backup
    validate_prerequisites
    create_backup_structure
    save_metadata
    backup_cluster_info
    backup_crds
    backup_control_plane
    backup_data_plane
    backup_build_plane
    backup_observability_plane
    compress_backup
    show_summary
}

# Run main function
main "$@"

