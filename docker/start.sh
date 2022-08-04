#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2022-08-04 18:33:37

########################################################################################################################################################################################################################

# Handling cleanup on stop signals
FINISH=0;
trap 'trap "FINISH=1" SIGTERM; kill 0; wait &> /dev/null' EXIT SIGINT SIGTERM SIGABRT

########################################################################################################################################################################################################################

# Give time to all servers to start and get an ip
sleep 10

# Read all commands
ARGS=""
IP=""
CIP=""

for ARG in $@; do
  if [[ ${DETECT_MASTERS:-true} != "false" ]]; then
    # Detect a -master command
    if [[ $ARG == "-master="* || $ARG == "-mserver="* ]]; then
      OPTION=$(expr "$ARG" : '\(.*=\)')
      VALUE=$(expr "$ARG" : '.*=\(.*\)')

      echo "Detected ${OPTION}"

      # Split all masters
      MASTERS=""
      IFS=',' read -ra ADDRS <<< "$VALUE"
      for ADDR in "${ADDRS[@]}"; do
        # Get HOST:PORT
        HOST=$(expr "$ADDR" : '\(.*\):')
        PORT=$(expr "$ADDR" : '.*:\(.*\)')

        if [[ $HOST =~ ^[0-9\.]+$ ]]; then
          MASTERS=${MASTERS:+${MASTERS},}${HOST}:${PORT}
        else
          echo "Getting Task IPs for ${HOST}"

          # Get all tasks ips
          typeset -i nbt
          nbt=0
          SECONDS=0

          echo "Waiting for the min masters count (${CLUSTER_SIZE})"

          while [[ $nbt -lt ${CLUSTER_SIZE} ]]; do
            tips=$(dig @127.0.0.11 +short tasks.${HOST})
            nbt=$(echo $tips | wc -w)
            [[ $SECONDS -gt 120 || $FINISH -eq 1 ]] && break
            sleep 1
          done

          for tip in $tips; do
            echo "Adding master: ${tip}"
            MASTERS=${MASTERS:+${MASTERS},}${tip}:${PORT}
          done

        fi
      done

      ARG=${MASTERS:+${OPTION}${MASTERS}}
    fi
  fi

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
          echo "Getting Task IPs for ${HOST}"

          # Get all tasks ips
          typeset -i nbt
          nbt=0
          SECONDS=0

          echo "Waiting for the min peers count (${CLUSTER_SIZE})"

          while [[ $nbt -lt ${CLUSTER_SIZE} ]]; do
            tips=$(dig @127.0.0.11 +short tasks.${HOST})
            nbt=$(echo $tips | wc -w)
            [[ $SECONDS -gt 120 || $FINISH -eq 1 ]] && break
            sleep 1
          done

          if [[ ${USE_DISCOVER:-true} != "false" ]]; then
            export LOCAL_HOSTNAME=$(hostname)
            CIP="-ip=${LOCAL_HOSTNAME}"
            # Find current ip on the network of the peers
            for tip in $tips; do
              cip=$(grep ${tip} /etc/hosts | awk '{print $1}' | head -1)
              if [[ -n "$cip" ]]; then
                echo "Found current ip: ${cip}"
                export LOCAL_IP=$cip

                # ? Does peers order matter?
                PEERS=${PEERS:+${PEERS},}${LOCAL_HOSTNAME}:${PORT}

                # Start discovery server in background
                tcpserver -D -H -R 0.0.0.0 555 /discover.sh &

                break
              fi
            done

            # Give time to all discovery servers to start
            sleep 10

            # Find peers hostname and add to /etc/hosts
            for tip in $tips; do
              if [[ "$tip" != "$cip" ]]; then
                REMOTE_HOSTNAME=""
                SECONDS=0
                # Retry logic
                while [[ -z "$REMOTE_HOSTNAME" ]]; do
                  # Connect to tip and retrieve the remote hostname, and save in /etc/hosts
                  REMOTE_HOSTNAME=$(tcpclient -D -H -R -T 10+120 $tip 555 /discover.sh)
                  [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
                  sleep 1
                done

                # IF found a valid hostname, add to peer. We expect to find up to CLUSTER_SIZE
                if [[ -n "$REMOTE_HOSTNAME" ]]; then
                  echo "Adding peer: ${REMOTE_HOSTNAME}"
                  PEERS=${PEERS:+${PEERS},}${REMOTE_HOSTNAME}:${PORT}
                fi
              fi
            done
          else
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
        fi
      done

      ARG=${PEERS:+${OPTION}${PEERS}}

    # Detect an ip command
    elif [[ $ARG == "-ip="* ]]; then
      OPTION=$(expr "$ARG" : '\(.*=\)')
      HOST=$(expr "$ARG" : '.*=\(.*\)')

      echo "Detected ${OPTION} with host ${HOST}"

      tips=$(dig @127.0.0.11 +short tasks.${HOST})
      for tip in $tips; do
        cip=$(grep ${tip} /etc/hosts | awk '{print $1}' | head -1)
        if [[ -n "$cip" ]]; then
          echo "Found current ip: ${cip}"
          IP="-ip=${cip}"
        fi
      done

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
elif ! [ -z "$PUBLIC_URL" ]; then
  HOST=$(expr "$PUBLIC_URL" : '\(.*\):')
  echo "Setting Host IP from Public Url: ${HOST}"
  ARGS=${ARGS:+${ARGS} }"-ip=${HOST}"
fi

# If there is a public url, add it to the parameters
if ! [ -z "$PUBLIC_URL" ]; then
  ARGS=${ARGS:+${ARGS} }"-publicUrl=$PUBLIC_URL"

  # If there is a tld, use it to extract rack and datacenter
  # gpu01.dal1.llnw.katapy.io with tld=katapy.io -> -rack=dal1 -dataCenter=llnw
  if ! [ -z "$TLD" ]; then
    IFS='.' read -a hostname_parts <<<${PUBLIC_URL%.${TLD}}
    ARGS=${ARGS:+${ARGS} }"-rack=${hostname_parts[1]} -dataCenter=${hostname_parts[2]}"
  fi

fi

echo "entrypoint called with args: $ARGS";

exec /entrypoint.sh $ARGS

wait &> /dev/null

exit 0

########################################################################################################################################################################################################################
