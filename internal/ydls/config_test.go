package ydls

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/wader/ydls/internal/ffmpeg"
	"github.com/wader/ydls/internal/leaktest"
	"github.com/wader/ydls/internal/timerange"
)

type bufferCloser struct {
	bytes.Buffer
}

func (bc *bufferCloser) Close() error {
	return nil
}

func TestFFmpegHasFormatsCodecs(t *testing.T) {
	if !testFfmpeg {
		t.Skip("TEST_FFMPEG env not set")
	}

	codecs := map[ffmpeg.Codec]string{}

	ydls := ydlsFromEnv(t)

	// collect unique codecs
	for _, f := range ydls.Config.Formats {
		for _, s := range f.Streams {
			for _, c := range s.Codecs {
				codecName := firstNonEmpty(ydls.Config.CodecMap[c.Name], c.Name)
				if s.Media == MediaAudio {
					codecs[ffmpeg.AudioCodec(codecName)] = "a:"
				} else if s.Media == MediaVideo {
					codecs[ffmpeg.VideoCodec(codecName)] = "a:"
				}
			}
		}
	}

	dummy, dummyErr := ffmpeg.Dummy("matroska", "mp3", "h264")
	if dummyErr != nil {
		log.Fatal(dummyErr)
	}
	dummyBuf, dummyBufErr := ioutil.ReadAll(dummy)
	if dummyBufErr != nil {
		log.Fatal(dummyBufErr)
	}

	for codec, specifier := range codecs {
		t.Logf("Testing: %v", codec)

		dummyReader := bytes.NewReader(dummyBuf)

		output := &bufferCloser{}

		ffmpegP := &ffmpeg.FFmpeg{
			Streams: []ffmpeg.Stream{
				ffmpeg.Stream{
					Maps: []ffmpeg.Map{
						ffmpeg.Map{
							Input:     ffmpeg.Reader{Reader: dummyReader},
							Specifier: specifier,
							Codec:     codec,
						},
					},
					Format: ffmpeg.Format{Name: "matroska"},
					Output: ffmpeg.Writer{Writer: output},
				},
			},
			DebugLog: nil, //log.New(os.Stdout, "debug> ", 0),
			Stderr:   nil, //writelogger.New(log.New(os.Stdout, "stderr> ", 0), ""),
		}

		if err := ffmpegP.Start(context.Background()); err != nil {
			t.Errorf("ffmpeg start failed for %s: %v", codec, err)
		} else if err := ffmpegP.Wait(); err != nil {
			t.Errorf("ffmpeg wait failed for %s: %v", codec, err)
		}
	}

}

