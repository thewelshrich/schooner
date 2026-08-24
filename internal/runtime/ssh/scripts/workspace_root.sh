set -eu
requested=$1
home_dir=$2
case "$requested" in
  "~") target=$home_dir ;;
  "~/"*) target="$home_dir/${requested#\~/}" ;;
  /*) target=$requested ;;
  *) printf 'workspace root must be absolute or begin with ~/\n' >&2; exit 64 ;;
esac
mkdir -p "$target"
resolved=$(cd "$target" && pwd -P)
escaped=$(printf '%s' "$resolved" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf '{"schema_version":"1","workspace_root":"%s"}\n' "$escaped"

