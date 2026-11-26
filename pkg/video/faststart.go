package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// maxTopLevelBoxes bounds the walk over an MP4's top-level boxes, so a corrupt
// file can never make MoovFirst loop for long.
const maxTopLevelBoxes = 64

// ErrNotMP4 is returned by MoovFirst when the data has neither a moov nor an
// mdat box among its top-level boxes.
var ErrNotMP4 = errors.New("no moov or mdat box found")

// ReadAtFunc reads up to n bytes at offset off. It may return fewer bytes when
// the data ends first.
type ReadAtFunc func(off, n int64) ([]byte, error)

// MoovFirst reports whether an MP4's moov box (the index) comes before its
// mdat box (the media data), which is what lets a player stream it over HTTP
// without first fetching the end of the file ("faststart"). It walks the
// top-level box headers only, so read can be a few small range requests
// against a remote object of the given size.
func MoovFirst(read ReadAtFunc, size int64) (bool, error) {
	var off int64
	for i := 0; i < maxTopLevelBoxes && off+8 <= size; i++ {
		hdr, err := read(off, 16)
		if err != nil {
			return false, err
		}
		if len(hdr) < 8 {
			return false, fmt.Errorf("short box header at %d", off)
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
		switch string(hdr[4:8]) {
		case "moov":
			return true, nil
		case "mdat":
			return false, nil
		}
		switch boxSize {
		case 0: // the box runs to the end of the file
			return false, ErrNotMP4
		case 1: // 64-bit size follows the type
			if len(hdr) < 16 {
				return false, fmt.Errorf("short large box header at %d", off)
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
		}
		if boxSize < 8 {
			return false, fmt.Errorf("invalid box size %d at %d", boxSize, off)
		}
		off += boxSize
	}
	return false, ErrNotMP4
}

// MoovFirstBytes is MoovFirst over an MP4 held in memory.
func MoovFirstBytes(data []byte) (bool, error) {
	size := int64(len(data))
	return MoovFirst(func(off, n int64) ([]byte, error) {
		return data[off:min(off+n, size)], nil
	}, size)
}

// RemuxFaststart rewrites an MP4 with its moov box at the start, copying every
// stream as is (no re-encoding, so quality and size are unchanged apart from
// the box order). The video's codec tag (e.g. hvc1) and metadata are kept.
func RemuxFaststart(ctx context.Context, data []byte) ([]byte, error) {
	in, err := os.CreateTemp("", "faststart-in-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("create input temp: %w", err)
	}
	defer os.Remove(in.Name())
	if _, err := in.Write(data); err != nil {
		in.Close()
		return nil, fmt.Errorf("write input temp: %w", err)
	}
	if err := in.Close(); err != nil {
		return nil, err
	}

	out, err := os.CreateTemp("", "faststart-out-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("create output temp: %w", err)
	}
	outPath := out.Name()
	out.Close()
	defer os.Remove(outPath)

	args := []string{
		"-y",
		"-i", in.Name(),
		"-map", "0",
		"-map_metadata", "0",
		"-c", "copy",
	}
	// A stream copy does not always keep the source tag (ffmpeg writes hev1
	// for HEVC by default), so carry it over explicitly.
	if tag := probeVideoTag(ctx, in.Name()); tag != "" {
		args = append(args, "-tag:v", tag)
	}
	args = append(args, "-movflags", "+faststart", outPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg remux failed: %w: %s", err, stderr.String())
	}
	return os.ReadFile(outPath)
}

// probeVideoTag returns the first video stream's codec tag (e.g. "hvc1",
// "avc1"), or "" when it can't be read.
func probeVideoTag(ctx context.Context, path string) string {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_tag_string",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return ""
	}
	tag := string(bytes.TrimSpace(out))
	if len(tag) != 4 || bytes.ContainsAny([]byte(tag), "[]") {
		return ""
	}
	return tag
}
