done = function(summary, latency, requests)
  io.write(string.format("RESULT rps=%.0f p50=%.1f p99=%.1f p999=%.1f max=%.1f errors=%d bytes=%d dur_s=%.2f\n",
    summary.requests / (summary.duration / 1e6),
    latency:percentile(50), latency:percentile(99), latency:percentile(99.9), latency.max,
    summary.errors.connect + summary.errors.read + summary.errors.write + summary.errors.status + summary.errors.timeout,
    summary.bytes, summary.duration / 1e6))
end
