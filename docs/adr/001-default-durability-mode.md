# ADR-001: Default durability mode

Date: 2026-11-01
Status: accepted

Context: An acknowledged append must survive a power cut. fsync on the Pi's
SD card takes ~4.8 ms, so one fsync per record caps throughput at ~200/s.
Measured through the sequencer (300-byte records, median of 3 runs):
always 196/s p99 9.3 ms (1 writer) and 186/s p99 690 ms (64 writers);
batch 96/s p99 18 ms (1 writer) and 3,690/s p99 32 ms (64 writers);
none 28,000–40,000/s, but loses unsynced writes on power loss.
Fsyncing .idx as well as .log adds 1% (4.84 vs 4.80 ms).
Real load today: 240 records/day from one phone.

Options:

1. always: lowest latency for one writer, ~200/s ceiling.
2. batch (5 ms / 64 KiB): +5 ms for a lone writer, 19x throughput under load.
3. none: fastest, loses acknowledged writes on power loss.

Decision: batch, because the load is about to stop being one phone: M2 adds
the Pi metrics agent and replay-driven load tests, and views write derived
events back into the log. Concurrent writers need more than the ~200 fsyncs/s
that always can give on this SD card, and batch keeps always's guarantee that
no acknowledged record is lost. Keep fsyncing .idx with .log.

Consequences:

- Easier: throughput scales with concurrent writers (3,690/s vs 186/s at 64
  writers) without giving up durability; p99 stays at 32 ms where always
  reaches 690 ms.
- Harder: a lone writer waits ~5 ms for a batch that never forms (p50 9.8 ms
  vs 4.5 ms for always), and an SD card stall (150–900 ms, seen in 3 of 27
  runs) delays every record in the batch, not just one.
- Durability modes do not affect corruption. Torn or corrupt records are
  handled by the CRC and recovery in every mode.
- The .idx fsync buys nothing for correctness, since recovery rebuilds the
  .idx from the .log on every Open. It is kept because it costs ~1% and one
  sync() for both files is simpler than reasoning about which file is
  durable.

Revisit when:

- the USB SSD replaces the SD card (fsync may drop 10–50x, so always's
  ceiling rises to thousands per second);
- a lone writer's 5 ms wait matters (fix: adaptive group commit, which
  commits at once when nothing else is queued);
- sustained load passes ~3,000 appends/s, near batch's measured ceiling here;
- startup with many segments gets slow enough that reading the .idx instead
  of rebuilding it is worth it, which would make the .idx fsync matter.
