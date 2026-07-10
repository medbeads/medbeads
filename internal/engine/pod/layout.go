package pod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/medbeads/medbeads/internal/engine/bead"
)

// SharedPodName is the file name (under PodsDir) of the Pod holding Beads
// that do not belong to any single patient (e.g. drug_master), per
// specs/DESIGN_v3.md §3.
const SharedPodName = "_shared.pod"

// podFileExt is the file extension for Pod pack files.
const podFileExt = ".pod"

// Store resolves the on-disk layout of Pod pack files under a MedBeads data
// directory: pods/<root first 2 hex>/<root 64 hex>.pod for patient Pods, and
// pods/_shared.pod for the shared Pod (specs/DESIGN_v3.md §3). Store itself
// holds no file handles; it is a pure path/lookup helper that Writer and
// Reader are built on top of.
type Store struct {
	// dataDir is the MedBeads data directory root (contains pods/, dict/,
	// index.db).
	dataDir string
}

// NewStore returns a Store rooted at dataDir. dataDir need not already
// exist; PodPath/Ensure create directories as needed.
func NewStore(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

// DataDir returns the root data directory this Store was constructed with.
func (s *Store) DataDir() string {
	return s.dataDir
}

// PodsDir returns the directory containing all Pod pack files.
func (s *Store) PodsDir() string {
	return filepath.Join(s.dataDir, "pods")
}

// SharedPodPath returns the path to the shared Pod (Beads with no single
// patient_root: no parents, or parents spanning multiple roots).
func (s *Store) SharedPodPath() string {
	return filepath.Join(s.PodsDir(), SharedPodName)
}

// PatientPodPath returns the path to the Pod for the given patient_root
// Bead ID. root must be a plain (unprefixed) lower-case hex Bead ID, as
// returned by bead.ComputeID / validated by bead.ParseID.
func (s *Store) PatientPodPath(root string) (string, error) {
	id, err := bead.ParseID(root)
	if err != nil {
		return "", fmt.Errorf("pod: patient pod path: %w", err)
	}
	shard := id[:2]
	return filepath.Join(s.PodsDir(), shard, id+podFileExt), nil
}

// EnsurePatientPodDir creates the shard directory that will contain root's
// Pod file (a no-op if it already exists), and returns the Pod's path. This
// must be called before opening a patient Pod for writing, since os.OpenFile
// does not create parent directories.
func (s *Store) EnsurePatientPodDir(root string) (string, error) {
	path, err := s.PatientPodPath(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("pod: ensure patient pod dir: %w", err)
	}
	return path, nil
}

// EnsurePodsDir creates the top-level pods/ directory (a no-op if it already
// exists). This is sufficient before opening the shared Pod, which has no
// shard subdirectory.
func (s *Store) EnsurePodsDir() error {
	if err := os.MkdirAll(s.PodsDir(), 0o755); err != nil {
		return fmt.Errorf("pod: ensure pods dir: %w", err)
	}
	return nil
}

// RelPath returns path expressed relative to s.dataDir (e.g.
// "pods/ab/....pod"), for callers that persist a Pod path somewhere durable
// (index.db's pods.path column — see index.RegisterPod) and need that
// stored value to remain valid regardless of the current working directory
// or dataDir's own absolute location a future process opens it from (this
// package's own PatientPodPath/SharedPodPath/ListPodFiles all build paths by
// joining s.dataDir with a pods/... suffix, so they are exactly as portable
// as dataDir itself was when the Store was constructed — RelPath strips
// that dataDir prefix back off so a stored path can be re-joined against
// whatever dataDir a later process actually used, per AbsPath).
//
// path must be under s.dataDir (as every path this package itself
// constructs always is); a path outside dataDir is a caller bug and returns
// an error rather than a "../"-laden relative path that would silently
// resolve somewhere unexpected once re-joined by AbsPath.
func (s *Store) RelPath(path string) (string, error) {
	rel, err := filepath.Rel(s.dataDir, path)
	if err != nil {
		return "", fmt.Errorf("pod: rel path: %s relative to %s: %w", path, s.dataDir, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("pod: rel path: %s is not under data dir %s", path, s.dataDir)
	}
	return filepath.ToSlash(rel), nil
}

// AbsPath resolves a path previously produced by RelPath (or, for backward
// compatibility with a store that already has absolute paths recorded in
// pods.path from before this normalization existed — see this task's data-
// reviewer note — an already-absolute path, returned unchanged) against
// s.dataDir. It is RelPath's inverse: a caller reading pods.path back out of
// index.db should always route it through AbsPath before opening the file,
// so a data directory opened via a different cwd (or moved) than whichever
// process originally wrote that row still resolves to the right file.
func (s *Store) AbsPath(storedPath string) string {
	if filepath.IsAbs(storedPath) {
		return storedPath
	}
	return filepath.Join(s.dataDir, filepath.FromSlash(storedPath))
}

// ListPodFiles walks PodsDir and returns the path of every *.pod file found
// (patient Pods and the shared Pod), for use by VerifyAll / reindex. Order is
// filepath.WalkDir's lexical order, which is deterministic but not otherwise
// meaningful.
func (s *Store) ListPodFiles() ([]string, error) {
	var out []string
	root := s.PodsDir()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("pod: list pod files: walk %s: %w", path, err)
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) == podFileExt {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
