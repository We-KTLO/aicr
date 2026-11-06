# ADR-030: Snapshots Beyond the 1 MiB ConfigMap Limit

## Status

**Draft** — 2026-11-06. Not yet discussed with maintainers; no issue filed.

## Decision Summary

Proposed: keep a single snapshot document as the contract, but stop requiring
it to fit in one ConfigMap or under the 1 MiB spec-file cap.

1. **Encode node lists once.** Store each topology entry's node set as indexes
   into one node-name table instead of repeating names (DD1). This shrinks the
   snapshot itself and helps every sink.
2. **Compress the staging ConfigMap.** The agent writes the serialized snapshot
   gzip-compressed into `binaryData` when the plain form would not fit, and the
   CLI decompresses it with a bounded reader (DD2).
3. **Chunk when compression is not enough.** Split the compressed payload over
   N run-owned ConfigMaps plus a manifest with per-chunk and whole-payload
   digests (DD3).
4. **Separate the snapshot read cap from the spec cap.** `aicr validate
   --snapshot` and `aicr recipe --snapshot` read snapshots under a snapshot cap
   (proposed 64 MiB decompressed) rather than `defaults.MaxSpecFileBytes`
   (1 MiB) (DD4).

An object-store or PVC sink (DD5) is left as an opt-in for clusters past what
DD1–DD3 can carry.

## Problem

On a managed-service HGX B300 cluster, `aicr snapshot` (v0.22.0 and v1.0.0)
could not produce a snapshot at all, and a snapshot taken another way could not
be validated:

| Quantity | Value |
| --- | --- |
| Nodes in the cluster | 326 (323 GPU nodes) |
| Topology label entries collected | 3,106 |
| Topology taint entries collected | 2 |
| Serialized snapshot (YAML), unpruned | about 1.9 MB |
| ConfigMap size limit (API server) | 1,048,576 bytes |
| Result | agent exit code 8: `ConfigMap ... is invalid: []: Too long: may not be more than 1048576 bytes` |
| `aicr validate --snapshot <file>` cap | 1 MiB (`defaults.MaxSpecFileBytes`), so the same document is rejected from a file too |

The size is dominated by node-name lists. With `--max-nodes-per-entry=8` and the
single-node label entries dropped by hand, the same cluster's snapshot came to
about 79 KB, about 4 % of the unpruned size. That workaround is lossy:
readings that the cap actually truncates fail closed in the checks that need
set membership (for example the GKE `gpuStack` pre-flight in
`docs/user/validation.md`), and dropping entries by hand is not reproducible.

The limit is structural, not specific to one provider. Label count grows with
node count times the per-node labels that are unique to a node
(`kubernetes.io/hostname`, instance IDs, serials, topology labels), so any
cluster of a few hundred nodes with rich node labels reaches it.

### Where the limit is hit today

- The in-pod agent writes the whole snapshot into one staging ConfigMap through
  `serializer.ConfigMapWriter.Serialize` (`pkg/serializer/configmap.go`), and
  the CLI reads it back with `Deployer.getSnapshotFromConfigMap`
  (`pkg/k8s/agent/wait.go`).
- `-o cm://ns/name` writes the same single ConfigMap.
- File reads go through `serializer` with `defaults.MaxSpecFileBytes` (1 MiB)
  in `pkg/serializer/reader.go`, so the cap applies to snapshots exactly as to
  recipes and specs.

## Design Decisions

### DD1: Node-set encoding (schema change, smaller documents everywhere)

Add an optional node table to the topology measurement:

```yaml
nodeTopology:
  nodes: [node-a, node-b, node-c, ...]     # each name once, sorted
  labels:
    nvidia.com/gpu.product=NVIDIA-B300-SXM6-AC:
      nodeRefs: "0-322"                     # ranges over the table
```

- Ranges (`0-322,324`) make "every GPU node has this label" a few bytes instead
  of 323 names.
