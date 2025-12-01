#!/usr/bin/env bash

# OpenChoreo Velero Backup Script
# Creates a complete backup of OpenChoreo using Velero

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
BACKUP_NAME="${BACKUP_NAME:-openchoreo-$(date +%Y%m%d-%H%M%S)}"
VELERO_NAMESPACE="${VELERO_NAMESPACE:-velero}"

# Backup options
BACKUP_NAMESPACES="${BACKUP_NAMESPACES:-openchoreo-control-plane,openchoreo-data-plane,openchoreo-build-plane,openchoreo-observability-plane}"
INCLUDE_CLUSTER_RESOURCES="${INCLUDE_CLUSTER_RESOURCES:-true}"
BACKUP_VOLUMES="${BACKUP_VOLUMES:-true}"
WAIT_FOR_COMPLETION="${WAIT_FOR_COMPLETION:-true}"
BACKUP_TTL="${BACKUP_TTL:-720h}"  # 30 days

# Selective backup flags
BACKUP_CP="${BACKUP_CP:-true}"
BACKUP_DP="${BACKUP_DP:-true}"
BACKUP_BP="${BACKUP_BP:-true}"
BACKUP_OP="${BACKUP_OP:-true}"

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
    
    if ! command_exists velero; then
        missing_tools+=("velero")
    fi
    
    if ! command_exists kubectl; then
        missing_tools+=("kubectl")
    fi
    
    if [ ${#missing_tools[@]} -gt 0 ]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        log_info "Please run ./velero_install.sh first"
        exit 1
    fi
    
    # Check cluster access
    if ! kubectl --context="${KUBE_CONTEXT}" cluster-info >/dev/null 2>&1; then
        log_error "Cannot access cluster '${CLUSTER_NAME}' with context '${KUBE_CONTEXT}'"
        exit 1
    fi
    
    # Check Velero is installed
    if ! kubectl --context="${KUBE_CONTEXT}" get namespace "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        log_error "Velero is not installed in cluster"
        log_info "Please run ./velero_install.sh first"
        exit 1
    fi
    
    # Check Velero is ready
    if ! kubectl --context="${KUBE_CONTEXT}" get deployment velero -n "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        log_error "Velero deployment not found"
        exit 1
    fi
    
    log_success "Prerequisites validated"
}

# Build namespace list based on flags
build_namespace_list() {
    local namespaces=()
    
    [ "$BACKUP_CP" = "true" ] && namespaces+=("openchoreo-control-plane")
    [ "$BACKUP_DP" = "true" ] && namespaces+=("openchoreo-data-plane")
    [ "$BACKUP_BP" = "true" ] && namespaces+=("openchoreo-build-plane")
    [ "$BACKUP_OP" = "true" ] && namespaces+=("openchoreo-observability-plane")
    
    # Add keel if it exists
    if kubectl --context="${KUBE_CONTEXT}" get namespace keel >/dev/null 2>&1; then
        namespaces+=("keel")
    fi
    
    # Join with commas
    BACKUP_NAMESPACES=$(IFS=,; echo "${namespaces[*]}")
    
    if [ -z "$BACKUP_NAMESPACES" ]; then
        log_error "No namespaces selected for backup"
        exit 1
    fi
}

# Pre-backup validation
pre_backup_validation() {
    log_step "Pre-backup validation..."
    
    # Check backup storage location is available
    log_info "Checking backup storage location..."
    if ! velero backup-location get >/dev/null 2>&1; then
        log_error "No backup storage location configured"
        exit 1
    fi
    
    local bsl_status=$(velero backup-location get 2>/dev/null | grep -w "Available" || echo "")
    
    if [ -z "$bsl_status" ]; then
        log_warning "Could not verify backup storage location status, continuing anyway..."
        velero backup-location get
    else
        log_success "Backup storage location is available"
    fi
    
    # Check namespaces exist
    log_info "Validating namespaces..."
    IFS=',' read -ra NS_ARRAY <<< "$BACKUP_NAMESPACES"
    for ns in "${NS_ARRAY[@]}"; do
        if ! kubectl --context="${KUBE_CONTEXT}" get namespace "$ns" >/dev/null 2>&1; then
            log_warning "Namespace '$ns' does not exist (will be skipped)"
        else
            log_info "  ✓ Namespace '$ns' exists"
        fi
    done
    
    log_success "Pre-backup validation complete"
}

# Count resources before backup
count_resources() {
    log_step "Counting resources to backup..."
    
    local total_resources=0
    
    IFS=',' read -ra NS_ARRAY <<< "$BACKUP_NAMESPACES"
    for ns in "${NS_ARRAY[@]}"; do
        if kubectl --context="${KUBE_CONTEXT}" get namespace "$ns" >/dev/null 2>&1; then
            local count=$(kubectl --context="${KUBE_CONTEXT}" get all,cm,secret,pvc -n "$ns" --no-headers 2>/dev/null | wc -l)
            log_info "  Namespace '${ns}': ${count} resources"
            total_resources=$((total_resources + count))
        fi
    done
    
    # Count OpenChoreo CRDs
    if [ "$BACKUP_CP" = "true" ]; then
        local crd_count=$(kubectl --context="${KUBE_CONTEXT}" get \
            organizations,projects,components,componentworkflows,componentworkflowruns \
            -A --no-headers 2>/dev/null | wc -l)
        log_info "  OpenChoreo CRs: ${crd_count} resources"
        total_resources=$((total_resources + crd_count))
    fi
    
    log_info "Total resources to backup: ${total_resources}"
}

# Create Velero backup
create_backup() {
    log_step "Creating backup: ${BACKUP_NAME}"
    
    # Build Velero backup command
    local backup_args=(
        "backup" "create" "${BACKUP_NAME}"
        "--include-namespaces" "${BACKUP_NAMESPACES}"
    )
    
    # Add cluster resources
    if [ "$INCLUDE_CLUSTER_RESOURCES" = "true" ]; then
        backup_args+=("--include-cluster-resources=true")
    fi
    
    # Add volume backup
    if [ "$BACKUP_VOLUMES" = "true" ]; then
        backup_args+=("--default-volumes-to-fs-backup=true")
    fi
    
    # Add TTL
    if [ -n "$BACKUP_TTL" ]; then
        backup_args+=("--ttl" "${BACKUP_TTL}")
    fi
    
    # Add wait flag
    if [ "$WAIT_FOR_COMPLETION" = "true" ]; then
        backup_args+=("--wait")
    fi
    
    # Execute backup
    log_info "Running: velero ${backup_args[*]}"
    echo ""
    
    if velero "${backup_args[@]}"; then
        log_success "Backup created successfully"
    else
        log_error "Failed to create backup"
        return 1
    fi
}

# Wait for backup to complete (if not using --wait)
wait_for_backup() {
    if [ "$WAIT_FOR_COMPLETION" = "true" ]; then
        return 0  # Already waited with --wait flag
    fi
    
    log_step "Waiting for backup to complete..."
    
    local max_wait=3600  # 1 hour
    local elapsed=0
    local interval=10
    
    while [ $elapsed -lt $max_wait ]; do
        local status=$(velero backup describe "${BACKUP_NAME}" 2>/dev/null | \
            grep "Phase:" | awk '{print $2}')
        
        case $status in
            Completed)
                log_success "Backup completed"
                return 0
                ;;
            Failed|PartiallyFailed)
                log_error "Backup failed or partially failed"
                return 1
                ;;
            InProgress)
                log_info "Backup in progress... (${elapsed}s elapsed)"
                ;;
            *)
                log_warning "Unknown backup status: ${status}"
                ;;
        esac
        
        sleep $interval
        elapsed=$((elapsed + interval))
    done
    
    log_error "Backup timed out after ${max_wait} seconds"
    return 1
}

