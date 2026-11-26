// Command mp4-faststart finds MP4 videos in the R2 bucket whose moov box sits
// after the media data and, with -apply, remuxes them in place with the moov
// box first (ffmpeg -c copy -movflags +faststart: no re-encoding), so clients
// can stream them from the CDN without first fetching the end of the file.
//
// By default it is a dry run: it only reads box headers (a few small range
// requests per object) and reports which videos need the remux.
//
// Credentials come from the same environment as the service: R2_ACCOUNT_ID,
// R2_ACCESS_KEY, R2_SECRET_KEY and R2_BUCKET_NAME.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"

	"github.com/rs/zerolog"

	"neoassets/pkg/r2"
	"neoassets/pkg/video"
)

// mediaCacheControl matches the header the service writes on approved media
// (internal/services.mediaCacheControl): the keys are immutable.
const mediaCacheControl = "public, max-age=31536000, immutable"

type outcome string

const (
	outcomeOK       outcome = "ok"        // moov already first
	outcomeNeeds    outcome = "needs"     // dry run: would be remuxed
	outcomeRemuxed  outcome = "remuxed"   // rewritten in place
	outcomeTooLarge outcome = "too-large" // over -max-size, left alone
	outcomeFailed   outcome = "failed"
)

type result struct {
	Key     string
	Outcome outcome
	Size    int64
	NewSize int64
	Err     error
}

type options struct {
	Apply   bool
	MaxSize int64
}

