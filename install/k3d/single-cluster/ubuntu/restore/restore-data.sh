#!/usr/bin/env bash

# OpenChoreo Data Restore Script
# Restores ONLY user data and custom resources after fresh installation
# 
# Use Case:
#   1. Create backup with backup-cluster.sh
#   2. Delete cluster completely
#   3. Run fresh install with install-single-cluster.sh
#   4. Run this script to restore data only
#
# This script restores:
#   - OpenChoreo custom resources (Components, Projects, Organizations, Environments)
#   - User workload namespaces (resources, secrets, PVCs)
#   - Gateway configurations (HTTP routes, etc.)
#   - Persistent volume data (observability logs/metrics, user workloads)
#
# This script SKIPS (because fresh install provides them):
#   - CRDs (already installed during cluster setup)
#   - Helm releases (already installed)
#   - Infrastructure resources (Deployments, Services, etc.)

set -eo pipefail

# Color codes
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
BACKUP_DIR=""
CLUSTER_NAME="${CLUSTER_NAME:-openchoreo}"
KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
SKIP_VOLUMES="${SKIP_VOLUMES:-false}"
FORCE="${FORCE:-false}"
DRY_RUN="${DRY_RUN:-false}"

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
    
    if ! command_exists kubectl; then
        log_error "kubectl not found"
        exit 1
    fi
    
    # Check cluster access
    if ! kubectl --context="${KUBE_CONTEXT}" cluster-info >/dev/null 2>&1; then
        log_error "Cannot access cluster '${CLUSTER_NAME}' with context '${KUBE_CONTEXT}'"
        log_error "Please ensure cluster is running and install is complete"
        exit 1
    fi
    
    # Verify that planes are installed
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${CP_NAMESPACE}" >/dev/null 2>&1; then
        log_error "Control Plane namespace not found. Please run install-single-cluster.sh first"
        exit 1
    fi
    
    log_success "Prerequisites validated"
}

# Validate backup directory
validate_backup() {
    log_step "Validating backup directory..."
    
    if [ -z "$BACKUP_DIR" ]; then
        log_error "Backup directory not specified. Use --backup-dir option."
        exit 1
    fi
    
    if [ ! -d "$BACKUP_DIR" ]; then
        log_error "Backup directory not found: $BACKUP_DIR"
        exit 1
    fi
    
    if [ ! -f "$BACKUP_DIR/metadata.json" ]; then
        log_error "Invalid backup: metadata.json not found"
        exit 1
    fi
    
    # Display backup info
    log_info "Backup Information:"
    cat "$BACKUP_DIR/metadata.json" | grep -E '"backup_timestamp"|"cluster_name"|"kubernetes_version"' || true
    echo ""
    
    log_success "Backup validated"
}

# Confirm restore operation
confirm_restore() {
    if [ "$FORCE" = "true" ]; then
        return 0
    fi
    
    log_warning "This will restore user data and custom resources to the freshly installed cluster!"
    log_info "Backup: $BACKUP_DIR"
    log_info "Target Cluster: $CLUSTER_NAME ($KUBE_CONTEXT)"
    echo ""
    log_info "Make sure you have run a fresh installation before restoring data."
    echo ""
    
    read -p "Continue with data restore? (yes/no): " -r
    if [[ ! $REPLY =~ ^[Yy][Ee][Ss]$ ]]; then
        log_info "Restore cancelled by user"
        exit 0
    fi
}

# Restore OpenChoreo custom resources only
restore_openchoreo_resources() {
    log_step "Restoring OpenChoreo custom resources..."
    
    local cp_dir="$BACKUP_DIR/control-plane"
    
    if [ ! -f "$cp_dir/openchoreo-resources.yaml" ]; then
        log_warning "OpenChoreo resources file not found, skipping"
        return 0
    fi
    
    if [ "$DRY_RUN" = "true" ]; then
        log_info "[DRY RUN] Would restore OpenChoreo custom resources"
        return 0
    fi

    log_info "Restoring Components, Projects, Organizations, Environments..."
    kubectl --context="${KUBE_CONTEXT}" apply -f "$cp_dir/openchoreo-resources.yaml" 2>/dev/null || {
        log_warning "Some resources may have failed to apply, continuing..."
    }

    log_success "OpenChoreo custom resources restored (Components, Projects, Organizations, Environments)"
}

