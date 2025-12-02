#!/usr/bin/env bash

# OpenChoreo Velero Restore Script
# Restores OpenChoreo from a Velero backup

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
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../../.." && pwd)"

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-openchoreo}"
KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
BACKUP_NAME="${BACKUP_NAME:-}"
RESTORE_NAME="${RESTORE_NAME:-}"
VELERO_NAMESPACE="${VELERO_NAMESPACE:-velero}"

# Backup root directory (can be overridden via environment variable)
if [ -z "${BACKUP_ROOT}" ]; then
    BACKUP_ROOT="$(dirname "${REPO_ROOT}")/openchoreo-backups"
fi

# Restore options
RESTORE_VOLUMES="${RESTORE_VOLUMES:-true}"
WAIT_FOR_COMPLETION="${WAIT_FOR_COMPLETION:-true}"
DRY_RUN="${DRY_RUN:-false}"
FORCE="${FORCE:-false}"

# Namespace mapping (for testing restore to different namespaces)
NAMESPACE_MAPPINGS="${NAMESPACE_MAPPINGS:-}"

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

# Velero uses --kubecontext flag (not --context)
# This function is kept for reference but we'll use --kubecontext flag directly

# Validate prerequisites
validate_prerequisites() {
    log_step "Validating prerequisites..."
    
    local missing_tools=()
    
    if ! command_exists velero; then
        missing_tools+=("velero")
    fi
    
    if ! command_exists kubectl; then
        missing_tools+=("kubectl")
    fi
    
    if [ ${#missing_tools[@]} -gt 0 ]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        log_info "Please run ../backup/velero_install.sh first"
        exit 1
    fi
    
    # Check cluster access
    if ! kubectl --context="${KUBE_CONTEXT}" cluster-info >/dev/null 2>&1; then
        log_error "Cannot access cluster '${CLUSTER_NAME}' with context '${KUBE_CONTEXT}'"
        log_info "Available contexts:"
        kubectl config get-contexts
        exit 1
    fi
    
    # Check Velero is installed
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        log_error "Velero is not installed in cluster"
        log_info "Please run ../backup/velero_install.sh first"
        exit 1
    fi
    
    # Verify this is a fresh OpenChoreo installation
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace openchoreo-control-plane >/dev/null 2>&1; then
        log_error "OpenChoreo is not installed in this cluster"
        log_info "Please run a fresh installation first:"
        log_info "  cd ../installation && ./install-single-cluster.sh"
        exit 1
    fi
    
    log_success "Prerequisites validated"
}

# Validate backup exists
validate_backup() {
    log_step "Validating backup..."
    
    if [ -z "$BACKUP_NAME" ]; then
        log_error "No backup specified"
        log_info "Available backups:"
        velero backup get --kubecontext="${KUBE_CONTEXT}"
        exit 1
    fi
    
    # Check backup exists and get status
    local backup_info=$(velero backup get "${BACKUP_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null)
    
    if [ -z "$backup_info" ]; then
        log_error "Backup '${BACKUP_NAME}' not found"
        log_info "Available backups:"
        velero backup get --kubecontext="${KUBE_CONTEXT}"
        exit 1
    fi
    
    # Extract status from backup get output (format: NAME STATUS CREATED EXPIRES STORAGE LOCATION SELECTOR)
    local status=$(echo "$backup_info" | tail -n +2 | grep "${BACKUP_NAME}" | awk '{print $2}' | tr -d '[:space:]')
    
    # If status is empty, try backup describe as fallback
    if [ -z "$status" ]; then
        local describe_output=$(velero backup describe "${BACKUP_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null)
        status=$(echo "$describe_output" | grep -E "^Phase:" | awk '{print $2}' | tr -d '[:space:]')
    fi
    
    # Normalize status (handle case variations)
    status=$(echo "$status" | tr '[:lower:]' '[:upper:]')
    
    if [ "$status" != "COMPLETED" ]; then
        log_error "Backup '${BACKUP_NAME}' is not in 'Completed' state (status: ${status})"
        log_info "Backup details:"
        velero backup describe "${BACKUP_NAME}" --kubecontext="${KUBE_CONTEXT}" | head -20
        exit 1
    fi
    
    # Display backup info
    log_info "Backup Information:"
    velero backup describe "${BACKUP_NAME}" --kubecontext="${KUBE_CONTEXT}" | head -20
    
    log_success "Backup validated"
}

# Verify MinIO publicUrl configuration
verify_minio_publicurl() {
    log_step "Verifying MinIO publicUrl configuration..."
    
    # Check if MinIO service exists
    if ! kubectl --context="${KUBE_CONTEXT}" get svc minio -n "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        log_warning "MinIO service not found, skipping publicUrl verification"
        return 0
    fi
    
    # Check if Ingress exists (should already exist from installation)
    if ! kubectl --context="${KUBE_CONTEXT}" get ingress minio -n "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        log_warning "MinIO Ingress not found - Velero installation may be incomplete"
        log_info "Run: cd ../backup && ./velero_install_local.sh --fix-publicurl"
        return 0
    fi
    
    # Check current publicUrl
    local current_publicurl=$(kubectl --context="${KUBE_CONTEXT}" get backupstoragelocation default -n "${VELERO_NAMESPACE}" -o jsonpath='{.spec.config.publicUrl}' 2>/dev/null || echo "")
    
    # Verify publicUrl is using the ingress host
    local expected_publicurl="http://minio.velero.localhost:8080"
    if echo "$current_publicurl" | grep -q "\.svc" || [ "$current_publicurl" != "$expected_publicurl" ]; then
        log_warning "MinIO publicUrl is not configured correctly (current: ${current_publicurl})"
        log_info "Fixing publicUrl to use Ingress: ${expected_publicurl}..."
        
        # Update backup location publicUrl
        kubectl --context="${KUBE_CONTEXT}" patch backupstoragelocation default \
            -n "${VELERO_NAMESPACE}" \
            --type merge \
            -p "{\"spec\":{\"config\":{\"publicUrl\":\"${expected_publicurl}\"}}}" || {
            log_warning "Failed to update publicUrl, but continuing..."
            return 0
        }
        
        log_success "MinIO publicUrl fixed to: ${expected_publicurl}"
    else
        log_info "MinIO publicUrl is correctly configured: ${current_publicurl}"
    fi
}

# Import backup to cluster
import_backup() {
    log_step "Importing backup to k3d node..."
    
    local backup_dir="${BACKUP_ROOT}/local/backups/${BACKUP_NAME}"
    local k3d_node="k3d-${CLUSTER_NAME}-server-0"
    
    # Debug: Show paths being used
    log_info "REPO_ROOT: ${REPO_ROOT}"
    log_info "BACKUP_ROOT: ${BACKUP_ROOT}"
    log_info "Backup directory: ${backup_dir}"
    log_info "K3D node: ${k3d_node}"
    
    # Check if backup exists on host
    if [ ! -d "$backup_dir" ]; then
        log_error "Backup directory not found: ${BACKUP_NAME}"
        log_error "Expected path: $backup_dir"
        log_info "Checking if backup root exists: ${BACKUP_ROOT}"
        if [ -d "$BACKUP_ROOT" ]; then
            log_info "Backup root exists. Available backups:"
            ls -1 "${BACKUP_ROOT}/local/backups/" 2>/dev/null || echo "  (none found in local/backups/)"
            # Also check if backups are in a different location
            log_info "Searching for backup in alternative locations..."
            find "${BACKUP_ROOT}" -type d -name "${BACKUP_NAME}" 2>/dev/null | head -5 || true
        else
            log_error "Backup root directory does not exist: ${BACKUP_ROOT}"
            log_info "You can override BACKUP_ROOT via environment variable:"
            log_info "  export BACKUP_ROOT=/path/to/openchoreo-backups"
            log_info "Or ensure backups are located at: ${BACKUP_ROOT}/local/backups/"
        fi
        exit 1
    fi
    
    log_info "Backup found on host: ${backup_dir}"
    log_info "Copying to k3d node: ${k3d_node}"
    
    # Copy backup to k3d node
    docker cp "${BACKUP_ROOT}/local" "${k3d_node}:${BACKUP_ROOT}/" || {
        log_error "Failed to copy backup to k3d node"
        exit 1
    }
    
    # Verify backup is accessible in k3d node
    docker exec "${k3d_node}" ls "${BACKUP_ROOT}/local/backups/${BACKUP_NAME}" >/dev/null 2>&1 || {
        log_error "Backup not found in k3d node after copy"
        exit 1
    }
    
    log_success "Backup imported to k3d node"
    
    # Wait for MinIO to detect the backup
    log_info "Waiting for MinIO to detect backup..."
    local minio_pod=$(kubectl --context="${KUBE_CONTEXT}" get pod -n velero -l app=minio -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    
    if [ -n "$minio_pod" ]; then
        # Wait up to 60 seconds for Velero to detect the backup
        local max_wait=60
        local elapsed=0
        local interval=2
        
        while [ $elapsed -lt $max_wait ]; do
            # Check if Velero can see the backup
            if velero backup get "${BACKUP_NAME}" --kubecontext="${KUBE_CONTEXT}" >/dev/null 2>&1; then
                log_success "MinIO detected backup"
                break
            fi
            
            sleep $interval
            elapsed=$((elapsed + interval))
            if [ $((elapsed % 10)) -eq 0 ]; then
                log_info "Still waiting for MinIO to detect backup... (${elapsed}s elapsed)"
            fi
        done
        
        if [ $elapsed -ge $max_wait ]; then
            log_warning "MinIO may not have detected backup yet, but continuing..."
            log_info "You may need to wait a bit longer or check MinIO logs"
        fi
    else
        log_warning "MinIO pod not found, skipping detection wait"
    fi
}

# Confirm restore operation
confirm_restore() {
    if [ "$FORCE" = "true" ] || [ "$DRY_RUN" = "true" ]; then
        return 0
    fi
    
    log_warning "This will restore data from backup to the current cluster!"
    log_info "Backup: ${BACKUP_NAME}"
    log_info "Target Cluster: ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    echo ""
    log_warning "IMPORTANT: Make sure you have run a fresh OpenChoreo installation before restoring."
    echo ""
    
    read -p "Continue with restore? (yes/no): " -r
    if [[ ! $REPLY =~ ^[Yy][Ee][Ss]$ ]]; then
        log_info "Restore cancelled by user"
        exit 0
    fi
}

# Pre-restore checks
pre_restore_checks() {
    log_step "Pre-restore checks..."
    
    # Check if there are existing resources that might conflict
    log_info "Checking for existing resources..."
    
    local org_count=$(kubectl --context="${KUBE_CONTEXT}" get organizations -A --no-headers 2>/dev/null | wc -l)
    local proj_count=$(kubectl --context="${KUBE_CONTEXT}" get projects -A --no-headers 2>/dev/null | wc -l)
    local comp_count=$(kubectl --context="${KUBE_CONTEXT}" get components -A --no-headers 2>/dev/null | wc -l)
    
    if [ "$org_count" -gt 0 ] || [ "$proj_count" -gt 0 ] || [ "$comp_count" -gt 0 ]; then
        log_warning "Found existing OpenChoreo resources in cluster:"
        log_warning "  Organizations: ${org_count}"
        log_warning "  Projects: ${proj_count}"
        log_warning "  Components: ${comp_count}"
        
        if [ "$FORCE" != "true" ]; then
            log_warning "Restoring may cause conflicts or overwrites"
            read -p "Continue anyway? (yes/no): " -r
            if [[ ! $REPLY =~ ^[Yy][Ee][Ss]$ ]]; then
                log_info "Restore cancelled"
                exit 0
            fi
        fi
    else
        log_success "No existing resources found - clean slate"
    fi
}

# Create restore
create_restore() {
    log_step "Creating restore: ${RESTORE_NAME:-from ${BACKUP_NAME}}"
    
    # Generate restore name if not provided
    if [ -z "$RESTORE_NAME" ]; then
        RESTORE_NAME="${BACKUP_NAME}-restore-$(date +%Y%m%d-%H%M%S)"
    fi
    
    # Build Velero restore command
    local restore_args=(
        "restore" "create" "${RESTORE_NAME}"
        "--from-backup" "${BACKUP_NAME}"
        "--force"
        "--kubecontext" "${KUBE_CONTEXT}"
    )
    
    # Add restore volumes flag
    if [ "$RESTORE_VOLUMES" = "true" ]; then
        restore_args+=("--restore-volumes=true")
    else
        restore_args+=("--restore-volumes=false")
    fi
    
    # Add wait flag
    if [ "$WAIT_FOR_COMPLETION" = "true" ] && [ "$DRY_RUN" != "true" ]; then
        restore_args+=("--wait")
    fi
    
    # Add namespace mappings if specified
    if [ -n "$NAMESPACE_MAPPINGS" ]; then
        restore_args+=("--namespace-mappings" "${NAMESPACE_MAPPINGS}")
    fi
    
    # Execute restore
    if [ "$DRY_RUN" = "true" ]; then
        log_info "DRY RUN: Would execute: velero ${restore_args[*]}"
        log_info "Backup contents:"
        velero backup describe "${BACKUP_NAME}" --kubecontext="${KUBE_CONTEXT}" --details
        log_success "Dry run complete - no changes made"
        exit 0
    fi
    
    log_info "Running: velero ${restore_args[*]}"
    echo ""
    
    if velero "${restore_args[@]}"; then
        log_success "Restore started successfully"
    else
        log_error "Failed to create restore"
        return 1
    fi
}

# Wait for restore to complete
wait_for_restore() {
    if [ "$WAIT_FOR_COMPLETION" = "true" ]; then
        return 0  # Already waited with --wait flag
    fi
    
    log_step "Waiting for restore to complete..."
    
    local max_wait=3600  # 1 hour
    local elapsed=0
    local interval=10
    
    while [ $elapsed -lt $max_wait ]; do
        local status=$(velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | \
            grep "Phase:" | awk '{print $2}' | tr -d '[:space:]' | tr '[:lower:]' '[:upper:]')
        
        case $status in
            COMPLETED)
                log_success "Restore completed"
                return 0
                ;;
            FAILED|PARTIALLYFAILED)
                log_error "Restore failed or partially failed"
                return 1
                ;;
            INPROGRESS|NEW)
                log_info "Restore in progress... (${elapsed}s elapsed)"
                ;;
            *)
                log_warning "Unknown restore status: ${status}"
                ;;
        esac
        
        sleep $interval
        elapsed=$((elapsed + interval))
    done
    
    log_error "Restore timed out after ${max_wait} seconds"
    return 1
}

# Wait for pods to be ready
wait_for_pods() {
    log_step "Waiting for pods to be ready..."
    
    local namespaces=(
        "openchoreo-control-plane"
        "openchoreo-data-plane"
        "openchoreo-build-plane"
        "openchoreo-observability-plane"
    )
    
    for ns in "${namespaces[@]}"; do
        if ! kubectl --context="${KUBE_CONTEXT}" get namespace "$ns" >/dev/null 2>&1; then
            log_info "Skipping namespace '${ns}' (does not exist)"
            continue
        fi
        
        log_info "Waiting for pods in ${ns}..."
        
        # Wait for pods with a timeout
        kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
            --all -n "$ns" --timeout=600s 2>/dev/null || {
            log_warning "Some pods in ${ns} may not be ready yet"
        }
    done
    
    log_success "Pods are starting up"
}

# Verify restore
verify_restore() {
    log_step "Verifying restore..."
    
    # Get restore details
    log_info "Restore details:"
    velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" --details || {
        log_error "Failed to get restore details"
        return 1
    }
    
    # Check restore status
    local phase_line=$(velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | grep "^Phase:")
    
    if echo "$phase_line" | grep -q "Completed"; then
        log_success "Restore phase: Completed"
    else
        log_error "Restore did not complete successfully"
        log_info "Phase: ${phase_line}"
        velero restore logs "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | grep -i error || true
        return 1
    fi
    
    # Check for warnings
    local warnings=$(velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | \
        grep "Warnings:" | awk '{print $NF}')
    
    if [ -n "$warnings" ] && [ "$warnings" != "0" ]; then
        log_warning "Restore completed with ${warnings} warnings"
        log_info "View warnings: velero restore logs ${RESTORE_NAME}"
    fi
    
    # Verify critical resources
    log_info "Verifying critical resources..."
    
    local org_count=$(kubectl --context="${KUBE_CONTEXT}" get organizations -A --no-headers 2>/dev/null | wc -l)
    local proj_count=$(kubectl --context="${KUBE_CONTEXT}" get projects -A --no-headers 2>/dev/null | wc -l)
    local comp_count=$(kubectl --context="${KUBE_CONTEXT}" get components -A --no-headers 2>/dev/null | wc -l)
    local compwf_count=$(kubectl --context="${KUBE_CONTEXT}" get componentworkflows -A --no-headers 2>/dev/null | wc -l)
    local env_count=$(kubectl --context="${KUBE_CONTEXT}" get environments -A --no-headers 2>/dev/null | wc -l)
    
    log_info "Restored resources:"
    log_info "  Organizations: ${org_count}"
    log_info "  Projects: ${proj_count}"
    log_info "  Components: ${comp_count}"
    log_info "  ComponentWorkflows: ${compwf_count}"
    log_info "  Environments: ${env_count}"
    
    if [ "$org_count" -eq 0 ] && [ "$proj_count" -eq 0 ] && [ "$comp_count" -eq 0 ]; then
        log_warning "No OpenChoreo resources found after restore"
        log_warning "This might indicate the backup was empty or restore failed"
    fi
    
    log_success "Restore verified"
}

