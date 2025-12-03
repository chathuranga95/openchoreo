#!/usr/bin/env bash

# OpenChoreo Velero Installation Script - Local k3d Version
# Simplified for k3d clusters running on Ubuntu VM with local storage

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
VELERO_NAMESPACE="${VELERO_NAMESPACE:-velero}"
VELERO_VERSION="${VELERO_VERSION:-v1.17.1}"

# Local storage configuration
BACKUP_DIR="${BACKUP_DIR:-$(dirname "${REPO_ROOT}")/openchoreo-backups}"

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

# Check if Velero CLI is installed
check_velero_cli() {
    log_step "Checking Velero CLI..."
    
    if command_exists velero; then
        VELERO_CLI_VERSION=$(velero version --client-only 2>/dev/null | grep -oP 'Version: \K[^\s]+' || echo "unknown")
        log_success "Velero CLI already installed (${VELERO_CLI_VERSION})"
        return 0
    fi
    
    log_warning "Velero CLI not found. Installing..."
    install_velero_cli
}

# Install Velero CLI
install_velero_cli() {
    log_info "Installing Velero CLI ${VELERO_VERSION}..."
    
    # Detect OS and architecture
    local os=$(uname -s | tr '[:upper:]' '[:lower:]')
    local arch=$(uname -m)
    
    case $arch in
        x86_64)
            arch="amd64"
            ;;
        aarch64|arm64)
            arch="arm64"
            ;;
        *)
            log_error "Unsupported architecture: $arch"
            exit 1
            ;;
    esac
    
    # Download Velero
    local download_url="https://github.com/vmware-tanzu/velero/releases/download/${VELERO_VERSION}/velero-${VELERO_VERSION}-${os}-${arch}.tar.gz"
    log_info "Downloading from: ${download_url}"
    
    local temp_dir=$(mktemp -d)
    cd "$temp_dir"
    
    if curl -sL "$download_url" | tar xz; then
        sudo install -o root -g root -m 0755 "velero-${VELERO_VERSION}-${os}-${arch}/velero" /usr/local/bin/velero
        log_success "Velero CLI installed successfully"
    else
        log_error "Failed to download Velero CLI"
        rm -rf "$temp_dir"
        exit 1
    fi
    
    rm -rf "$temp_dir"
    
    # Verify installation
    velero version --client-only
}

# Create local backup directory
create_backup_directory() {
    log_step "Setting up local backup storage..."
    
    # Create backup directory on host
    if [ ! -d "$BACKUP_DIR" ]; then
        log_info "Creating backup directory: $BACKUP_DIR"
        mkdir -p "$BACKUP_DIR"
    else
        log_info "Backup directory already exists: $BACKUP_DIR"
    fi
    
    # Set permissions
    chmod 755 "$BACKUP_DIR"
    
    log_success "Backup directory ready: $BACKUP_DIR"
}

# Create PersistentVolume for backups
create_backup_pv() {
    log_step "Creating PersistentVolume for backups..."
    
    # Create namespace if it doesn't exist
    kubectl --context="${KUBE_CONTEXT}" create namespace "${VELERO_NAMESPACE}" 2>/dev/null || true
    
    # Create PV that points to local directory
    cat <<EOF | kubectl --context="${KUBE_CONTEXT}" apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: velero-local-backup-pv
spec:
  capacity:
    storage: 100Gi
  accessModes:
    - ReadWriteOnce
  persistentVolumeReclaimPolicy: Retain
  storageClassName: velero-local-storage
  hostPath:
    path: ${BACKUP_DIR}
    type: DirectoryOrCreate
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: velero-local-backup-pvc
  namespace: ${VELERO_NAMESPACE}
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: velero-local-storage
  resources:
    requests:
      storage: 100Gi
EOF
    
    log_success "PersistentVolume created"
}

