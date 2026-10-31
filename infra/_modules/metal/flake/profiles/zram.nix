{
  # Compressed swap in memory: a small node (staging's 3.5 GB VMs) keeps more of what it rarely touches
  zramSwap = {
    enable = true;
    memoryPercent = 50;
  };
}
