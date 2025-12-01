#!/usr/bin/env bash

# OpenChoreo Velero Backup Verification Script
# Verifies Velero backups without performing a full restore

set -eo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-openchoreo}"
KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
BACKUP_NAME="${BACKUP_NAME:-}"
VELERO_NAMESPACE="${VELERO_NAMESPACE:-velero}"

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
    if ! command_exists velero; then
        log_error "Velero CLI not found"
        exit 1
    fi
    
    if ! command_exists kubectl; then
        log_error "kubectl not found"
        exit 1
    fi
    
    # Check cluster access
    if ! kubectl --context="${KUBE_CONTEXT}" cluster-info >/dev/null 2>&1; then
        log_error "Cannot access cluster '${CLUSTER_NAME}'"
        exit 1
    fi
}

# List all backups
list_backups() {
    log_step "Available Backups"
    
    velero backup get --context="${KUBE_CONTEXT}" || {
        log_error "Failed to list backups"
        return 1
    }
}

# Verify specific backup
verify_backup() {
    local backup="$1"
    
    log_step "Verifying Backup: ${backup}"
    
    # Get backup details
    log_info "Backup Details:"
    velero backup describe "${backup}" --context="${KUBE_CONTEXT}" || {
        log_error "Failed to describe backup"
        return 1
    }
    
    echo ""
    
    # Check backup status
    local status=$(velero backup describe "${backup}" --context="${KUBE_CONTEXT}" 2>/dev/null | \
        grep "Phase:" | awk '{print $2}')
    
    if [ "$status" != "Completed" ]; then
        log_error "Backup is not in 'Completed' state: ${status}"
        return 1
    fi
    log_success "Backup status: ${status}"
    
    # Check for errors
    local errors=$(velero backup describe "${backup}" --context="${KUBE_CONTEXT}" 2>/dev/null | \
        grep "Errors:" | awk '{print $NF}')
    
    if [ -n "$errors" ] && [ "$errors" != "0" ]; then
        log_error "Backup has ${errors} errors"
        log_info "View errors: velero backup logs ${backup}"
        return 1
    fi
    log_success "No errors in backup"
    
    # Check for warnings
    local warnings=$(velero backup describe "${backup}" --context="${KUBE_CONTEXT}" 2>/dev/null | \
        grep "Warnings:" | awk '{print $NF}')
    
    if [ -n "$warnings" ] && [ "$warnings" != "0" ]; then
        log_warning "Backup has ${warnings} warnings"
        log_info "View warnings: velero backup logs ${backup}"
    else
        log_success "No warnings in backup"
    fi
    
    # Check resource counts
    log_info ""
    log_info "Resource Statistics:"
    velero backup describe "${backup}" --details --context="${KUBE_CONTEXT}" | \
        grep -E "(Total items|Items backed up|Kubernetes version)" || true
    
    # Verify critical OpenChoreo resources
    log_info ""
    log_info "Checking for critical OpenChoreo resources..."
    
    local has_orgs=$(velero backup describe "${backup}" --details --context="${KUBE_CONTEXT}" | \
        grep -c "openchoreo.dev/v1alpha1/Organization" || echo 0)
    local has_projects=$(velero backup describe "${backup}" --details --context="${KUBE_CONTEXT}" | \
        grep -c "openchoreo.dev/v1alpha1/Project" || echo 0)
    local has_components=$(velero backup describe "${backup}" --details --context="${KUBE_CONTEXT}" | \
        grep -c "openchoreo.dev/v1alpha1/Component" || echo 0)
    local has_workflows=$(velero backup describe "${backup}" --details --context="${KUBE_CONTEXT}" | \
        grep -c "openchoreo.dev/v1alpha1/ComponentWorkflow" || echo 0)
    
    if [ "$has_orgs" -gt 0 ]; then
        log_success "✓ Contains Organizations"
    else
        log_warning "✗ No Organizations found"
    fi
    
    if [ "$has_projects" -gt 0 ]; then
        log_success "✓ Contains Projects"
    else
        log_warning "✗ No Projects found"
    fi
    
    if [ "$has_components" -gt 0 ]; then
        log_success "✓ Contains Components"
    else
        log_warning "✗ No Components found"
    fi
    
    if [ "$has_workflows" -gt 0 ]; then
        log_success "✓ Contains ComponentWorkflows"
    else
        log_warning "✗ No ComponentWorkflows found"
    fi
    
    # Check volumes
    log_info ""
    log_info "Checking persistent volumes..."
    
    local pv_count=$(velero backup describe "${backup}" --details --context="${KUBE_CONTEXT}" | \
        grep -c "v1/PersistentVolumeClaim" || echo 0)
    
    if [ "$pv_count" -gt 0 ]; then
        log_success "✓ Contains ${pv_count} PersistentVolumeClaims"
    else
        log_warning "✗ No PersistentVolumeClaims found"
    fi
    
    log_success "Backup verification complete"
}

