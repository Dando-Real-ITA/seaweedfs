#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2021-12-16 13:21:58

########################################################################################################################################################################################################################

sleep 10

# Read all commands
ARGS=""
IP=""
CIP=""
for ARG in $@; do

  if [[ ${DETECT_PEERS:-true} != "false" ]]; then
    # Detect a *peers command
    if [[ $ARG == *"peers="* ]]; then
      # TODO detect quotes after the =
      OPTION=$(expr "$ARG" : '\(.*=\)')
      VALUE=$(expr "$ARG" : '.*=\(.*\)')

      echo "Detected ${OPTION}"

      # Split all peers
      PEERS=""
      IFS=',' read -ra ADDRS <<< "$VALUE"
      for ADDR in "${ADDRS[@]}"; do
        # Get HOST:PORT
        HOST=$(expr "$ADDR" : '\(.*\):')
        PORT=$(expr "$ADDR" : '.*:\(.*\)')
        # Add directly IP peers
        if [[ $HOST =~ ^[0-9\.]+$ ]]; then
          PEERS=${PEERS:+${PEERS},}${HOST}:${PORT}
        else
          echo "Getting IPs for ${HOST}"

          # Get all tasks ips
          typeset -i nbt
          nbt=0
          SECONDS=0

          echo "Waiting for the min peers count (${CLUSTER_SIZE})"

          while [[ $nbt -lt ${CLUSTER_SIZE} ]]; do
            tips=$(dig @127.0.0.11 +short tasks.${HOST})
            nbt=$(echo $tips | wc -w)
            [[ $SECONDS -gt 120 ]] && break
            sleep 1
          done
          for tip in $tips; do
            echo "Adding peer: ${tip}"
            PEERS=${PEERS:+${PEERS},}${tip}:${PORT}

            cip=$(grep ${tip} /etc/hosts | awk '{print $1}' | head -1)
            if [[ -n "$cip" ]]; then
              echo "Found current ip: ${cip}"
              CIP="-ip=${cip}"
            fi
          done

          # TODO handle normal hostnames correctly, in case no service is found
          # # Get all direct ips
          # ips=$(dig @127.0.0.11 +short ${HOST})
          # for ip in $ips; do
          #   echo "Adding peer: ${ip}"
          #   PEERS=${PEERS:+${PEERS},}${ip}:${PORT}
          # done
        fi
      done

      ARG=${PEERS:+${OPTION}${PEERS}}

    # Detect an ip command
    elif [[ $ARG == *"ip="* ]]; then
      # Save the IP as a fallback
      IP=$ARG
      # Not writing the ip for now
      ARG=""
    fi
  fi

  ARGS=${ARGS:+${ARGS} }$ARG
done

if ! [ -z "$CIP" ]; then
  ARGS=${ARGS:+${ARGS} }$CIP
elif ! [ -z "$IP" ]; then
  ARGS=${ARGS:+${ARGS} }$IP
fi

# If there is a public url, add it to the parameters
if ! [ -z "$PUBLIC_URL" ]; then
  ARGS=${ARGS:+${ARGS} }"-publicUrl=$PUBLIC_URL"

  echo "Setting Host IP from Public Url"
  HOST=$(expr "$ADDR" : '\(.*\):')
  ARGS=${ARGS:+${ARGS} }"-ip=${HOST}"

  # If there is a tld, use it to extract rack and datacenter
  # gpu01.dal1.llnw.katapy.io with tld=katapy.io -> -rack=dal1 -dataCenter=llnw
  if ! [ -z "$TLD" ]; then
    IFS='.' read -a hostname_parts <<<${PUBLIC_URL%.${TLD}}
    ARGS=${ARGS:+${ARGS} }"-rack=${hostname_parts[1]} -dataCenter=${hostname_parts[2]}"
  fi

fi

exec /entrypoint.sh $ARGS

########################################################################################################################################################################################################################
