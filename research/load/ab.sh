#!/bin/bash
# ab.sh OLD_BIN NEW_BIN LIMIT CONNS OUT_PREFIX [RUNS] [DUR]
# Interleaves OLD and NEW runs so that drift in the VM hits both equally.
# Writes OUT_PREFIX.old.txt and OUT_PREFIX.new.txt in benchstat format:
# sec/op is server core time per request (2 cores / RPS), and the latency
# percentiles are extra units.
OLD=$1; NEW=$2; LIMIT=$3; CONNS=$4; OUT=$5; RUNS=${6:-10}; DUR=${7:-5s}
D=$(cd "$(dirname "$0")" && pwd)
: > $OUT.old.txt; : > $OUT.new.txt
line() { # label result-line
  awk -v n="$1" -v lim=$LIMIT '{for(i=1;i<=NF;i++){split($i,kv,"=");v[kv[1]]=kv[2]}}
    END{printf "BenchmarkTCP/limit=%s 1 %.1f ns/op %.0f req/s %.3f GB/s %.1f p50-us %.1f p99-us %.1f p999-us\n",
      lim, 2e9/v["rps"], v["rps"], v["bytes"]/v["dur_s"]/1e9, v["p50"], v["p99"], v["p999"]}' <<<"$2"
}
for i in $(seq $RUNS); do
  for which in old new; do
    BIN=$OLD; [ $which = new ] && BIN=$NEW
    R=$($D/run.sh $BIN $which $LIMIT $CONNS $DUR)
    line $which "$R" >> $OUT.$which.txt
  done
done
