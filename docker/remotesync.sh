#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2024-07-01 18:18:44

########################################################################################################################################################################################################################

WEED_SYNC_TYPE=${WEED_SYNC_TYPE:="remote"}
WEED_MASTER=${WEED_MASTER:="cloud_server:9333"}
WEED_FILER=${WEED_FILER:="cloud_filer:8888"}

if [[ "${WEED_SYNC_TYPE}" == "remote" ]]; then
  echo "***"
  echo "Executing remote to local sync on ${WEED_FILER}"

  conf_file="$1"
  while IFS= read -r line; do
    [[ $line = \#* ]] && continue

    echo "Executing remote to local sync for directory '${line}'"
    echo "remote.meta.sync -dir=${line};" | /usr/bin/weed shell -master=${WEED_MASTER} -filer=${WEED_FILER}
  done < "$conf_file"

  echo "***"

  exit 0
elif [[ "${WEED_SYNC_TYPE}" == "local" ]]; then
  echo "***"
  echo "Executing local to remote sync on ${WEED_FILER}"

  conf_file="$1"
  while IFS= read -r line; do
    [[ $line = \#* ]] && continue

    echo "Starting local to remote sync for directory '${line}'"
    /usr/bin/weed filer.remote.sync -filer=${WEED_FILER} -dir=${line} &
  done < "$conf_file"

  echo "***"

  wait

  exit 0
else
  echo "Invalid type ${WEED_SYNC_TYPE}"
fi

########################################################################################################################################################################################################################
