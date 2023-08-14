#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2023-08-14 09:25:50

########################################################################################################################################################################################################################

WEED_MASTER=${WEED_MASTER:="cache_server:9334"}
WEED_FILER=${WEED_FILER:="cache_filer:8889"}
WEED_KEEP_DAYS=${WEED_KEEP_DAYS:=2}

echo "fs.du /topics/.system/log;" | /usr/bin/weed shell -master=${WEED_MASTER}

for row in $(curl -s -H "Accept: application/json" "http://${WEED_FILER}/topics/.system/log?lastFileName=&limit=1000" | jq '.Entries[] | select(now - (.Crtime | fromdate) > (86400 * $keep_days)) | { "path" : .FullPath } | @base64' -c -M -r --argjson keep_days ${WEED_KEEP_DAYS}); do
  _jq() {
    echo ${row} | base64 -d | jq -r ${1}
  }
  curl -X DELETE "http://${WEED_FILER}$(_jq '.path')?recursive=true&ignoreRecursiveError=true"
done

echo "fs.du /topics/.system/log;" | /usr/bin/weed shell -master=${WEED_MASTER}

exit 0

########################################################################################################################################################################################################################