# Show restore summary
show_summary() {
    log_step "Restore Summary"
    
    echo -e "${BOLD}Restore completed successfully!${RESET}\n"
    echo -e "${CYAN}Restore Name:${RESET} ${RESTORE_NAME}"
    echo -e "${CYAN}Backup Name:${RESET} ${BACKUP_NAME}"
    echo -e "${CYAN}Cluster:${RESET} ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    
    # Get restore info
    local restore_info=$(velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null || echo "")
    
    if [ -n "$restore_info" ]; then
        local started=$(echo "$restore_info" | grep "Started:" | cut -d: -f2- | xargs)
        local completed=$(echo "$restore_info" | grep "Completed:" | cut -d: -f2- | xargs)
        
        echo -e "${CYAN}Started:${RESET} ${started}"
        echo -e "${CYAN}Completed:${RESET} ${completed}"
    fi
    
    # Show resource counts
    log_info ""
    log_info "Resource Counts:"
    echo -e "  Organizations:        $(kubectl --context="${KUBE_CONTEXT}" get organizations -A --no-headers 2>/dev/null | wc -l)"
    echo -e "  Projects:             $(kubectl --context="${KUBE_CONTEXT}" get projects -A --no-headers 2>/dev/null | wc -l)"
    echo -e "  Components:           $(kubectl --context="${KUBE_CONTEXT}" get components -A --no-headers 2>/dev/null | wc -l)"
    echo -e "  ComponentWorkflows:   $(kubectl --context="${KUBE_CONTEXT}" get componentworkflows -A --no-headers 2>/dev/null | wc -l)"
    echo -e "  ComponentWorkflowRuns: $(kubectl --context="${KUBE_CONTEXT}" get componentworkflowruns -A --no-headers 2>/dev/null | wc -l)"
    echo -e "  Environments:         $(kubectl --context="${KUBE_CONTEXT}" get environments -A --no-headers 2>/dev/null | wc -l)"
    
    echo -e "\n${GREEN}${BOLD}What was restored:${RESET}"
    echo -e "  ✓ All OpenChoreo custom resources"
    echo -e "  ✓ All Kubernetes resources"
    echo -e "  ✓ Persistent volumes"
    echo -e "  ✓ Secrets and ConfigMaps"
    echo -e "  ✓ Complete system state"
    
    echo -e "\n${CYAN}${BOLD}Next Steps:${RESET}"
    echo -e "  • View restore details:  ${YELLOW}velero restore describe ${RESTORE_NAME}${RESET}"
    echo -e "  • View restore logs:     ${YELLOW}velero restore logs ${RESTORE_NAME}${RESET}"
    echo -e "  • Check pods:            ${YELLOW}kubectl --context=${KUBE_CONTEXT} get pods -A${RESET}"
    echo -e "  • Check organizations:   ${YELLOW}kubectl --context=${KUBE_CONTEXT} get organizations -A${RESET}"
    echo -e "  • Check components:      ${YELLOW}kubectl --context=${KUBE_CONTEXT} get components -A${RESET}"
    
    echo -e "\n${CYAN}${BOLD}Verification:${RESET}"
    echo -e "  1. Verify resources exist (see above counts)"
    echo -e "  2. Check pods are running: ${YELLOW}kubectl get pods -A${RESET}"
    echo -e "  3. Try to trigger a build (test ComponentWorkflows)"
    echo -e "  4. Check logs are accessible (if Observability Plane was restored)"
    
    echo ""
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Restore OpenChoreo from a Velero backup.

IMPORTANT: Run this AFTER a fresh OpenChoreo installation:
  1. Install fresh cluster: cd ../installation && ./install-single-cluster.sh
  2. Run this restore script: $0 --backup <backup-name>

Options:
  --backup NAME                Backup name to restore from (required)
  --restore-name NAME          Custom restore name [default: auto-generated]
  --cluster-name NAME          Cluster name [default: openchoreo]
  --no-volumes                 Skip volume restore (faster, config only)
  --no-wait                    Don't wait for restore completion
  --dry-run                    Preview what would be restored
  --force                      Skip confirmation prompts
  --namespace-mappings MAP     Restore to different namespaces
                               Format: "old-ns1:new-ns1,old-ns2:new-ns2"
  --help, -h                   Show this help message

Environment Variables:
  CLUSTER_NAME                 Cluster name [default: openchoreo]
  BACKUP_NAME                  Backup to restore from
  RESTORE_NAME                 Custom restore name
  BACKUP_ROOT                  Backup root directory [default: <repo-parent>/openchoreo-backups]
                               Example: export BACKUP_ROOT=/home/user/openchoreo-backups

Examples:
  # List available backups
  velero backup get
  
  # Restore from specific backup
  $0 --backup openchoreo-20241201-120000
  
  # Dry run to preview restore
  $0 --backup openchoreo-20241201-120000 --dry-run
  
  # Fast restore without volumes (config only)
  $0 --backup openchoreo-20241201-120000 --no-volumes
  
  # Restore to test environment (different namespaces)
  $0 --backup openchoreo-20241201-120000 \\
     --namespace-mappings "openchoreo-control-plane:test-control-plane"

EOF
}

