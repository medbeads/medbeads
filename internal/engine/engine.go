// Package engine is MedBeads v3's "唯一の真実の API" (specs/DESIGN_v3.md §2):
// the single Go package that unifies the Pod pack-file store (package pod)
// and the SQLite index (package index) behind one write protocol (Ingest)
// and a small set of thin read APIs (GetBead, ListPatientBeads).
//
// # Process model
//
// Per specs/DESIGN_v3.md §2 ("medbeadsd（Go 単一バイナリ・唯一の常駐プロセス）"),
// exactly one Engine is meant to be open against a given data directory at
// any time, within exactly one OS process. Engine enforces the
// single-process part of that with an flock(2) advisory lock on a lock file
// under the data directory (see openLockFile in lock.go): a second Open
// against the same data directory — even from the same process — fails
// immediately rather than silently corrupting the append-only Pod files.
// This matters because pod.Writer opens its file with os.O_APPEND, which
// only guarantees atomic positioning of each individual Write relative to
// other writers going through the *same* open file description; two
// independent *os.File handles (e.g. from two Engine/medbeadsd instances)
// each tracking their own idea of "current end of file" via separate Stat
// calls (see pod.Writer.Append) could race and corrup the offset bookkeeping
// index.db relies on. flock is therefore not just good hygiene here, it is
// load-bearing for the offset invariant pod.Writer's doc comment assumes.
//
// # Write protocol (Ingest)
//
// See Ingest's doc comment for the full protocol; in short: verify the
// Bead's content hash, reject unknown parents (structural DAG-acyclicity
// guarantee), pre-resolve patient_root by inheriting from parents' already-
// indexed patient_root (a single IN query, no N+1), append to the resolved
// Pod file (fsync included), then index it in one SQLite transaction. "正本
// が常に先、インデックスは追いつける" (the Pod is always the durable source of
// truth; the index can always catch up from it) is preserved throughout.
//
// # Crash recovery
//
// Open runs index.CatchUp for every Pod file found under the data
// directory's pods/, advancing each Pod's indexed_upto watermark to the
// Pod's actual end. This recovers the case where a prior process crashed
// after a Pod append + fsync but before its matching IndexBead transaction
// committed (specs/DESIGN_v3.md §3, R1.3).
package engine