# Install Velero with local provider
install_velero_local() {
    log_step "Installing Velero with local storage provider..."
    
    # Check if Velero is already installed
    if kubectl --context="${KUBE_CONTEXT}" get namespace "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        if kubectl --context="${KUBE_CONTEXT}" get deployment -n "${VELERO_NAMESPACE}" velero >/dev/null 2>&1; then
            log_warning "Velero is already installed"
            read -p "Do you want to reinstall? (yes/no): " -r
            if [[ ! $REPLY =~ ^[Yy][Ee][Ss]$ ]]; then
                log_info "Skipping installation"
                return 0
            fi
            
            log_info "Uninstalling existing Velero..."
            velero uninstall --force --context="${KUBE_CONTEXT}" || true
            kubectl --context="${KUBE_CONTEXT}" delete namespace "${VELERO_NAMESPACE}" --force --grace-period=0 2>/dev/null || true
            sleep 10
        fi
    fi
    
    # Create namespace
    kubectl --context="${KUBE_CONTEXT}" create namespace "${VELERO_NAMESPACE}" 2>/dev/null || true
    
    # Install Velero with local configuration
    log_info "Installing Velero components..."
    
    # We'll use a custom configuration for local storage
    # This uses hostPath volumes instead of cloud object storage
    
    # Install Velero with local configuration
    velero install \
        --provider aws \
        --plugins velero/velero-plugin-for-aws:v1.8.0 \
        --bucket local \
        --use-node-agent \
        --use-volume-snapshots=false \
        --backup-location-config region=default,s3ForcePathStyle=true,s3Url=http://minio.velero.svc:9000 \
        --no-secret \
        --kubecontext "${KUBE_CONTEXT}"
    
    # Wait a bit for Velero to start
    sleep 5
    
    # Deploy MinIO for local S3-compatible storage
    deploy_local_minio
    
    # Wait for Velero to be ready
    log_info "Waiting for Velero to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Available \
        deployment/velero -n "${VELERO_NAMESPACE}" --timeout=300s || {
        log_warning "Velero may not be fully ready yet, continuing..."
    }
    
    log_success "Velero installed"
}

# Deploy MinIO for local S3-compatible storage
deploy_local_minio() {
    log_step "Deploying MinIO for local backup storage..."
    
    # Create MinIO deployment
    cat <<EOF | kubectl --context="${KUBE_CONTEXT}" apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: ${VELERO_NAMESPACE}
type: Opaque
stringData:
  cloud: |
    [default]
    aws_access_key_id = minio
    aws_secret_access_key = minio123
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minio
  namespace: ${VELERO_NAMESPACE}
spec:
  selector:
    matchLabels:
      app: minio
  template:
    metadata:
      labels:
        app: minio
    spec:
      containers:
      - name: minio
        image: minio/minio:latest
        args:
        - server
        - /storage
        env:
        - name: MINIO_ROOT_USER
          value: "minio"
        - name: MINIO_ROOT_PASSWORD
          value: "minio123"
        ports:
        - containerPort: 9000
        volumeMounts:
        - name: storage
          mountPath: /storage
      volumes:
      - name: storage
        hostPath:
          path: ${BACKUP_DIR}
          type: DirectoryOrCreate
---
apiVersion: v1
kind: Service
metadata:
  name: minio
  namespace: ${VELERO_NAMESPACE}
spec:
  type: ClusterIP
  ports:
  - port: 9000
    targetPort: 9000
  selector:
    app: minio
---
apiVersion: batch/v1
kind: Job
metadata:
  name: minio-setup
  namespace: ${VELERO_NAMESPACE}
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
      - name: mc
        image: minio/mc:latest
        command:
        - /bin/sh
        - -c
        - |
          sleep 10
          mc alias set local http://minio:9000 minio minio123
          mc mb --ignore-existing local/local
          echo "MinIO bucket 'local' created successfully"
EOF
    
    # Wait for MinIO to be ready
    log_info "Waiting for MinIO to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Available \
        deployment/minio -n "${VELERO_NAMESPACE}" --timeout=300s || {
        log_warning "MinIO may not be fully ready yet"
    }
    
    # Wait for setup job to complete
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=complete \
        job/minio-setup -n "${VELERO_NAMESPACE}" --timeout=120s 2>/dev/null || {
        log_warning "MinIO setup job may still be running"
    }
    
    # Update Velero backup location with credentials
    log_info "Configuring Velero backup location..."
    
    kubectl --context="${KUBE_CONTEXT}" patch backupstoragelocation default \
        -n "${VELERO_NAMESPACE}" \
        --type merge \
        -p "{\"spec\":{\"credential\":{\"name\":\"minio-credentials\",\"key\":\"cloud\"}}}"
    
    log_success "MinIO deployed and configured"
}

# Verify Velero installation
verify_installation() {
    log_step "Verifying Velero installation..."
    
    # Check Velero deployment
    if ! kubectl --context="${KUBE_CONTEXT}" get deployment velero -n "${VELERO_NAMESPACE}" >/dev/null 2>&1; then
        log_error "Velero deployment not found"
        return 1
    fi
    
    # Check Velero status
    log_info "Velero version:"
    velero version --context="${KUBE_CONTEXT}" || {
        log_warning "Velero server may not be fully ready yet"
    }
    
    # Check backup location
    log_info "Backup storage location:"
    velero backup-location get --context="${KUBE_CONTEXT}" || {
        log_warning "Backup location not ready yet, may need a moment"
    }
    
    # Check node agent (restic)
    log_info "Node agent pods:"
    kubectl --context="${KUBE_CONTEXT}" get pods -n "${VELERO_NAMESPACE}" -l name=node-agent || {
        log_warning "Node agent pods not found"
    }
    
    log_success "Velero installation verified"
}