# Export backup to local filesystem
export_backup() {
    log_step "Exporting backup from k3d node..."
    
    local export_dir="$(dirname "${REPO_ROOT}")/openchoreo-backups"
    local k3d_node="k3d-${CLUSTER_NAME}-server-0"
    local backup_path_in_node="${export_dir}"
    
    log_info "Copying backup from k3d node: ${k3d_node}"
    
    # Create local directory
    mkdir -p "${export_dir}"
    
    # Copy from k3d node to host
    docker cp "${k3d_node}:${backup_path_in_node}/local" "${export_dir}/" 2>/dev/null || {
        log_error "Failed to copy backup from k3d node"
        log_info "Check: docker exec ${k3d_node} ls -lh ${backup_path_in_node}"
        return 1
    }
    
    # Verify backup
    if [ -d "${export_dir}/local/backups/${BACKUP_NAME}" ]; then
        local size=$(du -sh "${export_dir}/local/backups/${BACKUP_NAME}" 2>/dev/null | cut -f1)
        log_success "Backup exported to: ${export_dir}/local/backups/${BACKUP_NAME}"
        log_info "Backup size: ${size}"
    else
        log_error "Backup not found after export"
        return 1
    fi
}

# Verify backup
verify_backup() {
    log_step "Verifying backup..."
    
    # Get backup details
    log_info "Backup details:"
    velero backup describe "${BACKUP_NAME}" --details || {
        log_error "Failed to get backup details"
        return 1
    }
    
    # Check backup status
    local phase_line=$(velero backup describe "${BACKUP_NAME}" 2>/dev/null | grep "^Phase:")
    
    if echo "$phase_line" | grep -q "Completed"; then
        log_success "Backup phase: Completed"
    else
        log_error "Backup did not complete successfully"
        log_info "Phase: ${phase_line}"
        velero backup logs "${BACKUP_NAME}" 2>/dev/null | grep -i error || true
        return 1
    fi
    
    # Get backup statistics
    local items_backed_up=$(velero backup describe "${BACKUP_NAME}" 2>/dev/null | \
        grep "Total items to be backed up:" | awk '{print $NF}')
    local items_completed=$(velero backup describe "${BACKUP_NAME}" 2>/dev/null | \
        grep "Items backed up:" | awk '{print $NF}')
    
    log_info "Statistics:"
    log_info "  Total items: ${items_backed_up:-unknown}"
    log_info "  Backed up: ${items_completed:-unknown}"
    
    # Check for warnings
    local warnings=$(velero backup describe "${BACKUP_NAME}" 2>/dev/null | \
        grep "Warnings:" | awk '{print $NF}')
    
    if [ -n "$warnings" ] && [ "$warnings" != "0" ]; then
        log_warning "Backup completed with ${warnings} warnings"
        log_info "View warnings: velero backup logs ${BACKUP_NAME}"
    fi
    
    log_success "Backup verified successfully"
}