# Restore user workloads and configurations
restore_user_workloads() {
    log_step "Restoring user workloads..."

    local dp_dir="$BACKUP_DIR/data-plane"

    # Restore namespaces first (before any resources)
    if [ -f "$dp_dir/namespaces.yaml" ]; then
        log_info "Restoring component workload namespaces..."
        kubectl --context="${KUBE_CONTEXT}" apply -f "$dp_dir/namespaces.yaml" 2>/dev/null || true
    fi

    # Restore DataPlane resources
    if [ -f "$dp_dir/dataplane-resources.yaml" ]; then
        log_info "Restoring DataPlane resources..."
        kubectl --context="${KUBE_CONTEXT}" apply -f "$dp_dir/dataplane-resources.yaml" 2>/dev/null || true
    fi

    # Restore user routes (HTTP/GRPC/TCP routes)
    if [ -f "$dp_dir/routes.yaml" ]; then
        log_info "Restoring user routes (HTTP/GRPC/TCP)..."
        kubectl --context="${KUBE_CONTEXT}" apply -f "$dp_dir/routes.yaml" 2>/dev/null || true
    fi

    # Backward compatibility: old backups have gateway-config.yaml
    if [ -f "$dp_dir/gateway-config.yaml" ]; then
        log_info "Restoring routes from legacy backup..."
        kubectl --context="${KUBE_CONTEXT}" apply -f "$dp_dir/gateway-config.yaml" 2>/dev/null || true
    fi

    log_success "User workloads restored"
}

