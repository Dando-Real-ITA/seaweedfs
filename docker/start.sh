#!/usr/bin/env bash
# -*- coding: utf-8 -*-
# 2022-08-16 13:48:35

########################################################################################################################################################################################################################

# Handling cleanup on stop signals
FINISH=0;
trap 'trap "FINISH=1" SIGTERM; kill 0; wait &> /dev/null' EXIT SIGINT SIGTERM SIGABRT

########################################################################################################################################################################################################################

check_masters() {
  echo ""
  # if [ -z "$1" ]; then
  #   die "Exiting... invalid arguments";
  # fi

  # # TESTARE CON GREP REGEX
  # if [[ -n "$(grep -E "\s+$HOSTNAME$" /etc/hosts)" ]]; then
  #   echo "$HOSTNAME Found in $ETC_HOSTS, Removing now...";
  #   try cp $ETC_HOSTS $TMP_HOSTS;
  #   try sed -i -r "/\s+$HOSTNAME/d" "$TMP_HOSTS";
  #   try cat $TMP_HOSTS > $ETC_HOSTS;
  # else
  #   yell "$HOSTNAME was not found in your $ETC_HOSTS";
  # fi

  # # Notify service is ready
  # /usr/bin/env systemd-notify --ready

  #   FAIL=0

  #   if [[ -n "${PID}" ]]; then
  #     check_pid || break
  #   fi

  #   check_date || FAIL=1

  #   if [[ $FAIL -eq 0 ]]; then
  #     /usr/bin/env systemd-notify WATCHDOG=1;
  #   else
  #     echo "Watchdog conditions fail"
  #   fi

  # sleep $(($WATCHDOG_USEC / 5000000))

}

check_peers() {
  SERVICE=$1
  PEERS=$2

  # Expected by tcpclient
  export LOCAL_HOSTNAME=$(hostname)
  export LOCAL_IP="no_add"

  echo "Started check_peers on ${LOCAL_HOSTNAME} for ${SERVICE} with peers: ${PEERS}"

  while(true); do
    [[ $FINISH -eq 1 ]] && break
    # Random sleep
    sleep $((300 + $RANDOM%180))

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

      # IF found a valid hostname, and it is not already present in the peers array,
      if [[ -n "${REMOTE_HOSTNAME}" && ! "${PEERS}" =~ "${REMOTE_HOSTNAME}" ]]; then
        echo "Found hostname not in peers: ${REMOTE_HOSTNAME}, restarting the container"
        kill -s SIGKILL 1
      fi
    done
  done
}

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
            [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
            sleep 1
          done

          if [[ ${USE_DISCOVER:-true} != "false" ]]; then
            # * Docker 20.10.0 auto creates alias for hostname if hostname != container
            # * For semplicity, it is assumed that the master hostname is ${HOST}_${task_slot}
            typeset -i task_slot
            task_slot=1
            for tip in $tips; do
              REMOTE_HOSTNAME="${HOST}_${task_slot}"
              ((task_slot+=1))
              echo ${REMOTE_HOSTNAME};

              echo "Adding master: ${REMOTE_HOSTNAME}"
              MASTERS=${MASTERS:+${MASTERS},}${REMOTE_HOSTNAME}:${PORT}
            done

            # TODO Make more robust, using a first tcpclient loop on ips to load real hostnames, and then a background repeated tcpclient loop with tasks ips to keep the hosts updated,
            # * with condition to when to make the check ( i.e. heartbeat failing)
            # # Find peers hostname and add to /etc/hosts
            # for tip in $tips; do
            #   if [[ "$tip" != "$cip" ]]; then
            #     REMOTE_HOSTNAME=""
            #     SECONDS=0
            #     # Retry logic
            #     while [[ -z "${REMOTE_HOSTNAME}" ]]; do
            #       # Connect to tip and retrieve the remote hostname, and save in /etc/hosts
            #       REMOTE_HOSTNAME=$(tcpclient -D -H -R -T 10+120 $tip 555 /discover.sh)
            #       [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
            #       sleep 1
            #     done

            #     # IF found a valid hostname, add to peer. We expect to find up to CLUSTER_SIZE
            #     if [[ -n "${REMOTE_HOSTNAME}" ]]; then
            #       echo "Adding peer: ${REMOTE_HOSTNAME}"
            #       PEERS=${PEERS:+${PEERS},}${REMOTE_HOSTNAME}:${PORT}
            #     fi
            #   fi
            # done
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
                while [[ -z "${REMOTE_HOSTNAME}" ]]; do
                  # Connect to tip and retrieve the remote hostname, and save in /etc/hosts
                  REMOTE_HOSTNAME=$(tcpclient -D -H -R -T 10+120 $tip 555 /discover.sh)
                  [[ $SECONDS -gt 300 || $FINISH -eq 1 ]] && break
                  sleep 1
                done

                # IF found a valid hostname, add to peer. We expect to find up to CLUSTER_SIZE
                if [[ -n "${REMOTE_HOSTNAME}" ]]; then
                  echo "Adding peer: ${REMOTE_HOSTNAME}"
                  PEERS=${PEERS:+${PEERS},}${REMOTE_HOSTNAME}:${PORT}
                fi
              fi
            done

            # Add check if peers change in number or hostname, and restart the process if this happens, to reload peers list
            # * Not necessary if the process is able to dynamically add and remove peers
            check_peers ${HOST} ${PEERS} &
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
