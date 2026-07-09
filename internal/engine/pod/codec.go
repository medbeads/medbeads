package pod

import (
	"fmt"
	"os"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// compress compresses src using codec and returns the result. CodecRaw
// returns src unchanged (no copy — callers must not mutate the result if
// they still hold src, matching the immutable-buffer convention used
// elsewhere in this package).
func compress(codec Codec, src []byte) ([]byte, error) {
	switch codec {
	case CodecRaw:
		return src, nil
	case CodecZstd:
		enc, err := zstdEncoder()
		if err != nil {
			return nil, err
		}
		return enc.EncodeAll(src, nil), nil
	case CodecZstdDict:
		// No writer selects CodecZstdDict yet (dictionary trained during
		// R7.2 migration; see doc.go), but the compress-side switch is
		// complete so that adding a dictionary later only requires wiring an
		// encoder, not restructuring this function.
		return nil, fmt.Errorf("pod: compress: %w: zstd-dict encoding not yet available (no shared dictionary loaded)", ErrUnknownCodec)
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownCodec, uint8(codec))
	}
}

// decompress reverses compress for the given codec.
func decompress(codec Codec, src []byte) ([]byte, error) {
	switch codec {
	case CodecRaw:
		return src, nil
	case CodecZstd:
		dec, err := zstdDecoder()
		if err != nil {
			return nil, err
		}
		out, err := dec.DecodeAll(src, nil)
		if err != nil {
			return nil, fmt.Errorf("pod: zstd decompress: %w", err)
		}
		return out, nil
	case CodecZstdDict:
		return decompressWithDict(src, nil)
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownCodec, uint8(codec))
	}
}

// decompressWithDict decompresses zstd-dict-coded core_bytes using dict (the
// raw shared-dictionary bytes loaded via LoadDict). It exists as its own
// function — separate from the process-wide zstdDecoder() used for
// CodecZstd — because a dictionary decoder is dictionary-specific and must
// not be shared across dictionary versions.
func decompressWithDict(src, dict []byte) ([]byte, error) {
	if dict == nil {
		return nil, fmt.Errorf("pod: decompress zstd-dict: %w: no dictionary loaded", ErrUnknownCodec)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(dict))
	if err != nil {
		return nil, fmt.Errorf("pod: decompress zstd-dict: new reader: %w", err)
	}
	defer dec.Close()
	out, err := dec.DecodeAll(src, nil)
	if err != nil {
		return nil, fmt.Errorf("pod: decompress zstd-dict: %w", err)
	}
	return out, nil
}

// LoadDict reads a zstd shared-dictionary file from path (e.g.
// dict/zstd-v1.dict, specs/DESIGN_v3.md §3) and returns its raw bytes,
// validating that zstd itself accepts it as a dictionary. Nothing in R1
// calls this yet — CodecZstdDict has no writer until the dictionary is
// trained during migration (R7.2) — but the loader is implemented now so the
// codec ID is exercised end-to-end (frame flags, reader dispatch) ahead of
// that work landing.
func LoadDict(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pod: load dict: %w", err)
	}
	// Validate by attempting to construct a decoder with it; discard the
	// decoder immediately, we only want the validation side effect.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(data))
	if err != nil {
		return nil, fmt.Errorf("pod: load dict: %s: not a valid zstd dictionary: %w", path, err)
	}
	dec.Close()
	return data, nil
}

// --- process-wide, dictionary-less zstd encoder/decoder ------------------
//
// zstd.NewWriter/NewReader are relatively heavyweight to construct (they
// spin up worker goroutines), so this package builds one encoder and one
// decoder lazily and reuses them for every CodecZstd frame. Both types are
// documented by klauspost/compress as safe for concurrent use by multiple
// goroutines, which matches this package's per-Pod (not process-wide)
// locking model in writer.go.

var (
	zstdEncOnce sync.Once
	zstdEnc     *zstd.Encoder
	zstdEncErr  error

	zstdDecOnce sync.Once
	zstdDec     *zstd.Decoder
	zstdDecErr  error
)

func zstdEncoder() (*zstd.Encoder, error) {
	zstdEncOnce.Do(func() {
		zstdEnc, zstdEncErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	})
	return zstdEnc, zstdEncErr
}

func zstdDecoder() (*zstd.Decoder, error) {
	zstdDecOnce.Do(func() {
		zstdDec, zstdDecErr = zstd.NewReader(nil)
	})
	return zstdDec, zstdDecErr
}