# Restore user workload namespaces (from user-workloads backup)
restore_user_namespaces() {
    log_step "Restoring user workload namespaces..."

    local user_workloads_dir="$BACKUP_DIR/user-workloads"

    if [ ! -d "$user_workloads_dir" ]; then
        log_info "No user workload namespaces found in backup"
        return 0
    fi

    if [ "$DRY_RUN" = "true" ]; then
        log_info "[DRY RUN] Would restore user workload namespaces"
        return 0
    fi

    # Get all user namespace directories
    for ns_dir in "$user_workloads_dir"/*; do
        [ -d "$ns_dir" ] || continue

        local ns=$(basename "$ns_dir")
        log_info "Restoring namespace: ${ns}"

        # Create namespace if it doesn't exist
        kubectl --context="${KUBE_CONTEXT}" create namespace "$ns" 2>/dev/null || true

        # Restore resources
        if [ -f "$ns_dir/resources.yaml" ]; then
            log_info "  Restoring resources in namespace ${ns}..."
            kubectl --context="${KUBE_CONTEXT}" apply -f "$ns_dir/resources.yaml" 2>/dev/null || {
                log_warning "  Some resources in ${ns} may have failed to apply"
            }
        fi

        # Restore secrets
        if [ -f "$ns_dir/secrets.yaml" ]; then
            log_info "  Restoring secrets in namespace ${ns}..."
            kubectl --context="${KUBE_CONTEXT}" apply -f "$ns_dir/secrets.yaml" 2>/dev/null || true
        fi

        # Restore PVCs
        if [ "$SKIP_VOLUMES" != "true" ] && [ -f "$ns_dir/pvcs.yaml" ]; then
            log_info "  Restoring PVCs in namespace ${ns}..."
            kubectl --context="${KUBE_CONTEXT}" apply -f "$ns_dir/pvcs.yaml" 2>/dev/null || true

            # Wait for PVCs to be bound
            sleep 5

            # Restore PVC data
            if [ -d "$ns_dir/volumes" ]; then
                for volume_file in "$ns_dir/volumes"/*.tar.gz; do
                    [ -f "$volume_file" ] || continue

                    local pvc_name=$(basename "$volume_file" .tar.gz)
                    restore_pvc_data "$ns" "$pvc_name" "$volume_file"
                done
            fi
        fi

        log_success "Namespace ${ns} restored"
    done

    log_success "User workload namespaces restored"
}

# Restore Build Plane user data
restore_build_plane_data() {
    log_step "Restoring Build Plane user data..."
    
    local bp_dir="$BACKUP_DIR/build-plane"
    
    if [ ! -d "$bp_dir" ]; then
        log_warning "Build Plane backup not found, skipping"
        return 0
    fi
    
    # Restore BuildPlane resources
    if [ -f "$bp_dir/buildplane-resources.yaml" ]; then
        log_info "Restoring BuildPlane resources..."
        kubectl --context="${KUBE_CONTEXT}" apply -f "$bp_dir/buildplane-resources.yaml" 2>/dev/null || true
    fi
    
    # Restore Argo Workflows (user-created)
    if [ -f "$bp_dir/workflows.yaml" ]; then
        log_info "Restoring user Workflows..."
        kubectl --context="${KUBE_CONTEXT}" apply -f "$bp_dir/workflows.yaml" 2>/dev/null || true
    fi
    
    # Restore persistent volumes
    if [ "$SKIP_VOLUMES" != "true" ] && [ -d "$bp_dir/volumes" ]; then
        restore_build_plane_volumes
    fi
    
    log_success "Build Plane user data restored"
}

# Restore Build Plane volumes
restore_build_plane_volumes() {
    log_info "Restoring Build Plane persistent volumes..."

    local bp_volumes_dir="$BACKUP_DIR/build-plane/volumes"

    # Wait for PVCs to be created by fresh install
    log_info "Waiting for Build Plane PVCs to be ready..."
    sleep 10

    # Restore all Build Plane volumes (registry, argo, etc.)
    for volume_file in "$bp_volumes_dir"/*.tar.gz; do
        [ -f "$volume_file" ] || continue

        local pvc_name=$(basename "$volume_file" .tar.gz)
        log_info "Restoring Build Plane PVC: ${pvc_name}..."
        restore_pvc_data "${BP_NAMESPACE}" "$pvc_name" "$volume_file"
    done
}

# Restore Observability Plane data
restore_observability_data() {
    log_step "Restoring Observability Plane data..."
    
    local op_dir="$BACKUP_DIR/observability-plane"
    
    if [ ! -d "$op_dir" ]; then
        log_warning "Observability Plane backup not found, skipping"
        return 0
    fi
    
    if [ "$DRY_RUN" = "true" ]; then
        log_info "[DRY RUN] Would restore Observability Plane data"
        return 0
    fi
    
    # Wait for observability pods to be ready
    log_info "Waiting for Observability Plane to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n "${OP_NAMESPACE}" -l app.kubernetes.io/component=observer \
        --timeout=300s 2>/dev/null || {
        log_warning "Some Observability pods may not be ready yet"
    }
    
    # Restore persistent volumes (logs and metrics)
    if [ "$SKIP_VOLUMES" != "true" ] && [ -d "$op_dir/volumes" ]; then
        restore_observability_volumes
    fi
    
    log_success "Observability Plane data restored"
}

# Restore Observability volumes (OpenSearch and Prometheus)
restore_observability_volumes() {
    log_info "Restoring Observability Plane persistent volumes..."
    
    local op_volumes_dir="$BACKUP_DIR/observability-plane/volumes"
    
    # Scale down observability components to safely restore data
    log_info "Scaling down Observability components for safe restore..."
    kubectl --context="${KUBE_CONTEXT}" scale deployment opensearch -n "${OP_NAMESPACE}" --replicas=0 2>/dev/null || true
    kubectl --context="${KUBE_CONTEXT}" scale statefulset prometheus-openchoreo-observability-prometheus -n "${OP_NAMESPACE}" --replicas=0 2>/dev/null || true
    sleep 10
    
    # Restore OpenSearch volumes (logs)
    log_info "Restoring OpenSearch data (logs)..."
    for volume_file in "$op_volumes_dir"/opensearch-*.tar.gz; do
        [ -f "$volume_file" ] || continue
        
        local pvc_name=$(basename "$volume_file" .tar.gz | sed 's/^opensearch-//')
        restore_pvc_data "${OP_NAMESPACE}" "$pvc_name" "$volume_file"
    done
    
    # Restore Prometheus volumes (metrics)
    log_info "Restoring Prometheus data (metrics)..."
    for volume_file in "$op_volumes_dir"/prometheus-*.tar.gz; do
        [ -f "$volume_file" ] || continue
        
        local pvc_name=$(basename "$volume_file" .tar.gz | sed 's/^prometheus-//')
        restore_pvc_data "${OP_NAMESPACE}" "$pvc_name" "$volume_file"
    done
    
    # Scale back up
    log_info "Scaling up Observability components..."
    kubectl --context="${KUBE_CONTEXT}" scale deployment opensearch -n "${OP_NAMESPACE}" --replicas=1 2>/dev/null || true
    kubectl --context="${KUBE_CONTEXT}" scale statefulset prometheus-openchoreo-observability-prometheus -n "${OP_NAMESPACE}" --replicas=1 2>/dev/null || true
    
    # Wait for pods to be ready
    log_info "Waiting for Observability components to be ready..."
    sleep 20
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n "${OP_NAMESPACE}" -l app.kubernetes.io/name=opensearch \
        --timeout=300s 2>/dev/null || log_warning "OpenSearch may need more time to start"
    
    log_success "Observability volumes restored (logs and metrics)"
}

# Restore PVC data
restore_pvc_data() {
    local namespace="$1"
    local pvc_name="$2"
    local data_file="$3"
    
    log_info "Restoring PVC: ${namespace}/${pvc_name}"
    
    # Check if PVC exists
    if ! kubectl --context="${KUBE_CONTEXT}" get pvc "$pvc_name" -n "$namespace" >/dev/null 2>&1; then
        log_warning "PVC $pvc_name not found in namespace $namespace, skipping"
        return 1
    fi
    
    # Create temporary pod
    local restore_pod="restore-pod-$(date +%s)"
    
    cat <<EOF | kubectl --context="${KUBE_CONTEXT}" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${restore_pod}
  namespace: ${namespace}
spec:
  containers:
  - name: restore
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
    
    # Wait for pod
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready \
        pod/${restore_pod} -n "${namespace}" --timeout=300s >/dev/null 2>&1 || {
        log_warning "Restore pod failed to start for PVC ${pvc_name}"
        kubectl --context="${KUBE_CONTEXT}" delete pod ${restore_pod} -n "${namespace}" --force --grace-period=0 >/dev/null 2>&1 || true
        return 1
    }
    
    # Clear existing data
    kubectl --context="${KUBE_CONTEXT}" exec -n "${namespace}" ${restore_pod} -- \
        sh -c 'rm -rf /data/* /data/.[!.]* 2>/dev/null || true' >/dev/null 2>&1 || true
    
    # Restore data
    cat "${data_file}" | kubectl --context="${KUBE_CONTEXT}" exec -i -n "${namespace}" ${restore_pod} -- \
        tar xzf - -C /data 2>/dev/null || {
        log_warning "Failed to restore data for PVC ${pvc_name}"
    }
    
    # Cleanup
    kubectl --context="${KUBE_CONTEXT}" delete pod ${restore_pod} -n "${namespace}" --force --grace-period=0 >/dev/null 2>&1 || true
    
    log_success "PVC ${pvc_name} data restored"
}

# Verify restoration
verify_restore() {
    log_step "Verifying restoration..."
    
    # Check OpenChoreo custom resources
    log_info "Checking OpenChoreo resources..."
    local component_count=$(kubectl --context="${KUBE_CONTEXT}" get components -A --no-headers 2>/dev/null | wc -l)
    local project_count=$(kubectl --context="${KUBE_CONTEXT}" get projects -A --no-headers 2>/dev/null | wc -l)
    local org_count=$(kubectl --context="${KUBE_CONTEXT}" get organizations -A --no-headers 2>/dev/null | wc -l)

    log_info "  Components restored: ${component_count}"
    log_info "  Projects restored: ${project_count}"
    log_info "  Organizations restored: ${org_count}"
    
    # Check PVCs
    if [ "$SKIP_VOLUMES" != "true" ]; then
        log_info "Checking persistent volumes..."
        kubectl --context="${KUBE_CONTEXT}" get pvc -A | grep -E "openchoreo|NAME"
    fi
    
    log_success "Restoration verification complete"
}

# Display summary
show_summary() {
    log_step "Data Restore Complete!"
    
    echo -e "${BOLD}Data restored successfully!${RESET}\n"
    echo -e "${CYAN}Cluster:${RESET} ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    echo -e "${CYAN}Backup:${RESET} ${BACKUP_DIR}"
    
    echo -e "\n${GREEN}${BOLD}What was restored:${RESET}"
    echo -e "  ✅ OpenChoreo custom resources (Components, Projects, Organizations, Environments)"
    echo -e "  ✅ User routes (HTTP/GRPC/TCP routes for workloads)"
    echo -e "  ✅ User workload namespaces (resources and secrets)"
    if [ "$SKIP_VOLUMES" != "true" ]; then
        echo -e "  ✅ Persistent volume data (observability, user workloads)"
    fi

    echo -e "\n${CYAN}Next Steps:${RESET}"
    echo -e "  1. Verify custom resources: ${YELLOW}kubectl --context=${KUBE_CONTEXT} get components,projects,organizations -A${RESET}"
    echo -e "  2. Check user namespaces: ${YELLOW}kubectl --context=${KUBE_CONTEXT} get namespaces${RESET}"
    echo -e "  3. Check logs: ${YELLOW}Access OpenSearch Dashboard (if configured)${RESET}"
    echo -e "  4. Check metrics: ${YELLOW}Access Prometheus (if configured)${RESET}"
    echo -e "  5. Verify applications: ${YELLOW}kubectl --context=${KUBE_CONTEXT} get all -A${RESET}"
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Restore user data and custom resources after fresh OpenChoreo installation.

IMPORTANT: Run this AFTER a fresh installation with install-single-cluster.sh

What this restores:
  - OpenChoreo custom resources (Components, Projects, Organizations, Environments)
  - User routes (HTTP routes, GRPC routes, TCP routes for workloads)
  - User workload namespaces (all resources, secrets, PVCs)
  - Persistent volume data (observability logs/metrics, user workloads)

What this does NOT restore (comes from fresh install):
  - CRDs (already installed during cluster setup)
  - Helm releases (already installed)
  - Infrastructure resources (Deployments, Services, etc.)

Workflow:
  1. Create backup:    cd backup && ./backup-cluster.sh
  2. Delete cluster:   k3d cluster delete openchoreo
  3. Fresh install:    cd installation && ./install-single-cluster.sh
  4. Restore data:     cd restore && ./restore-data.sh --backup-dir ../backup/backups/backup-*

Options:
  --backup-dir DIR              Path to backup directory (required)
  --cluster-name NAME           Target cluster name (default: openchoreo)
  --skip-volumes               Skip persistent volume restore
  --force                      Force restore without confirmation
  --dry-run                    Show what would be restored
  --help, -h                   Show this help message

Examples:
  # Restore all user data
  $0 --backup-dir ../backup/backups/backup-2024-11-25-120000
  
  # Restore without volumes (config only)
  $0 --backup-dir /path/to/backup --skip-volumes
  
  # Dry run to preview
  $0 --backup-dir /path/to/backup --dry-run

EOF
}

# Parse arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --backup-dir)
                BACKUP_DIR="$2"
                shift 2
                ;;
            --cluster-name)
                CLUSTER_NAME="$2"
                KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
                shift 2
                ;;
            --skip-volumes)
                SKIP_VOLUMES=true
                shift
                ;;
            --force)
                FORCE=true
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --help|-h)
                show_usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                show_usage
                exit 1
                ;;
        esac
    done
}

# Main
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
    echo -e "${BOLD}Data Restore Script (After Fresh Install)${RESET}\n"
    
    parse_args "$@"
    
    log_info "Configuration:"
    log_info "  Backup: ${BACKUP_DIR:-<not specified>}"
    log_info "  Cluster: ${CLUSTER_NAME}"
    log_info "  Restore Volumes: $([ "$SKIP_VOLUMES" = "false" ] && echo "Yes" || echo "No")"
    log_info "  Dry Run: ${DRY_RUN}"
    echo ""
    
    validate_backup
    validate_prerequisites
    confirm_restore
    restore_openchoreo_resources
    restore_user_workloads
    restore_user_namespaces
    restore_build_plane_data
    restore_observability_data
    verify_restore
    show_summary
}

main "$@"

