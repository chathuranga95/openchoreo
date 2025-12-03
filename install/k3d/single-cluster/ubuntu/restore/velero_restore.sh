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

# Repository password for node-agent (must match the one used during backup)
REPO_PASSWORD="${REPO_PASSWORD:-static-passw0rd}"

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

# Load repository password from saved file
load_repo_password() {
    local password_file="${BACKUP_ROOT}/.velero-repo-password"
    
    if [ -z "$REPO_PASSWORD" ]; then
        if [ -f "$password_file" ]; then
            REPO_PASSWORD=$(cat "$password_file")
            log_info "Loaded repository password from: ${password_file}"
        else
            log_error "Repository password not found!"
            log_error "Expected location: ${password_file}"
            log_info ""
            log_info "The repository password is needed to decrypt volume backups."
            log_info "You can set it via environment variable:"
            log_info "  export REPO_PASSWORD='your-password-here'"
            log_info ""
            log_info "If you don't know the password, check the backup source system."
            exit 1
        fi
    else
        log_info "Using repository password from environment variable"
    fi
}

# Check repository password matches
check_repo_password() {
    log_step "Checking repository password..."
    
    # Get the repository password secret from the cluster
    local cluster_password=$(kubectl --context="${KUBE_CONTEXT}" get secret \
        -n "${VELERO_NAMESPACE}" velero-repo-credentials \
        -o jsonpath='{.data.repository-password}' 2>/dev/null | base64 -d)
    
    if [ -z "$cluster_password" ]; then
        log_warning "Repository password secret not found in cluster"
        log_info "This might be a fresh Velero installation - will set the correct password"
        return 1
    fi
    
    if [ "$cluster_password" != "$REPO_PASSWORD" ]; then
        log_error "Repository password mismatch!"
        log_error "The cluster has a different repository password than the backup"
        log_info ""
        log_info "To fix this, you need to:"
        log_info "  1. Uninstall Velero: velero uninstall --force"
        log_info "  2. Set correct password: export REPO_PASSWORD='<correct-password>'"
        log_info "  3. Reinstall Velero: cd ../backup && ./velero_install_local.sh"
        log_info "  4. Run restore again"
        exit 1
    fi
    
    log_success "Repository password matches"
    return 0
}

