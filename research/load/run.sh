#!/bin/bash
# usage: run.sh <binary> <label> <limit> <conns> <dur> [server_cpus] [client_cpus] [gomaxprocs]
BIN=$1; LABEL=$2; LIMIT=$3; CONNS=$4; DUR=$5; SC=${6:-0,1}; CC=${7:-2,3}; GMP=${8:-2}
THREADS=${THREADS:-2}
S=$(cd "$(dirname "$0")" && pwd)
PORT=18080
GOMAXPROCS=$GMP PORT=$PORT taskset -c $SC $BIN > /dev/null 2>&1 &
PID=$!
for i in $(seq 50); do curl -s -o /dev/null localhost:$PORT/healthz && break; sleep 0.1; done
URL="http://127.0.0.1:$PORT/fizzbuzz?int1=3&int2=5&limit=$LIMIT&str1=fizz&str2=buzz"
taskset -c $CC wrk -t$THREADS -c$CONNS -d2s $URL > /dev/null 2>&1   # warm-up
taskset -c $CC wrk -t$THREADS -c$CONNS -d$DUR -s $S/pct.lua $URL | grep RESULT | sed "s/^/$LABEL limit=$LIMIT c=$CONNS /"
kill $PID; wait $PID 2>/dev/null
