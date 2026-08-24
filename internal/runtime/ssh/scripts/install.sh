set -eu
packages=""
for tool in "$@"; do
  case "$tool" in
    git|tmux) packages="$packages $tool" ;;
    *) printf 'unsupported tool: %s\n' "$tool" >&2; exit 64 ;;
  esac
done
if [ -n "$packages" ]; then
  sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a APT_LISTCHANGES_FRONTEND=none \
    apt-get -o DPkg::Lock::Timeout=120 -o Acquire::Retries=3 update
  # shellcheck disable=SC2086 -- values are allow-listed above.
  sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a APT_LISTCHANGES_FRONTEND=none \
    apt-get -o DPkg::Lock::Timeout=120 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confold install -y $packages
fi
printf '{"schema_version":"1","installed":true}\n'