# Create initial backup schedule
create_backup_schedule() {
    log_step "Creating backup schedule..."
    
    read -p "Do you want to create a daily backup schedule? (yes/no): " -r
    if [[ ! $REPLY =~ ^[Yy][Ee][Ss]$ ]]; then
        log_info "Skipping backup schedule creation"
        return 0
    fi
    
    local schedule_name="openchoreo-daily"
    local schedule_time="0 2 * * *"  # 2 AM daily
    local retention_hours="720"  # 30 days
    
    log_info "Creating daily backup schedule..."
    log_info "  Schedule: ${schedule_time} (2 AM daily)"
    log_info "  Retention: ${retention_hours} hours (30 days)"
    
    velero schedule create "${schedule_name}" \
        --schedule="${schedule_time}" \
        --include-namespaces="openchoreo-control-plane,openchoreo-data-plane,openchoreo-build-plane,openchoreo-observability-plane" \
        --default-volumes-to-fs-backup=true \
        --ttl="${retention_hours}h" \
        --context="${KUBE_CONTEXT}" || {
        log_warning "Failed to create schedule, but you can create it later"
        return 0
    }
    
    log_success "Backup schedule created: ${schedule_name}"
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Install Velero for OpenChoreo backup/restore on local k3d cluster.
Uses local filesystem storage - no cloud credentials needed.

Options:
  --backup-dir DIR             Local backup directory [default: ../openchoreo-backups]
  --cluster-name NAME          Cluster name [default: openchoreo]
  --help, -h                   Show this help message

Environment Variables:
  BACKUP_DIR                   Local backup directory
  CLUSTER_NAME                 Cluster name

Examples:
  # Install with defaults (backups to ../openchoreo-backups)
  $0
  
  # Install with custom backup location
  $0 --backup-dir /mnt/backups/openchoreo
  
  # Install for different cluster
  $0 --cluster-name my-cluster

EOF
}

# Parse command line arguments
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

# Main installation flow
main() {
    echo -e "${CYAN}${BOLD}"
    cat << 'EOF'
   ___                   ____  _                            
  / _ \ _ __   ___ _ __ / ___ | |__   ___  _ __  ___  ___  
 | | | | '_ \ / _ \ '_ \| |   | '_ \ / _ \| '__// _ \/ _ \ 
 | |_| | |_) |  __/ | | | |___| | | | (_) | |  |  __/ (_) |
  \___/| .__/ \___|_| |_|\____|_| |_|\___/|_|   \___|\___/ 
       |_|                                                  
EOF
    echo -e "${RESET}"
    echo -e "${BOLD}Velero Installation Script (Local k3d)${RESET}\n"
    
    parse_args "$@"
    
    # Display configuration
    log_info "Configuration:"
    log_info "  Cluster: ${CLUSTER_NAME} (${KUBE_CONTEXT})"
    log_info "  Backup Directory: ${BACKUP_DIR}"
    log_info "  Storage: Local filesystem (via MinIO)"
    echo ""
    
    # Execute installation
    validate_prerequisites
    check_velero_cli
    create_backup_directory
    install_velero_local
    verify_installation
    create_backup_schedule
    
    # Show next steps
    log_step "Installation Complete!"
    
    echo -e "${GREEN}${BOLD}Velero is ready for OpenChoreo backup/restore!${RESET}\n"
    
    echo -e "${CYAN}Backup Location:${RESET} ${BACKUP_DIR}"
    echo -e "${CYAN}Storage Type:${RESET} Local MinIO (S3-compatible)"
    
    echo -e "\n${CYAN}Next Steps:${RESET}"
    echo -e "  1. Create a backup:      ${YELLOW}../backup/velero_backup.sh${RESET}"
    echo -e "  2. View backups:         ${YELLOW}velero backup get${RESET}"
    echo -e "  3. View backup location: ${YELLOW}ls -lh ${BACKUP_DIR}${RESET}"
    echo -e "  4. Test restore:         ${YELLOW}../restore/velero_restore.sh --dry-run${RESET}"
    
    echo -e "\n${CYAN}Important Notes:${RESET}"
    echo -e "  • Backups are stored locally in: ${BACKUP_DIR}"
    echo -e "  • For VM backups, backup this directory externally"
    echo -e "  • For production, consider cloud storage (use velero_install.sh)"
    
    echo ""
}

# Run main function
main "$@"

