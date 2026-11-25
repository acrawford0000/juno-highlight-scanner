package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type videoStream struct {
	Width       int
	Height      int
	PixelFormat string `json:"pix_fmt"`
}

func probe(ctx context.Context, path string) (videoStream, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,pix_fmt", "-of", "json", "-i", path)
	output, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return videoStream{}, fmt.Errorf("ffprobe: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return videoStream{}, fmt.Errorf("ffprobe: %w", err)
	}
	var data struct{ Streams []videoStream }
	if err := json.Unmarshal(output, &data); err != nil {
		return videoStream{}, err
	}
	if len(data.Streams) == 0 {
		return videoStream{}, fmt.Errorf("no video stream")
	}
	video := data.Streams[0]
	if video.Height <= 0 || abs(video.Width*9-video.Height*16) > video.Height/10 {
		return videoStream{}, fmt.Errorf("expected a 16:9 recording, got %dx%d", video.Width, video.Height)
	}
	return video, nil
}

type samplingResult struct {
	frames  int
	elapsed time.Duration
}

func sampleVideo(ctx context.Context, path string, video videoStream, interval float64, keyframes, cuda bool,
	onFrame func(float64, []byte) error) (samplingResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := time.Now()
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "info", "-noautorotate"}
	if cuda {
		args = append(args, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda")
	} else {
		args = append(args, "-threads", "4")
	}
	selectFilter := fmt.Sprintf("select=isnan(prev_selected_t)+gt(floor((t+0.000001)/%[1]s)\\,floor((prev_selected_t+0.000001)/%[1]s))",
		strconv.FormatFloat(interval, 'f', -1, 64))
	if keyframes {
		args = append(args, "-discard:v", "nokey", "-skip_frame", "nokey")
		selectFilter = "select=eq(key\\,1)"
	}
	if cuda {
		pixelFormat := "nv12"
		if video.PixelFormat == "yuv420p10le" {
			pixelFormat = "p010le"
		}
		selectFilter += ",hwdownload,format=" + pixelFormat
	}
	if video.Width != 1920 || video.Height != 1080 {
		selectFilter += ",scale=1920:1080:flags=bilinear"
	}
	filter := fmt.Sprintf("%s,crop=%d:%d:iw-ow:0,format=rgb24,showinfo=checksum=0",
		selectFilter, frameWidth, frameHeight)
	args = append(args, "-i", path, "-map", "0:v:0", "-an", "-sn", "-dn",
		"-filter_threads", "2", "-vf", filter, "-fps_mode", "passthrough",
		"-c:v", "rawvideo", "-threads:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	raw, err := cmd.StdoutPipe()
	if err != nil {
		return samplingResult{}, err
	}
	defer raw.Close()
	log, err := cmd.StderrPipe()
	if err != nil {
		return samplingResult{}, err
	}
	defer log.Close()
	if err := cmd.Start(); err != nil {
		return samplingResult{}, err
	}
	timestamps := make(chan float64, 8)
	logDone := make(chan string, 1)
	go readFrameLog(ctx, log, timestamps, logDone)

	result := samplingResult{}
	data := make([]byte, frameWidth*frameHeight*3)
	var readErr error
	for {
		_, err := io.ReadFull(raw, data)
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			cancel()
			break
		}
		select {
		case stamp, ok := <-timestamps:
			if !ok {
				readErr = fmt.Errorf("FFmpeg returned an image without a timestamp")
				cancel()
			} else {
				readErr = onFrame(stamp, data)
				if readErr != nil {
					cancel()
				} else {
					result.frames++
				}
			}
		case <-ctx.Done():
			readErr = ctx.Err()
		}
		if readErr != nil {
			break
		}
	}
	// Drain any timestamp for a frame FFmpeg logged just before a failed write.
	for range timestamps {
	}
	details := <-logDone
	waitErr := cmd.Wait()
	result.elapsed = time.Since(start)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return result, readErr
	}
	if waitErr != nil {
		return result, fmt.Errorf("FFmpeg: %w%s", waitErr, details)
	}
	if readErr != nil {
		return result, readErr
	}
	if result.frames == 0 {
		return result, fmt.Errorf("FFmpeg returned no video frames")
	}
	return result, nil
}

func abs(value int) int {
	return max(value, -value)
}

func readFrameLog(ctx context.Context, log io.Reader, timestamps chan<- float64, done chan<- string) {
	defer close(timestamps)
	var recent []string
	lines := bufio.NewScanner(log)
	lines.Buffer(make([]byte, 4096), 1024*1024)
	for lines.Scan() {
		line := lines.Text()
		if strings.Contains(line, "Parsed_showinfo") {
			_, timeValue, ok := strings.Cut(line, "pts_time:")
			if ok {
				fields := strings.Fields(timeValue)
				if len(fields) > 0 {
					if stamp, err := strconv.ParseFloat(fields[0], 64); err == nil {
						select {
						case timestamps <- stamp:
						case <-ctx.Done():
						}
					}
				}
			}
			continue
		}
		recent = append(recent, line)
		if len(recent) > 6 {
			recent = recent[1:]
		}
	}
	if err := lines.Err(); err != nil {
		recent = append(recent, err.Error())
	}
	message := ""
	if len(recent) > 0 {
		message = "\n  " + strings.Join(recent, "\n  ")
	}
	done <- message
}