# Verify all recent backups
verify_all_backups() {
    log_step "Verifying All Backups"
    
    local backups=$(velero backup get --context="${KUBE_CONTEXT}" -o name 2>/dev/null | \
        grep "backup.velero.io/" | sed 's|backup.velero.io/||' || echo "")
    
    if [ -z "$backups" ]; then
        log_warning "No backups found"
        return 0
    fi
    
    local total=0
    local passed=0
    local failed=0
    
    while IFS= read -r backup; do
        [ -z "$backup" ] && continue
        
        total=$((total + 1))
        echo ""
        log_info "Verifying backup ${total}: ${backup}"
        
        local status=$(velero backup describe "${backup}" --context="${KUBE_CONTEXT}" 2>/dev/null | \
            grep "Phase:" | awk '{print $2}')
        
        if [ "$status" = "Completed" ]; then
            log_success "✓ ${backup}: ${status}"
            passed=$((passed + 1))
        else
            log_error "✗ ${backup}: ${status}"
            failed=$((failed + 1))
        fi
    done <<< "$backups"
    
    echo ""
    log_info "Summary: ${passed}/${total} backups completed successfully"
    
    if [ "$failed" -gt 0 ]; then
        log_warning "${failed} backups failed or incomplete"
        return 1
    fi
}

# Check backup schedule status
check_schedules() {
    log_step "Checking Backup Schedules"
    
    velero schedule get --context="${KUBE_CONTEXT}" || {
        log_warning "No backup schedules configured"
        return 0
    }
    
    echo ""
    log_info "Schedule Details:"
    
    local schedules=$(velero schedule get --context="${KUBE_CONTEXT}" -o name 2>/dev/null | \
        grep "schedule.velero.io/" | sed 's|schedule.velero.io/||' || echo "")
    
    if [ -z "$schedules" ]; then
        log_warning "No schedules found"
        return 0
    fi
    
    while IFS= read -r schedule; do
        [ -z "$schedule" ] && continue
        
        echo ""
        log_info "Schedule: ${schedule}"
        velero schedule describe "${schedule}" --context="${KUBE_CONTEXT}" | \
            grep -E "(Phase|Schedule|Last Backup)" || true
    done <<< "$schedules"
}

# Show backup storage location status
check_storage_location() {
    log_step "Checking Backup Storage Location"
    
    velero backup-location get --context="${KUBE_CONTEXT}" || {
        log_error "No backup storage location configured"
        return 1
    }
    
    echo ""
    
    local status=$(velero backup-location get --context="${KUBE_CONTEXT}" -o json 2>/dev/null | \
        grep -oP '"phase":\s*"\K[^"]+' | head -1)
    
    if [ "$status" = "Available" ]; then
        log_success "Backup storage is available"
    else
        log_error "Backup storage is not available: ${status}"
        return 1
    fi
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Verify Velero backups without performing a full restore.

Options:
  --backup NAME                Verify specific backup
  --all                        Verify all backups
  --list                       List all backups
  --schedules                  Check backup schedules
  --storage                    Check backup storage location
  --cluster-name NAME          Cluster name [default: openchoreo]
  --help, -h                   Show this help message

Examples:
  # List all backups
  $0 --list
  
  # Verify specific backup
  $0 --backup openchoreo-20241201-120000
  
  # Verify all backups
  $0 --all
  
  # Check backup schedules
  $0 --schedules
  
  # Check backup storage
  $0 --storage
  
  # Full verification
  $0 --all --schedules --storage

EOF
}

# Parse command line arguments
parse_args() {
    local action="list"
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            --backup)
                BACKUP_NAME="$2"
                action="verify"
                shift 2
                ;;
            --all)
                action="verify-all"
                shift
                ;;
            --list)
                action="list"
                shift
                ;;
            --schedules)
                action="schedules"
                shift
                ;;
            --storage)
                action="storage"
                shift
                ;;
            --cluster-name)
                CLUSTER_NAME="$2"
                KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
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
    
    # Execute action
    case $action in
        list)
            list_backups
            ;;
        verify)
            if [ -z "$BACKUP_NAME" ]; then
                log_error "No backup specified"
                exit 1
            fi
            verify_backup "$BACKUP_NAME"
            ;;
        verify-all)
            verify_all_backups
            ;;
        schedules)
            check_schedules
            ;;
        storage)
            check_storage_location
            ;;
    esac
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
    echo -e "${BOLD}Velero Backup Verification Script${RESET}\n"
    
    validate_prerequisites
    
    if [ $# -eq 0 ]; then
        list_backups
    else
        parse_args "$@"
    fi
}

# Run main function
main "$@"