# Ensure repository password is set in cluster
ensure_repo_password() {
    if ! check_repo_password; then
        log_info "Setting repository password in cluster..."
        
        # Create/update the repository password secret
        kubectl --context="${KUBE_CONTEXT}" create secret generic velero-repo-credentials \
            -n "${VELERO_NAMESPACE}" \
            --from-literal=repository-password="$REPO_PASSWORD" \
            --dry-run=client -o yaml | \
            kubectl --context="${KUBE_CONTEXT}" apply -f -
        
        # Restart node-agent pods to pick up the new password
        log_info "Restarting node-agent pods..."
        kubectl --context="${KUBE_CONTEXT}" delete pods \
            -n "${VELERO_NAMESPACE}" -l name=node-agent 2>/dev/null || true
        
        # Wait for node-agent to be ready again
        sleep 10
        kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
            -n "${VELERO_NAMESPACE}" -l name=node-agent --timeout=120s || {
            log_warning "Node-agent pods may not be ready yet"
        }
        
        log_success "Repository password configured"
    fi
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

# Check node-agent health
check_node_agent_health() {
    log_step "Checking node-agent health..."
    
    local node_agent_pods=$(kubectl --context="${KUBE_CONTEXT}" get pods \
        -n "${VELERO_NAMESPACE}" -l name=node-agent \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)
    
    if [ -z "$node_agent_pods" ]; then
        log_error "No node-agent pods found!"
        log_info "Node-agent is required for pod volume restores"
        log_info "Please ensure Velero is properly installed with --use-node-agent"
        return 1
    fi
    
    log_info "Found node-agent pods: ${node_agent_pods}"
    
    # Check if node-agent pods are ready
    local ready_count=$(kubectl --context="${KUBE_CONTEXT}" get pods \
        -n "${VELERO_NAMESPACE}" -l name=node-agent \
        --field-selector=status.phase=Running \
        -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | \
        grep -o "True" | wc -l)
    
    local total_count=$(echo "$node_agent_pods" | wc -w)
    
    if [ "$ready_count" -lt "$total_count" ]; then
        log_warning "Some node-agent pods are not ready (${ready_count}/${total_count})"
        log_info "Waiting for node-agent pods to be ready..."
        
        kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
            -n "${VELERO_NAMESPACE}" -l name=node-agent --timeout=120s || {
            log_error "Node-agent pods failed to become ready"
            log_info "Check node-agent logs:"
            log_info "  kubectl --context=${KUBE_CONTEXT} logs -n ${VELERO_NAMESPACE} -l name=node-agent --tail=50"
            return 1
        }
    fi
    
    log_success "Node-agent pods are healthy"
    return 0
}

# Pre-restore checks
pre_restore_checks() {
    log_step "Pre-restore checks..."
    
    # Check node-agent health if restoring volumes
    if [ "$RESTORE_VOLUMES" = "true" ]; then
        if ! check_node_agent_health; then
            log_error "Node-agent health check failed"
            log_info "Pod volume restores require healthy node-agent pods"
            exit 1
        fi
    fi
    
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

# Fix pod volume restore errors by retrying failed PVRs
fix_pod_volume_restore_errors() {
    log_step "Attempting to fix pod volume restore errors..."
    
    # Get all failed PVRs for this restore
    local failed_pvrs=$(kubectl --context="${KUBE_CONTEXT}" get pvr \
        -n "${VELERO_NAMESPACE}" -l velero.io/restore-name="${RESTORE_NAME}" \
        --field-selector=status.phase=Failed -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)
    
    if [ -z "$failed_pvrs" ]; then
        log_info "No failed pod volume restores found"
        return 0
    fi
    
    log_warning "Found failed pod volume restores: ${failed_pvrs}"
    log_info "Deleting failed PVRs to allow Velero to retry..."
    
    local retry_count=0
    local max_retries=2
    
    for pvr in $failed_pvrs; do
        log_info "Deleting failed PVR: ${pvr}"
        kubectl --context="${KUBE_CONTEXT}" delete pvr "${pvr}" \
            -n "${VELERO_NAMESPACE}" --wait=false 2>/dev/null || true
        
        # Get the pod name from PVR to check if pod exists
        local pod_name=$(kubectl --context="${KUBE_CONTEXT}" get pvr "${pvr}" \
            -n "${VELERO_NAMESPACE}" -o jsonpath='{.spec.pod.name}' 2>/dev/null || echo "")
        
        if [ -n "$pod_name" ]; then
            local pod_namespace=$(kubectl --context="${KUBE_CONTEXT}" get pvr "${pvr}" \
                -n "${VELERO_NAMESPACE}" -o jsonpath='{.spec.pod.namespace}' 2>/dev/null || echo "")
            
            if [ -n "$pod_namespace" ]; then
                log_info "Checking if pod ${pod_namespace}/${pod_name} is ready..."
                
                # Wait for pod to be ready (if it exists)
                if kubectl --context="${KUBE_CONTEXT}" get pod "${pod_name}" \
                    -n "${pod_namespace}" >/dev/null 2>&1; then
                    log_info "Waiting for pod ${pod_namespace}/${pod_name} to be ready..."
                    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready \
                        pod/"${pod_name}" -n "${pod_namespace}" --timeout=60s 2>/dev/null || {
                        log_warning "Pod ${pod_namespace}/${pod_name} may not be ready yet"
                    }
                fi
            fi
        fi
    done
    
    log_info "Waiting for Velero to retry pod volume restores..."
    sleep 15
    
    # Check if PVRs are being retried
    local new_pvrs=$(kubectl --context="${KUBE_CONTEXT}" get pvr \
        -n "${VELERO_NAMESPACE}" -l velero.io/restore-name="${RESTORE_NAME}" \
        --field-selector=status.phase=InProgress -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)
    
    if [ -n "$new_pvrs" ]; then
        log_success "Pod volume restores are being retried"
        return 0
    else
        log_warning "No new pod volume restores detected"
        return 1
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
        
        # If restoring volumes, wait a bit for pods to be created before volume restore starts
        if [ "$RESTORE_VOLUMES" = "true" ]; then
            log_info "Waiting for pods to be created before volume restore starts..."
            sleep 10
        fi
    else
        log_error "Failed to create restore"
        return 1
    fi
}

# Check for pod volume restore errors
check_pod_volume_restore_errors() {
    log_info "Checking for pod volume restore errors..."
    
    # Get restore logs and check for common errors
    local restore_logs=$(velero restore logs "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null)
    
    if echo "$restore_logs" | grep -q "no such file or directory"; then
        log_warning "Detected 'no such file or directory' errors in restore logs"
        log_info "This usually means parent directories don't exist when restoring files"
        log_info "This can happen if pods are restored before volumes are ready"
        
        # Check if there are failed PVRs (Pod Volume Restores)
        local failed_pvrs=$(kubectl --context="${KUBE_CONTEXT}" get pvr \
            -n "${VELERO_NAMESPACE}" -l velero.io/restore-name="${RESTORE_NAME}" \
            --field-selector=status.phase=Failed -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)
        
        if [ -n "$failed_pvrs" ]; then
            log_warning "Found failed Pod Volume Restores: ${failed_pvrs}"
            log_info "Attempting to retry failed PVRs..."
            
            # Delete failed PVRs to allow retry
            for pvr in $failed_pvrs; do
                log_info "Deleting failed PVR: ${pvr}"
                kubectl --context="${KUBE_CONTEXT}" delete pvr "${pvr}" \
                    -n "${VELERO_NAMESPACE}" --wait=false 2>/dev/null || true
            done
            
            # Wait a bit for Velero to retry
            sleep 10
            
            return 1  # Indicate retry needed
        fi
        
        return 1
    fi
    
    return 0
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
    local retry_count=0
    local max_retries=3
    
    while [ $elapsed -lt $max_wait ]; do
        local status=$(velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | \
            grep "Phase:" | awk '{print $2}' | tr -d '[:space:]' | tr '[:lower:]' '[:upper:]')
        
        case $status in
            COMPLETED)
                log_success "Restore completed"
                return 0
                ;;
            FAILED|PARTIALLYFAILED)
                # Check for pod volume restore errors that might be retryable
                if [ "$RESTORE_VOLUMES" = "true" ] && [ $retry_count -lt $max_retries ]; then
                    if check_pod_volume_restore_errors; then
                        log_info "Retrying failed pod volume restores (attempt $((retry_count + 1))/${max_retries})..."
                        if fix_pod_volume_restore_errors; then
                            retry_count=$((retry_count + 1))
                            sleep 30  # Wait before checking again
                            continue
                        fi
                    fi
                fi
                
                log_error "Restore failed or partially failed"
                log_info "Restore status:"
                velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" | head -30
                log_info ""
                log_info "Restore logs (errors only):"
                velero restore logs "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | grep -i error | tail -20 || true
                return 1
                ;;
            INPROGRESS|NEW)
                log_info "Restore in progress... (${elapsed}s elapsed)"
                # Periodically check for pod volume restore errors
                if [ "$RESTORE_VOLUMES" = "true" ] && [ $((elapsed % 60)) -eq 0 ] && [ $elapsed -gt 0 ]; then
                    check_pod_volume_restore_errors || true
                fi
                ;;
            *)
                log_warning "Unknown restore status: ${status}"
                ;;
        esac
        
        sleep $interval
        elapsed=$((elapsed + interval))
    done
    
    log_error "Restore timed out after ${max_wait} seconds"
    log_info "Final restore status:"
    velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" | head -30
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
        
        # Check for pod volume restore errors specifically
        if [ "$RESTORE_VOLUMES" = "true" ]; then
            log_info "Checking pod volume restore status..."
            local pvr_status=$(kubectl --context="${KUBE_CONTEXT}" get pvr \
                -n "${VELERO_NAMESPACE}" -l velero.io/restore-name="${RESTORE_NAME}" \
                -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.status.phase}{"\n"}{end}' 2>/dev/null)
            
            if [ -n "$pvr_status" ]; then
                log_info "Pod Volume Restore status:"
                echo "$pvr_status" | while read -r line; do
                    if echo "$line" | grep -q "Failed"; then
                        log_warning "  $line"
                    else
                        log_info "  $line"
                    fi
                done
            fi
        fi
        
        log_info "Restore logs (errors only):"
        velero restore logs "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | grep -i error | tail -20 || true
        
        # Show troubleshooting tips
        log_info ""
        log_info "${YELLOW}Troubleshooting tips:${RESET}"
        log_info "1. Check node-agent logs:"
        log_info "   kubectl --context=${KUBE_CONTEXT} logs -n ${VELERO_NAMESPACE} -l name=node-agent --tail=100"
        log_info ""
        log_info "2. Check Velero server logs:"
        log_info "   kubectl --context=${KUBE_CONTEXT} logs -n ${VELERO_NAMESPACE} deployment/velero --tail=100"
        log_info ""
        log_info "3. If pod volume restore failed with 'no such file or directory':"
        log_info "   - This usually means directories don't exist when restoring files"
        log_info "   - Try deleting failed PVRs and retrying:"
        log_info "     kubectl --context=${KUBE_CONTEXT} delete pvr -n ${VELERO_NAMESPACE} -l velero.io/restore-name=${RESTORE_NAME} --field-selector=status.phase=Failed"
        log_info "   - Then wait for Velero to retry automatically"
        log_info ""
        log_info "4. If issues persist, try restoring without volumes first:"
        log_info "   $0 --backup ${BACKUP_NAME} --no-volumes"
        
        return 1
    fi
    
    # Check for warnings
    local warnings=$(velero restore describe "${RESTORE_NAME}" --kubecontext="${KUBE_CONTEXT}" 2>/dev/null | \
        grep "Warnings:" | awk '{print $NF}')
    
    if [ -n "$warnings" ] && [ "$warnings" != "0" ]; then
        log_warning "Restore completed with ${warnings} warnings"
        log_info "View warnings: velero restore logs ${RESTORE_NAME}"
    fi
    
    # Check pod volume restore status if volumes were restored
    if [ "$RESTORE_VOLUMES" = "true" ]; then
        log_info "Checking pod volume restore status..."
        local failed_pvrs=$(kubectl --context="${KUBE_CONTEXT}" get pvr \
            -n "${VELERO_NAMESPACE}" -l velero.io/restore-name="${RESTORE_NAME}" \
            --field-selector=status.phase=Failed -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)
        
        if [ -n "$failed_pvrs" ]; then
            log_warning "Some pod volume restores failed: ${failed_pvrs}"
            log_info "You may need to manually retry these PVRs or restore without volumes"
        else
            log_success "All pod volume restores completed successfully"
        fi
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
  2. Install Velero with SAME repo password: cd ../backup && REPO_PASSWORD='<password>' ./velero_install_local.sh
  3. Run this restore script: $0 --backup <backup-name>
  
