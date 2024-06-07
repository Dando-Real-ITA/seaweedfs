#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2024-06-07 00:43:26

########################################################################################################################################################################################################################

# Handling cleanup on stop signals
FINISH=0;
trap 'trap "FINISH=1" SIGTERM; kill 0; wait &> /dev/null' EXIT SIGINT SIGTERM SIGABRT

########################################################################################################################################################################################################################

check_masters() {
  SERVICE=$1
  MASTERS=$2

  # Expected by tcpclient
  export LOCAL_HOSTNAME=$(hostname)
  # The remote host will not update the hostname-ip map
  export LOCAL_IP="no_add"
  # Don't update locally the hostname-ip map
  export ADD_HOST="false"

  echo "Started check_masters on ${LOCAL_HOSTNAME} for ${SERVICE} with masters: ${MASTERS}"

  while(true); do
    [[ $FINISH -eq 1 ]] && break
    # Random sleep
    sleep $((60 + $RANDOM%30))

    # Current service ips
    tips=$(dig @127.0.0.11 +short tasks.${SERVICE})

    for tip in $tips; do
      REMOTE_HOSTNAME=""
      SECONDS=0
      # Retry logic
      while [[ -z "${REMOTE_HOSTNAME}" ]]; do
        # Connect to tip and retrieve the remote hostname
        REMOTE_HOSTNAME=$(tcpclient -D -H -R -T 10+120 $tip 555 /discover.sh)
        [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
        sleep 1
      done

      # IF found a valid hostname, and it is not already present in the masters list, restart container
      if [[ -n "${REMOTE_HOSTNAME}" && ! "${MASTERS}" =~ (^|,)${REMOTE_HOSTNAME}(:|$) ]]; then
        sleep $((60 + $RANDOM%30))
        echo "Found hostname not in masters: ${REMOTE_HOSTNAME}, restarting the container"
        reboot
      fi
    done
  done
}

check_peers() {
  SERVICE=$1
  PORT=$2

  # Load the current cluster ids ( which are HOST:PORT )
  # * If it is a new cluster, weed shell will timeout and cluster will be empty
  echo "Loading master list from raft cluster ${SERVICE}"
  cluster=($(echo "cluster.raft.ps; exit" | timeout -s SIGKILL 1m /usr/bin/weed shell -master=${SERVICE}:${PORT} 2>/dev/null | awk 'NR>2{ print $2 }'))

  # Verify for every master if it is online
  for id in "${cluster[@]}"; do
    master=${id%:${PORT}}

    if ! ping -c1 ${master} &>/dev/null; then
      # Remove the dead master
      echo "Removing dead master ${master} from raft cluster ${SERVICE}"
      SECONDS=0
      while echo "cluster.raft.remove -id=${master}:${PORT}; exit" | timeout -s SIGKILL 1m /usr/bin/weed shell -master=${SERVICE}:${PORT}; do
        [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
        sleep 1
      done
    fi
  done
}

########################################################################################################################################################################################################################

# Give time to all servers to start and get an ip
sleep 10

# Read all commands
ARGS=""
IP=""
CIP=""
COMMAND_PORT=""

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
            [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
            sleep 1
          done

          if [[ ${USE_DISCOVER:-true} != "false" ]]; then
            # * Docker 20.10.0 auto creates alias for hostname if hostname != container
            # Find masters hostname
            # Expected by tcpclient
            export LOCAL_HOSTNAME=$(hostname)
            # The remote host will not update the hostname-ip map
            export LOCAL_IP="no_add"
            # Don't update locally the hostname-ip map
            export ADD_HOST="false"
            for tip in $tips; do
              REMOTE_HOSTNAME=""
              SECONDS=0
              # Retry logic
              while [[ -z "${REMOTE_HOSTNAME}" ]]; do
                # Connect to tip and retrieve the remote hostname, and save in /etc/hosts
                REMOTE_HOSTNAME=$(tcpclient -D -H -R -T 10+120 $tip 555 /discover.sh)
                [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
                sleep 1
              done

              # IF found a valid hostname, add to master
              if [[ -n "${REMOTE_HOSTNAME}" ]]; then
                echo "Adding master: ${REMOTE_HOSTNAME}"
                MASTERS=${MASTERS:+${MASTERS},}${REMOTE_HOSTNAME}:${PORT}
              fi
            done

            # Add check if masters change in number or hostname, and restart the process if this happens, to reload masters list
            # * Not necessary if the process is able to dynamically add and remove masters
            # check_masters ${HOST} ${MASTERS} &
          else
            for tip in $tips; do
              echo "Adding master: ${tip}"
              MASTERS=${MASTERS:+${MASTERS},}${tip}:${PORT}
            done
          fi
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
            [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
            sleep 1
          done

          if [[ ${USE_DISCOVER:-true} != "false" ]]; then
            # * Docker 20.10.0 auto creates alias for hostname if hostname != container
            # Expected by tcpclient
            export LOCAL_HOSTNAME=$(hostname)
            # The remote host will not update the hostname-ip map
            export ADD_HOST="false"
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
                while [[ -z "${REMOTE_HOSTNAME}" ]]; do
                  # Connect to tip and retrieve the remote hostname, and save in /etc/hosts
                  REMOTE_HOSTNAME=$(tcpclient -D -H -R -T 10+120 $tip 555 /discover.sh)
                  [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
                  sleep 1
                done

                # IF found a valid hostname, add to peer
                if [[ -n "${REMOTE_HOSTNAME}" ]]; then
                  echo "Adding peer: ${REMOTE_HOSTNAME}"
                  PEERS=${PEERS:+${PEERS},}${REMOTE_HOSTNAME}:${PORT}
                fi
              fi
            done

            # # Remove unavailable peers before starting the process. Can happen if this process is a member of the cluster restarted with a different hostname
            # check_peers ${HOST} ${PORT}
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

  if [[ ${USE_HOSTNAME:-true} != "false" ]]; then
    if [[ $ARG == "-ip="* ]]; then
      LOCAL_HOSTNAME=$(hostname)
      IP="-ip=${LOCAL_HOSTNAME}"
      # Not writing the ip for now
      ARG=""
    fi
  fi

  if [[ $ARG == "-port="* ]]; then
    VALUE=$(expr "$ARG" : '.*=\(.*\)')
    COMMAND_PORT="$VALUE"
  fi

  ARGS=${ARGS:+${ARGS} }$ARG
done

if [ -n "$CIP" ]; then
  ARGS=${ARGS:+${ARGS} }$CIP
elif [ -n "$IP" ]; then
  ARGS=${ARGS:+${ARGS} }$IP
elif [ -n "$PUBLIC_URL" ]; then
  HOST=$(expr "$PUBLIC_URL" : '\(.*\):')
  echo "Setting Host IP from Public Url: ${HOST}"
  ARGS=${ARGS:+${ARGS} }"-ip=${HOST}"
fi

# If there is a public url, add it to the parameters
if [ -n "$PUBLIC_URL" ]; then
  ARG_PUBLIC_URL="$PUBLIC_URL"
  PUBLIC_URL_PORT=$(expr "$PUBLIC_URL" : '.*:\(.*\)')
  if [ -z "$PUBLIC_URL_PORT" ]; then
    if [ -n "$COMMAND_PORT" ]; then
      PUBLIC_URL_PORT=$COMMAND_PORT
    else
      PUBLIC_URL_PORT=8080
      echo "No public url port found, assuming default: $PUBLIC_URL_PORT";
    fi
    ARG_PUBLIC_URL="$PUBLIC_URL:$PUBLIC_URL_PORT"
  fi
  ARGS=${ARGS:+${ARGS} }"-publicUrl=$ARG_PUBLIC_URL"

  # If there is a tld, use it to extract rack and datacenter
  # [vpn.]gpu01.nash01.usa.katapy.io[:8080] with tld=katapy.io -> -rack=nash01 -dataCenter=usa
  if [ -n "$TLD" ]; then
    rack_url=${PUBLIC_URL#vpn.}
    rack_url=${rack_url%:*}
    IFS='.' read -a hostname_parts <<<"${rack_url%."${TLD}"}"
    ARGS=${ARGS:+${ARGS} }"-rack=${hostname_parts[1]} -dataCenter=${hostname_parts[2]}"
  fi

fi

echo "entrypoint called with args: $ARGS";

exec /entrypoint.sh $ARGS

wait &> /dev/null

exit 0

########################################################################################################################################################################################################################
