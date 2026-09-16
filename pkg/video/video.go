package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"neoassets/internal/models"
)

// ffprobeOutput is the subset of ffprobe JSON we consume.
type ffprobeOutput struct {
	Streams []struct {
		CodecName  string `json:"codec_name"`
		CodecType  string `json:"codec_type"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		RFrameRate string `json:"r_frame_rate"`
	} `json:"streams"`
	Format struct {
		FormatName string  `json:"format_name"`
		Duration   string  `json:"duration"`
		Size       string  `json:"size"`
	} `json:"format"`
}

// ParseFPS converts an "num/den" ffprobe frame rate string into an integer
// frame rate (rounded down, e.g. 30000/1001 -> 29).
func ParseFPS(r string) int {
	parts := []byte(r)
	idx := bytes.IndexByte(parts, '/')
	if idx < 0 {
		n, _ := strconv.Atoi(string(parts))
		return n
	}
	num, err1 := strconv.Atoi(string(parts[:idx]))
	den, err2 := strconv.Atoi(string(parts[idx+1:]))
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

// ImageInfo describes an image's codec and pixel dimensions.
type ImageInfo struct {
	Codec  string
	Width  int
	Height int
}

// ProbeImage inspects an image file (any ffmpeg-supported format) and returns
// its codec name and dimensions. Used to validate approved media.
func ProbeImage(ctx context.Context, data []byte) (*ImageInfo, error) {
	tmp, err := os.CreateTemp("", "image-probe-*")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		tmp.Name(),
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	info := &ImageInfo{}
	for _, s := range parsed.Streams {
		if s.CodecType == "video" {
			info.Codec = s.CodecName
			info.Width = s.Width
			info.Height = s.Height
			break
		}
	}
	return info, nil
}

// Probe inspects a video file (any ffmpeg-supported container/codec) and returns
// its format, video codec, resolution, frame rate and duration.
func Probe(ctx context.Context, data []byte) (*models.VideoMeta, error) {
	tmp, err := os.CreateTemp("", "video-probe-*")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		tmp.Name(),
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	meta := &models.VideoMeta{Format: parsed.Format.FormatName}
	for _, s := range parsed.Streams {
		if s.CodecType == "video" {
			meta.Codec = s.CodecName
			meta.Width = s.Width
			meta.Height = s.Height
			meta.FPS = ParseFPS(s.RFrameRate)
			break
		}
	}
	if d, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		meta.DurationSec = d
	}
	return meta, nil
}

// ConvertToWebm converts the input video to WebM (AV1 via SVT-AV1 + Opus audio)
// at the original resolution using nearest-neighbour scaling
// (scale=flags=neighbor, crisp for pixel-art games), capped at 45 seconds. The
// output matches the videosnap-converter format/quality. The returned bytes are
// uploaded directly.
func ConvertToWebm(ctx context.Context, data []byte) ([]byte, error) {
	in, err := os.CreateTemp("", "video-in-*")
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

	out, err := os.CreateTemp("", "video-out-*.webm")
	if err != nil {
		return nil, fmt.Errorf("create output temp: %w", err)
	}
	outPath := out.Name()
	out.Close()
	defer os.Remove(outPath)

	// Match the videosnap-converter output: AV1 (SVT) CRF 36, preset 8,
	// film-grain=0, yuv420p, nearest-neighbour scaling at the original
	// resolution, capped at 45 seconds, Opus audio at 128k.
	args := []string{
		"-y",
		"-i", in.Name(),
		"-vf", "scale=flags=neighbor",
		"-t", "45",
		"-c:v", "libsvtav1",
		"-crf", "36",
		"-preset", "8",
		"-svtav1-params", "film-grain=0",
		"-pix_fmt", "yuv420p",
		"-c:a", "libopus",
		"-b:a", "128k",
		outPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg conversion failed: %w: %s", err, stderr.String())
	}

	converted, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read converted video: %w", err)
	}
	return converted, nil
}

// ResizeImage scales an image so its longest side is at most maxSize (never
// upscaling) preserving the aspect ratio, and re-encodes it as WebP. Used to
// normalize approved art (logos/covers 1024px, fanart 1920px).
func ResizeImage(ctx context.Context, data []byte, maxSize int) ([]byte, error) {
	in, err := os.CreateTemp("", "image-in-*")
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

	out, err := os.CreateTemp("", "image-out-*.webp")
	if err != nil {
		return nil, fmt.Errorf("create output temp: %w", err)
	}
	outPath := out.Name()
	out.Close()
	defer os.Remove(outPath)

	filter := fmt.Sprintf("scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease", maxSize, maxSize)
	args := []string{
		"-y",
		"-i", in.Name(),
		"-vf", filter,
		"-c:v", "libwebp",
		"-quality", "90",
		outPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg resize failed: %w: %s", err, stderr.String())
	}

	resized, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read resized image: %w", err)
	}
	return resized, nil
}