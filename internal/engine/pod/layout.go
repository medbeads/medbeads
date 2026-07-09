package pod

import (
	"fmt"
	"os"
	"path/filepath"

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