# Show backup summary
show_summary() {
    log_step "Backup Summary"
    
    echo -e "${BOLD}Backup completed successfully!${RESET}\n"
    echo -e "${CYAN}Backup Name:${RESET} ${BACKUP_NAME}"
    echo -e "${CYAN}Cluster:${RESET} ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    echo -e "${CYAN}Namespaces:${RESET} ${BACKUP_NAMESPACES}"
    
    # Get backup details
    local backup_info=$(velero backup describe "${BACKUP_NAME}" 2>/dev/null || echo "")
    
    if [ -n "$backup_info" ]; then
        local started=$(echo "$backup_info" | grep "Started:" | cut -d: -f2- | xargs)
        local completed=$(echo "$backup_info" | grep "Completed:" | cut -d: -f2- | xargs)
        local expiration=$(echo "$backup_info" | grep "Expiration:" | cut -d: -f2- | xargs)
        
        echo -e "${CYAN}Started:${RESET} ${started}"
        echo -e "${CYAN}Completed:${RESET} ${completed}"
        echo -e "${CYAN}Expires:${RESET} ${expiration}"
    fi
    
    echo -e "\n${GREEN}${BOLD}What was backed up:${RESET}"
    echo -e "  ✓ All OpenChoreo custom resources (components, projects, organizations, etc.)"
    echo -e "  ✓ All Kubernetes resources in selected namespaces"
    echo -e "  ✓ Persistent volumes (via Velero node-agent)"
    echo -e "  ✓ Secrets and ConfigMaps"
    
    echo -e "\n${CYAN}${BOLD}Next Steps:${RESET}"
    echo -e "  • View backup details:   ${YELLOW}velero backup describe ${BACKUP_NAME}${RESET}"
    echo -e "  • View backup logs:      ${YELLOW}velero backup logs ${BACKUP_NAME}${RESET}"
    echo -e "  • List all backups:      ${YELLOW}velero backup get${RESET}"
    echo -e "  • Test restore:          ${YELLOW}./velero_restore.sh --backup ${BACKUP_NAME} --dry-run${RESET}"
    echo -e "  • Restore to cluster:    ${YELLOW}./velero_restore.sh --backup ${BACKUP_NAME}${RESET}"
    
    echo ""
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Create a complete backup of OpenChoreo using Velero.

Options:
  --name NAME                  Backup name [default: openchoreo-YYYYMMDD-HHMMSS]
  --cluster-name NAME          Cluster name [default: openchoreo]
  --control-plane-only         Backup only Control Plane
  --data-plane-only            Backup only Data Plane
  --build-plane-only           Backup only Build Plane
  --observability-only         Backup only Observability Plane
  --no-volumes                 Skip volume backups (faster)
  --no-wait                    Don't wait for backup completion
  --ttl DURATION               Backup retention time [default: 720h]
  --help, -h                   Show this help message

Environment Variables:
  CLUSTER_NAME                 Cluster name [default: openchoreo]
  BACKUP_NAME                  Custom backup name
  BACKUP_TTL                   Backup retention time [default: 720h]

Examples:
  # Full backup (default)
  $0
  
  # Backup with custom name
  $0 --name my-backup
  
  # Backup only Control Plane
  $0 --control-plane-only
  
  # Quick backup without volumes
  $0 --no-volumes
  
  # Backup with longer retention (90 days)
  $0 --ttl 2160h

EOF
}

# Parse command line arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --name)
                BACKUP_NAME="$2"
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
            --no-volumes)
                BACKUP_VOLUMES=false
                shift
                ;;
            --no-wait)
                WAIT_FOR_COMPLETION=false
                shift
                ;;
            --ttl)
                BACKUP_TTL="$2"
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
    echo -e "${BOLD}Velero Backup Script${RESET}\n"
    
    parse_args "$@"
    build_namespace_list
    
    # Display configuration
    log_info "Backup Configuration:"
    log_info "  Backup Name: ${BACKUP_NAME}"
    log_info "  Cluster: ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    log_info "  Namespaces: ${BACKUP_NAMESPACES}"
    log_info "  Include Volumes: ${BACKUP_VOLUMES}"
    log_info "  Retention (TTL): ${BACKUP_TTL}"
    echo ""
    
    # Execute backup
    validate_prerequisites
    pre_backup_validation
    count_resources
    create_backup
    wait_for_backup
    export_backup
    verify_backup
    show_summary
}

# Run main function
main "$@"