- Readers accept both the current `node-list` form and `nodeRefs`; writers emit
  `nodeRefs` from a new snapshot minor version on. The `--max-nodes-per-entry`
  truncation marker keeps its meaning.
- Not measured: the size after DD1 on the 326-node snapshot. The pruned excerpt
  suggests node names are well over 90 % of the bytes, but this needs a
  measurement before the ADR is accepted.

### DD2: Compressed staging ConfigMap

- When the serialized snapshot exceeds a threshold (proposed 900 KiB, leaving
  room for metadata and annotations), the agent writes
  `binaryData["snapshot.yaml.gz"]` instead of `data["snapshot.yaml"]`, with an
  annotation recording the encoding and the uncompressed SHA-256.
- Below the threshold nothing changes, so small clusters and existing readers
  keep working.
- The reader decompresses through a bounded reader (the DD4 cap) and verifies
  the digest, so a crafted ConfigMap cannot inflate without limit.
- `binaryData` counts toward the same 1 MiB object limit, so DD2 alone moves the
  ceiling (YAML of repeated node names typically compresses well; ratio not yet
  measured on this data) but does not remove it.

### DD3: Chunked ConfigMaps for the remainder

- If the compressed payload still exceeds the threshold, the agent writes it as
  chunks `<staging-name>-<i>` (each under the threshold) and a manifest
  ConfigMap listing the chunk names, sizes and digests plus the whole-payload
  digest.
- All chunks are **run-owned** under ADR-020: suffixed with the run ID, labelled
  `aicr.run/run-id`, and removed by the same cleanup as the staging ConfigMap.
  The agent's Role gains `create` on ConfigMaps whose names it derives, nothing
  broader.
- `-o cm://ns/name` gets the same layout, with `name` as the manifest. A reader
  that predates DD3 sees a manifest it cannot parse and fails closed with a
  message naming the minimum CLI version.
- Writes are not atomic across chunks; the manifest is written last and is the
  commit point, so a reader never sees a partial snapshot.

### DD4: A snapshot-specific read cap

- New `defaults.MaxSnapshotBytes` (proposed 64 MiB, decompressed) for snapshot
  inputs to `validate`, `recipe`, `diff` and the API server, separate from
  `MaxSpecFileBytes`. Recipes and specs keep the 1 MiB cap.
- Accept `.yaml.gz` / `.json.gz` files for `--snapshot`, decompressed under the
  same cap.

### DD5: Optional external sink (not the default)

For clusters beyond what DD1–DD3 carry, allow `-o pvc://ns/claim/path` and an
object-store URI, written by the agent and read by the CLI. This adds
credentials and RBAC surface, so it stays opt-in. Running the agent with stdout
output and collecting the Job log already works as a manual escape hatch and
should be documented as such in the meantime.

## Alternatives Considered

- **Only raise `--max-nodes-per-entry` defaults.** Truncation is lossy and makes
  set-membership checks fail closed; it is a workaround, not a fix.
- **Drop node-unique labels from collection.** Shrinks the document but removes
  data some checks use, and the right list differs per provider.
- **Always use an external sink.** Works at any size but makes the common small
  cluster case depend on storage the cluster may not have.

## Consequences

- Snapshot schema gains an optional node table (DD1); consumers that parse
  `node-list` directly need an update, or a helper that expands `nodeRefs`.
- The redaction policy must treat the node table like the existing node lists
  (names are dropped under minimal redaction).
- Test plan: a KWOK profile with 1,000 nodes and per-node unique labels that
  exceeds 1 MiB unpruned; round-trip tests for plain, compressed and chunked
  staging; a bounded-decompression test with a payload whose inflated size
  exceeds the cap; and a compatibility test that an old-format snapshot still
  reads.

## Open Questions

1. Is DD1 alone enough for the clusters maintainers care about? It needs a
   measurement on a real large snapshot.
2. Should the compression threshold be configurable, or fixed?
3. Does the API server (`aicr serve`) need the same cap change in this ADR or a
   follow-up?
