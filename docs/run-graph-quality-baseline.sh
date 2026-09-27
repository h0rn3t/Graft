#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <output-directory>" >&2
  exit 2
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "this raw-measurement script requires macOS /usr/bin/time -l -p" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
output_dir="$(mkdir -p "$1" && cd "$1" && pwd)"
input_commit="ec2c1aa2089bdc7a6f47f8f1191b0d381069327e"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

export GOPROXY=off GOSUMDB=off GRAFT_NO_LSP=1
(cd "$repo_root" && go build -o "$scratch/graft" ./cmd/graft)
(cd "$repo_root" && go build -o "$scratch/graph-quality" ./cmd/graph-quality)

mkdir -p "$scratch/archive"
git -C "$repo_root" archive "$input_commit" | tar -x -C "$scratch/archive"
printf '%s\n' "$input_commit" > "$output_dir/input-commit.txt"
printf 'graft %s\ngraph-quality %s\n' \
  "$(shasum -a 256 "$scratch/graft" | awk '{print $1}')" \
  "$(shasum -a 256 "$scratch/graph-quality" | awk '{print $1}')" \
  > "$output_dir/binary-sha256.txt"
for file in "$repo_root"/internal/graphquality/testdata/oracle-go/*.go; do
  printf '%s %s\n' "$(basename "$file")" "$(shasum -a 256 "$file" | awk '{print $1}')"
done > "$output_dir/fixture-sha256.txt"
go version > "$output_dir/go-version.txt"
uname -a > "$output_dir/host.txt"

"$scratch/graph-quality" "$repo_root/internal/graphquality/testdata/oracle-go" \
  --oracle "$repo_root/internal/graphquality/testdata/oracle-go/oracle.json" --json \
  > "$output_dir/oracle.json"

printf 'kind,sample,real_seconds,max_rss_bytes,stdout_bytes,stdout_sha256\n' > "$output_dir/samples.csv"
measure() {
  local kind="$1" sample="$2" cwd="$3"
  shift 3
  local output="$scratch/${kind}-${sample}.stdout"
  local timing="$scratch/time.txt"
  (cd "$cwd" && /usr/bin/time -l -p "$@" > "$output" 2> "$timing")
  local seconds rss bytes digest
  seconds="$(awk '$1 == "real" {print $2}' "$timing")"
  rss="$(awk '/maximum resident set size/ {print $1}' "$timing")"
  bytes="$(wc -c < "$output" | tr -d ' ')"
  digest="$(shasum -a 256 "$output" | awk '{print $1}')"
  printf '%s,%s,%s,%s,%s,%s\n' "$kind" "$sample" "$seconds" "$rss" "$bytes" "$digest" >> "$output_dir/samples.csv"
}

for sample in 1 2 3; do
  cp -R "$scratch/archive" "$scratch/build-$sample"
  measure build "$sample" "$scratch/build-$sample" "$scratch/graft" build
done

cp -R "$scratch/archive" "$scratch/query"
(cd "$scratch/query" && "$scratch/graft" build > "$scratch/query-build.txt" 2>&1)
shasum -a 256 "$scratch/query/graft/.graph/wiring.json" | awk '{print $1}' > "$output_dir/query-graph-sha256.txt"
for sample in 1 2 3 4 5; do
  measure ask "$sample" "$scratch/query" "$scratch/graft" ask "graph quality report" --source --no-refresh
  measure read "$sample" "$scratch/query" "$scratch/graft" read "internal/graphquality/report.go::Analyze" --no-refresh
done