# Parse command line arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --backup)
                BACKUP_NAME="$2"
                shift 2
                ;;
            --restore-name)
                RESTORE_NAME="$2"
                shift 2
                ;;
            --cluster-name)
                CLUSTER_NAME="$2"
                KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
                shift 2
                ;;
            --no-volumes)
                RESTORE_VOLUMES=false
                shift
                ;;
            --no-wait)
                WAIT_FOR_COMPLETION=false
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --force)
                FORCE=true
                shift
                ;;
            --namespace-mappings)
                NAMESPACE_MAPPINGS="$2"
                shift 2
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

# Main restore flow
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
    echo -e "${BOLD}Velero Restore Script${RESET}\n"
    
    parse_args "$@"
    
    # Display configuration
    log_info "Restore Configuration:"
    log_info "  Backup: ${BACKUP_NAME:-<not specified>}"
    log_info "  Restore Name: ${RESTORE_NAME:-<auto-generated>}"
    log_info "  Cluster: ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    log_info "  Restore Volumes: ${RESTORE_VOLUMES}"
    log_info "  Dry Run: ${DRY_RUN}"
    if [ -n "$NAMESPACE_MAPPINGS" ]; then
        log_info "  Namespace Mappings: ${NAMESPACE_MAPPINGS}"
    fi
    echo ""
    
    # Execute restore
    validate_prerequisites
    verify_minio_publicurl
    import_backup
    validate_backup
    confirm_restore
    pre_restore_checks
    create_restore
    
    if [ "$DRY_RUN" != "true" ]; then
        wait_for_restore
        wait_for_pods
        verify_restore
        show_summary
    fi
}

# Run main function
main "$@"

