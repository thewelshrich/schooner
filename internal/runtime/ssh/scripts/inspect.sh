set -eu

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/\t/\\t/g; s/\r/\\r/g'
}

os_id=$(sed -n 's/^ID=//p' /etc/os-release | head -n1 | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | head -n1 | tr -d '"')
machine_arch=$(uname -m)
case "$machine_arch" in
  x86_64) architecture=amd64 ;;
  aarch64|arm64) architecture=arm64 ;;
  *) architecture=$machine_arch ;;
esac
remote_home=$(getent passwd "$(id -u)" | cut -d: -f6)
if [ -z "$remote_home" ]; then remote_home=$(cd ~ && pwd -P); fi
requested_root=${1:-~}
case "$requested_root" in
  "~") project_root=$remote_home ;;
  "~/"*) project_root="$remote_home/${requested_root#\~/}" ;;
  /*) project_root=$requested_root ;;
  *) project_root=$requested_root ;;
esac
project_root_exists=false
if [ -d "$project_root" ]; then project_root=$(cd "$project_root" && pwd -P); project_root_exists=true; fi
identity_file="$remote_home/.local/state/schooner/identity"
remote_identity=""
if [ -f "$identity_file" ]; then remote_identity=$(sed -n '1p' "$identity_file"); fi

git_available=false
git_version=""
if command -v git >/dev/null 2>&1; then git_available=true; git_version=$(git --version | head -n1); fi
tmux_available=false
tmux_version=""
if command -v tmux >/dev/null 2>&1; then tmux_available=true; tmux_version=$(tmux -V | head -n1); fi
passwordless_sudo=false
if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then passwordless_sudo=true; fi

printf '{"schema_version":"1","os_id":"%s","os_version":"%s","architecture":"%s","home":"%s","remote_identity":"%s","project_root":"%s","project_root_exists":%s,"git":{"available":%s,"version":"%s"},"tmux":{"available":%s,"version":"%s"},"passwordless_sudo":%s}\n' \
  "$(json_escape "$os_id")" "$(json_escape "$os_version")" "$(json_escape "$architecture")" \
  "$(json_escape "$remote_home")" "$(json_escape "$remote_identity")" "$(json_escape "$project_root")" "$project_root_exists" \
  "$git_available" "$(json_escape "$git_version")" "$tmux_available" "$(json_escape "$tmux_version")" "$passwordless_sudo"