NOTE: The repository password is critical for decrypting volume backups.
      It will be auto-loaded from ${BACKUP_ROOT}/.velero-repo-password

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
  REPO_PASSWORD                Repository password (auto-loaded from .velero-repo-password)
                               CRITICAL: Must match the password used during backup!

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

Troubleshooting:

Pod Volume Restore Errors ("no such file or directory"):
  This error occurs when Velero tries to restore files before directories exist.
  The script automatically retries failed pod volume restores, but if issues persist:
  
  1. Check node-agent health:
     kubectl --context=k3d-openchoreo get pods -n velero -l name=node-agent
     kubectl --context=k3d-openchoreo logs -n velero -l name=node-agent --tail=100
  
  2. Manually retry failed PVRs:
     kubectl --context=k3d-openchoreo delete pvr -n velero \\
       -l velero.io/restore-name=<restore-name> \\
       --field-selector=status.phase=Failed
     # Velero will automatically retry
  
  3. If volumes aren't critical, restore without them:
     $0 --backup <backup-name> --no-volumes
  
  4. Check Velero server logs:
     kubectl --context=k3d-openchoreo logs -n velero deployment/velero --tail=100

Repository Password Mismatch:
  If restore fails with password errors:
  1. Verify password matches backup: cat ${BACKUP_ROOT}/.velero-repo-password
  2. Set password: export REPO_PASSWORD='<correct-password>'
  3. Reinstall Velero: cd ../backup && ./velero_install_local.sh

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
    log_info "  Backup Root: ${BACKUP_ROOT}"
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
    load_repo_password
    ensure_repo_password
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

