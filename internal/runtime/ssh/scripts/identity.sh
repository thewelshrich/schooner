set -eu
candidate=$1
home_dir=$2
state_dir="$home_dir/.local/state/schooner"
identity_file="$state_dir/identity"
umask 077
mkdir -p "$state_dir"
chmod 700 "$state_dir"
if [ ! -f "$identity_file" ]; then
  (set -C; printf '%s\n' "$candidate" > "$identity_file") 2>/dev/null || true
  chmod 600 "$identity_file"
fi
identity=$(sed -n '1p' "$identity_file")
printf '{"schema_version":"1","remote_identity":"%s"}\n' "$identity"
