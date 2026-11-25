# Filer HTTP download chunk cache

HTTP file downloads can reuse the same `TieredChunkCache` disk implementation
used by `weed mount`. The filer owns a separate instance; it must not open a
running mount's cache files.

## Options

| `weed filer` option | Default | Meaning |
| --- | --- | --- |
| `-cacheDir` | empty | Parent directory for reusable download chunks. Empty disables retention. |
| `-cacheCapacityMB` | `16384` | Retained disk-cache capacity in MiB when `cacheDir` is set. Zero disables retention. |
| `-cacheDirWrite` | empty | Temporary download staging directory. Defaults to a private staging subdirectory under `cacheDir`, or the OS temporary directory when no cache directory is set. |
| `-downloadStagingMemoryThresholdMB` | `16` | Chunks up to this size are staged in memory; larger ones on disk. Must not exceed `downloadStagingMemoryMB`. |
| `-downloadStagingMemoryMB` | `1024` | Total staging memory shared by all downloads. |
| `-downloadStagingDiskMB` | `16384` | Total staging disk shared by all downloads. Larger chunks fail to download. |

For `weed server`, prefix these options with `filer.` (for example
`-filer.cacheDir`). Mount and embedded S3/WebDAV cache settings are
independent. These options apply to the filer's HTTP chunk-backed downloads.

The filer creates a stable identity-specific subdirectory under `cacheDir` and
locks it against another process using the same filer identity. Separate filers
can use the same parent directory. Do not copy or directly share a live mount
cache directory. Retained entries survive a normal filer restart; they are
discarded when the filer runs a different SeaweedFS version.

## Read and fill behavior

1. Reserve a prefetch/staging slot before opening an upstream response.
2. Try the retained chunk cache. A full cached chunk can serve a partial view.
   Volume locations are looked up only on a cache miss.
3. On a miss for a retainable chunk, fetch the **whole chunk** even for a range
   request, with replica and transient same-volume retries; the range is served
   from staging. Failed attempts are not published in the cache. A range miss
   therefore reserves and downloads a full chunk before its first byte.
4. Insert successful whole decoded chunks into the existing disk cache using
   a bounded-memory reader API. Encrypted chunks bypass retention.
5. Deliver the private staging data to the client and delete it afterwards.

Chunk manifests are resolved through the same in-memory `ChunkManifestCache`
used by mount (LRU, 256 entries / 64 MiB) when retention is enabled.

Concurrent requests for the same retainable chunk are coalesced: one request
fills it and the others wait, then read the retained copy. Unrelated chunks
never wait on each other. If the filling request is cancelled or fails, a
waiting request takes over. Chunks too large to retain do not start a
coalesced fill and are fetched as the requested view only.
Entry metadata is still read from the filer store; this cache is not an
offline file store. Retention failures are best effort and do not invalidate a
successfully staged response. Eviction uses mount's existing segmented FIFO
policy.

The cache lock is held only to look up, reserve or publish an entry. Copies
into and out of the cache run unlocked on a pinned segment, so a slow insert
never blocks reads or other inserts (filer and mount alike). Recycling a
segment unlinks its file; in-flight copies keep reading the old data, and an
insert whose segment was recycled is discarded. Until those copies finish, the
unlinked segment still occupies disk. Cache hits still involve a local copy into
staging. No full-size chunk RAM allocation is needed for insertion or copying.

## Capacity and disk placement

Four-prefetch includes completed but unconsumed staging chunks. A staged chunk
up to the memory threshold is held in memory; a larger one is written to disk
from the first byte. Filer staging budgets (1 GiB memory and 16 GiB disk by
default) are independent of retained-cache capacity; when exhausted, new
downloads wait. The cache's segmented layout also limits the size
of each retained entry; entries too large for a segment are delivered but not
retained.

For 250 MiB chunks, a full four-chunk download window reserves about 1,000 MiB
staging disk and no staging memory, so the default disk budget lets about 16
downloads stage at full depth. A 16 GiB retained cache plus a 16 GiB
staging budget can require roughly 32 GiB disk space, plus indexes, filesystem
overhead, and headroom. Budgets do not reserve actual filesystem free space.

Choose a disk-backed local directory, preferably SSD, rather than a small
temporary RAM filesystem. Temporary files are removed on normal completion,
error, or cancellation; a process crash can leave staging files behind.