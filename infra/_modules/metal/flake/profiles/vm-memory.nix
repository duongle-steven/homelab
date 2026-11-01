{ pkgs, ... }:

# A VM hands back to its host only the pages its kernel frees (free page reporting), never its page cache, so each
# staging VM would grow to its whole size in the host's memory. On a VM only, reclaim cache every minute until a
# quarter of the node's memory is free again; hardware never runs it.
{
  systemd.services.memory-reclaim = {
    description = "Reclaim page cache so a VM's host gets its memory back";
    unitConfig.ConditionVirtualization = "vm";
    serviceConfig.Type = "oneshot";
    path = [ pkgs.gawk ];
    # swappiness=0: only the page cache, never anonymous memory into swap; the kernel may reclaim less than asked
    script = ''
      # KiB short of a quarter of MemTotal being free
      short=$(awk '/^MemTotal:/ { total = $2 } /^MemFree:/ { free = $2 } END { print int(total / 4 - free) }' /proc/meminfo)
      if [ "$short" -gt 0 ]; then
        echo "$(( short * 1024 )) swappiness=0" > /sys/fs/cgroup/memory.reclaim || true
      fi
    '';
  };
  systemd.timers.memory-reclaim = {
    wantedBy = [ "timers.target" ];
    # From the timer's start, at boot or at a switch that adds it: OnBootSec has long passed after a switch
    timerConfig = {
      OnActiveSec = "1min";
      OnUnitActiveSec = "1min";
    };
  };
}