func main() {
	prefix := flag.String("prefix", "media/games/", "only objects under this key prefix")
	apply := flag.Bool("apply", false, "remux and overwrite the objects (default: dry run, report only)")
	workers := flag.Int("workers", 4, "objects processed in parallel")
	limit := flag.Int("limit", 0, "stop after this many objects need a remux (0: no limit)")
	maxSizeMB := flag.Int64("max-size-mb", 200, "skip videos larger than this")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	env := func(k string) string { return strings.TrimSpace(os.Getenv(k)) }
	accountID, accessKey, secretKey, bucket := env("R2_ACCOUNT_ID"), env("R2_ACCESS_KEY"), env("R2_SECRET_KEY"), env("R2_BUCKET_NAME")
	if accountID == "" || accessKey == "" || secretKey == "" || bucket == "" {
		fmt.Fprintln(os.Stderr, "R2_ACCOUNT_ID, R2_ACCESS_KEY, R2_SECRET_KEY and R2_BUCKET_NAME must be set")
		os.Exit(2)
	}
	if *apply {
		if err := checkFFmpeg(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	client, err := r2.NewClient(ctx, accountID, accessKey, secretKey, bucket, zerolog.Nop())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mode := "DRY RUN (nothing is written; pass -apply to remux)"
	if *apply {
		mode = "APPLY (objects are overwritten in place)"
	}
	fmt.Printf("bucket %s, prefix %q: %s\n", bucket, *prefix, mode)

	objects, err := client.ListKeysWithSize(ctx, *prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var mp4s []r2.ObjectInfo
	for _, o := range objects {
		if strings.HasSuffix(strings.ToLower(o.Key), ".mp4") {
			mp4s = append(mp4s, o)
		}
	}
	fmt.Printf("%d MP4 objects to check\n", len(mp4s))

	opts := options{Apply: *apply, MaxSize: *maxSizeMB << 20}
	counts := run(ctx, client, mp4s, opts, *workers, *limit, func(r result) {
		switch r.Outcome {
		case outcomeOK:
		case outcomeNeeds:
			fmt.Printf("NEEDS    %s (%s)\n", r.Key, mb(r.Size))
		case outcomeRemuxed:
			fmt.Printf("REMUXED  %s (%s -> %s)\n", r.Key, mb(r.Size), mb(r.NewSize))
		case outcomeTooLarge:
			fmt.Printf("SKIPPED  %s (%s, over -max-size-mb)\n", r.Key, mb(r.Size))
		case outcomeFailed:
			fmt.Printf("FAILED   %s: %v\n", r.Key, r.Err)
		}
	})

	fmt.Printf("\nalready faststart: %d, need remux: %d, remuxed: %d, too large: %d, failed: %d\n",
		counts[outcomeOK], counts[outcomeNeeds], counts[outcomeRemuxed], counts[outcomeTooLarge], counts[outcomeFailed])
	if ctx.Err() != nil {
		fmt.Println("interrupted")
		os.Exit(130)
	}
	if counts[outcomeFailed] > 0 {
		os.Exit(1)
	}
}

// run checks (and with opts.Apply, remuxes) the objects with a pool of
// workers, calling report for each result, and returns the count per outcome.
// With limit > 0 it stops scheduling once that many objects needed a remux.
func run(ctx context.Context, client r2.Client, objects []r2.ObjectInfo, opts options, workers, limit int, report func(result)) map[outcome]int {
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan r2.ObjectInfo)
	results := make(chan result)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range jobs {
				results <- process(ctx, client, o, opts)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, o := range objects {
			select {
			case jobs <- o:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	counts := map[outcome]int{}
	for r := range results {
		// Work cut short by -limit (or Ctrl+C) was never written: not a failure.
		if r.Outcome == outcomeFailed && ctx.Err() != nil && errors.Is(r.Err, context.Canceled) {
			continue
		}
		counts[r.Outcome]++
		report(r)
		if limit > 0 && counts[outcomeNeeds]+counts[outcomeRemuxed] >= limit {
			cancel()
		}
	}
	return counts
}

// process checks one object and, when opts.Apply is set and its moov box is
// not first, remuxes it and overwrites it after verifying the result.
func process(ctx context.Context, client r2.Client, o r2.ObjectInfo, opts options) result {
	res := result{Key: o.Key, Size: o.Size}
	fail := func(err error) result {
		res.Outcome, res.Err = outcomeFailed, err
		return res
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}

	first, err := video.MoovFirst(func(off, n int64) ([]byte, error) {
		return client.ReadObjectRange(ctx, o.Key, off, n)
	}, o.Size)
	if err != nil {
		return fail(fmt.Errorf("read boxes: %w", err))
	}
	if first {
		res.Outcome = outcomeOK
		return res
	}
	if !opts.Apply {
		res.Outcome = outcomeNeeds
		return res
	}
	if o.Size > opts.MaxSize {
		res.Outcome = outcomeTooLarge
		return res
	}

	data, err := client.DownloadObject(ctx, o.Key, opts.MaxSize)
	if errors.Is(err, r2.ErrObjectTooLarge) {
		res.Outcome = outcomeTooLarge
		return res
	}
	if err != nil {
		return fail(err)
	}
	// The object may have changed since it was listed.
	if first, err := video.MoovFirstBytes(data); err != nil {
		return fail(fmt.Errorf("read boxes: %w", err))
	} else if first {
		res.Outcome = outcomeOK
		return res
	}

	remuxed, err := video.RemuxFaststart(ctx, data)
	if err != nil {
		return fail(err)
	}
	if err := verifyRemux(ctx, data, remuxed); err != nil {
		return fail(fmt.Errorf("remux check failed, object left untouched: %w", err))
	}
	if err := client.UploadFileCached(ctx, o.Key, "video/mp4", mediaCacheControl, bytes.NewReader(remuxed)); err != nil {
		return fail(err)
	}
	res.Outcome, res.NewSize = outcomeRemuxed, int64(len(remuxed))
	return res
}

// verifyRemux makes sure a remuxed file is faststart and still the same video
// (codec, resolution, duration, about the same size) before it replaces the
// original.
func verifyRemux(ctx context.Context, before, after []byte) error {
	first, err := video.MoovFirstBytes(after)
	if err != nil {
		return err
	}
	if !first {
		return errors.New("moov box is still after the media data")
	}
	a, err := video.Probe(ctx, before)
	if err != nil {
		return fmt.Errorf("probe original: %w", err)
	}
	b, err := video.Probe(ctx, after)
	if err != nil {
		return fmt.Errorf("probe remuxed: %w", err)
	}
	if a.Codec != b.Codec || a.Width != b.Width || a.Height != b.Height {
		return fmt.Errorf("stream changed: %s %dx%d -> %s %dx%d", a.Codec, a.Width, a.Height, b.Codec, b.Width, b.Height)
	}
	if math.Abs(a.DurationSec-b.DurationSec) > 0.5 {
		return fmt.Errorf("duration changed: %.2fs -> %.2fs", a.DurationSec, b.DurationSec)
	}
	// A stream copy only moves boxes; a large size change means data was lost.
	if ratio := float64(len(after)) / float64(len(before)); ratio < 0.95 || ratio > 1.05 {
		return fmt.Errorf("size changed too much: %d -> %d bytes", len(before), len(after))
	}
	return nil
}

func mb(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// checkFFmpeg makes sure ffmpeg and ffprobe are installed before -apply
// starts downloading anything.
func checkFFmpeg() error {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found in PATH (needed for -apply)", bin)
		}
	}
	return nil
}