func TestFormats(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	ydls := ydlsFromEnv(t)

	for _, c := range []struct {
		url              string
		audioOnly        bool
		expectedFilename string
	}{
		{soundcloudTestAudioURL, true, "BIS Radio Show #793 with The Drifter"},
		{youtubeTestVideoURL, false, "TEST VIDEO"},
	} {
		for formatName, f := range ydls.Config.Formats {
			func() {
				defer leaktest.Check(t)()

				hasVideo := false
				for _, s := range f.Streams {
					if s.Media == MediaVideo {
						hasVideo = true
						break
					}
				}

				if c.audioOnly && hasVideo {
					t.Logf("%s: %s: skip, test stream is audio only\n", c.url, formatName)
					return
				}

				ctx, cancelFn := context.WithCancel(context.Background())

				dr, err := ydls.Download(
					ctx,
					DownloadOptions{
						URL:       c.url,
						Format:    formatName,
						TimeRange: timerange.TimeRange{Stop: 1 * time.Second},
					},
					nil,
				)
				if err != nil {
					cancelFn()
					t.Errorf("%s: %s: download failed: %s", c.url, formatName, err)
					return
				}

				const limitBytes = 10 * 1024 * 1024
				pi, err := ffmpeg.Probe(ctx, ffmpeg.Reader{Reader: io.LimitReader(dr.Media, limitBytes)}, nil, nil)
				dr.Media.Close()
				dr.Wait()
				cancelFn()
				if err != nil {
					t.Errorf("%s: %s: probe failed: %s", c.url, formatName, err)
					return
				}

				if !strings.HasPrefix(dr.Filename, c.expectedFilename) {
					t.Errorf("%s: %s: expected filename '%s' found '%s'", c.url, formatName, c.expectedFilename, dr.Filename)
					return
				}
				if f.MIMEType != dr.MIMEType {
					t.Errorf("%s: %s: expected MIME type '%s' found '%s'", c.url, formatName, f.MIMEType, dr.MIMEType)
					return
				}
				if !f.Formats.Member(pi.FormatName()) {
					t.Errorf("%s: %s: expected format %s found %s", c.url, formatName, f.Formats, pi.FormatName())
					return
				}

				for i := 0; i < len(f.Streams); i++ {
					formatStream := f.Streams[i]
					probeStream := pi.Streams[i]

					if !formatStream.CodecNames.Member(probeStream.CodecName) {
						t.Errorf("%s: %s: expected codec %s found %s", c.url, formatName, formatStream.CodecNames, probeStream.CodecName)
						return
					}
				}

				if f.Prepend == "id3v2" {
					if pi.Format.Tags.Title == "" {
						t.Errorf("%s: %s: expected id3v2 title tag", c.url, formatName)
					}
				}

				t.Logf("%s: %s: OK (probed %s)\n", c.url, formatName, pi)
			}()
		}
	}
}

func TestRawFormat(t *testing.T) {
	if !testNetwork || !testFfmpeg || !testYoutubeldl {
		t.Skip("TEST_NETWORK, TEST_FFMPEG, TEST_YOUTUBEDL env not set")
	}

	ydls := ydlsFromEnv(t)

	defer leaktest.Check(t)()

	ctx, cancelFn := context.WithCancel(context.Background())

	dr, err := ydls.Download(ctx, DownloadOptions{URL: youtubeTestVideoURL}, nil)
	if err != nil {
		cancelFn()
		t.Errorf("%s: %s: download failed: %s", youtubeTestVideoURL, "raw", err)
		return
	}

	pi, err := ffmpeg.Probe(ctx, ffmpeg.Reader{Reader: io.LimitReader(dr.Media, 10*1024*1024)}, nil, nil)
	dr.Media.Close()
	dr.Wait()
	cancelFn()
	if err != nil {
		t.Errorf("%s: %s: probe failed: %s", youtubeTestVideoURL, "raw", err)
		return
	}

	t.Logf("%s: %s: OK (probed %s)\n", youtubeTestVideoURL, "raw", pi)
}

func TestFindByFormatCodecs(t *testing.T) {
	ydls := ydlsFromEnv(t)

	for i, c := range []struct {
		format   string
		codecs   []string
		expected string
	}{
		{"mp3", []string{"mp3"}, "mp3"},
		{"flac", []string{"flac"}, "flac"},
		{"mov", []string{"alac"}, "alac"},
		{"mov", []string{"aac", "h264"}, "mp4"},
		{"matroska", []string{"vorbis", "vp8"}, "mkv"},
		{"matroska", []string{"opus", "vp9"}, "mkv"},
		{"matroska", []string{"aac", "h264"}, "mkv"},
		{"matroska", []string{"vp8", "vorbis"}, "mkv"},
		{"matroska", []string{"vorbis", "vp8"}, "mkv"},
		{"mpegts", []string{"aac", "h264"}, "ts"},
		{"", []string{}, ""},
	} {
		_, actualFormatName := ydls.Config.Formats.FindByFormatCodecs(c.format, c.codecs)
		if c.expected != actualFormatName {
			t.Errorf("%d: expected format %s, got %s", i, c.expected, actualFormatName)
		}
	}

}
