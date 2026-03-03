#!/bin/bash
set -eu -o pipefail

token="${GITHUB_TOKEN:?Set GITHUB_TOKEN before running this script}"

clone_or_pull() {
        local page="$1"
        local tmpfile
        tmpfile=$(mktemp)
        trap "rm -f '$tmpfile'" RETURN

        curl -sfL \
                -H "Accept: application/vnd.github+json" \
                -H "Authorization: Bearer $token" \
                -H "X-GitHub-Api-Version: 2022-11-28" \
                "https://api.github.com/gists?per_page=100&page=$page" >"$tmpfile"

        local id url
        while IFS=$'\t' read -r id url; do
                if [ -d "$id" ]; then
                        git -C "$id" pull --quiet
                else
                        git clone --quiet "$url"
                fi
        done < <(jq -r '.[] | [.id, .git_pull_url] | @tsv' "$tmpfile")

        jq length "$tmpfile"
}

page=1
count=0
while true; do
        length=$(clone_or_pull "$page")
        count=$((count + length))
        if [ "$length" -lt 100 ]; then
                break
        fi
        page=$((page + 1))
done
echo "total $count gists"
