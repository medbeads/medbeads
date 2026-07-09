# tools/migrate

One-way v2 → v3 migration CLI (R7.2): reads v2 CAS objects, recomputes v3 IDs
in topological order, writes an old→new ID map CSV, and appends into Pods
with post-migration verification. Not yet implemented (lands with M1).
