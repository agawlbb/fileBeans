package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type mediaStreamInfo struct {
	FieldOrder  string `json:"field_order"`
	Index       int    `json:"index"`
	Type        string `json:"codec_type"`
	Codec       string `json:"codec_name"`
	Profile     string `json:"profile"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Pixel       string `json:"pix_fmt"`
	Frames      string `json:"nb_frames"`
	ReadFrames  string `json:"nb_read_frames"`
	Rate        string `json:"avg_frame_rate"`
	SAR         string `json:"sample_aspect_ratio"`
	Channels    int    `json:"channels"`
	Bitrate     string `json:"bit_rate"`
	Transfer    string `json:"color_transfer"`
	Primaries   string `json:"color_primaries"`
	Space       string `json:"color_space"`
	Disposition struct {
		Attached int `json:"attached_pic"`
	} `json:"disposition"`
	SideData []struct {
		Rotation int `json:"rotation"`
	} `json:"side_data_list"`
}
type mediaInfo struct {
	Streams []mediaStreamInfo `json:"streams"`
	Format  struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
		Bitrate  string `json:"bit_rate"`
	} `json:"format"`
}
type mediaPlan struct {
	Video     mediaStreamInfo
	Audio     *mediaStreamInfo
	Image     bool
	Copy      bool
	Extension string
	Duration  float64
	HDR       bool
}

// Playlist/network demuxers are deliberately excluded. Media cannot fetch URLs
// or enumerate arbitrary server-side files while being probed/decoded.
const safeMediaFormats = "mov,matroska,webm,avi,asf,mpegts,mpeg,mpegvideo,m4v,h264,hevc,flv,ogg,image2,jpeg_pipe,png_pipe,webp_pipe,gif,apng,bmp_pipe,tiff_pipe,jpegxl_pipe,avif"

func mediaInputArgs() []string {
	return []string{"-protocol_whitelist", "file,pipe", "-format_whitelist", safeMediaFormats}
}
func (s *Service) probeMedia(ctx context.Context, input *os.File, count bool) (mediaInfo, error) {
	var info mediaInfo
	c := s.config.Normalization.defaults()
	if _, e := input.Seek(0, 0); e != nil {
		return info, e
	}
	args := []string{"-v", "error"}
	args = append(args, mediaInputArgs()...)
	if count {
		args = append(args, "-count_frames")
	}
	args = append(args, "-show_entries", "format=format_name,duration,bit_rate:stream=index,codec_type,codec_name,profile,width,height,pix_fmt,nb_frames,nb_read_frames,avg_frame_rate,field_order,sample_aspect_ratio,channels,bit_rate,color_transfer,color_primaries,color_space:stream_disposition=attached_pic:stream_side_data=rotation", "-of", "json", "/proc/self/fd/3")
	command := exec.CommandContext(ctx, c.FFprobe, args...)
	command.ExtraFiles = []*os.File{input}
	command.Stderr = io.Discard
	var output limitedBuffer
	output.limit = 1 << 20
	command.Stdout = &output
	if e := command.Run(); e != nil {
		return info, fmt.Errorf("%w: media cannot be inspected", ErrUnsupported)
	}
	if e := json.Unmarshal(output.data, &info); e != nil {
		return info, ErrUnsupported
	}
	return info, nil
}

type limitedBuffer struct {
	data  []byte
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data)+n > b.limit {
		return 0, errors.New("tool output exceeded limit")
	}
	b.data = append(b.data, p...)
	return n, nil
}
func makeMediaPlan(info mediaInfo, name string) (mediaPlan, error) {
	var p mediaPlan
	found := false
	for _, v := range info.Streams {
		if v.Type == "video" && v.Disposition.Attached == 0 && !found {
			p.Video = v
			found = true
		}
		if v.Type == "audio" && p.Audio == nil {
			a := v
			p.Audio = &a
		}
	}
	if !found || p.Video.Width <= 0 || p.Video.Height <= 0 {
		return p, ErrUnsupported
	}
	p.Duration, _ = strconv.ParseFloat(info.Format.Duration, 64)
	switch info.Format.Name {
	case "jpeg_pipe", "png_pipe", "webp_pipe", "bmp_pipe", "tiff_pipe", "gif", "apng", "avif", "image2", "jpegxl_pipe":
		p.Image = true
	}
	p.HDR = p.Video.Transfer == "smpte2084" || p.Video.Transfer == "arib-std-b67"
	ext := strings.ToLower(filepath.Ext(name))
	if p.Image {
		frames, _ := strconv.Atoi(p.Video.Frames)
		read, _ := strconv.Atoi(p.Video.ReadFrames)
		if info.Format.Name == "gif" || info.Format.Name == "apng" || frames > 1 || read > 1 {
			if ext != ".gif" && ext != ".png" && ext != ".apng" && ext != ".webp" {
				return p, ErrUnsupported
			}
			p.Copy = true
			p.Extension = ext
			return p, nil
		}
		alpha := strings.Contains(p.Video.Pixel, "rgba") || strings.Contains(p.Video.Pixel, "bgra") || strings.HasPrefix(p.Video.Pixel, "yuva") || strings.HasPrefix(p.Video.Pixel, "gbrap") || strings.HasPrefix(p.Video.Pixel, "ya") || p.Video.Pixel == "pal8"
		if alpha {
			p.Extension = ".png"
			p.Copy = p.Video.Codec == "png"
		} else {
			p.Extension = ".jpg"
			p.Copy = p.Video.Codec == "mjpeg"
		}
		return p, nil
	}
	if p.Duration <= 0 || p.Duration > 365*24*3600 {
		return p, ErrUnsupported
	}
	p.Extension = ".mp4"
	p.HDR = p.Video.Transfer == "smpte2084" || p.Video.Transfer == "arib-std-b67"
	short, long := p.Video.Width, p.Video.Height
	if short > long {
		short, long = long, short
	}
	bitrate, _ := strconv.ParseInt(info.Format.Bitrate, 10, 64)
	// Preserve already compact, compatible encodes. The generated fingerprint
	// provides the stronger no-reconversion guarantee for our own outputs.
	p.Copy = (p.Video.FieldOrder == "" || p.Video.FieldOrder == "unknown" || p.Video.FieldOrder == "progressive") && ext == ".mp4" && p.Video.Codec == "hevc" && !p.HDR && short <= 1080 && long <= 2560 && bitrate > 0 && bitrate <= 8_000_000 && (p.Video.SAR == "1:1" || p.Video.SAR == "" || p.Video.SAR == "N/A")
	for _, d := range p.Video.SideData {
		if d.Rotation != 0 {
			p.Copy = false
		}
	}
	if p.Audio != nil && (p.Audio.Codec != "aac" || p.Audio.Channels > 2) {
		p.Copy = false
	}
	return p, nil
}
func encodeArgs(p mediaPlan) []string {
	args := []string{"-map", fmt.Sprintf("0:%d", p.Video.Index)}
	if p.Copy && !p.Image {
		return append(args, "-map", "0:a:0?", "-c:v", "copy", "-c:a", "copy", "-tag:v", "hvc1", "-movflags", "+faststart")
	}
	if p.Image {
		args = append(args, "-frames:v", "1")
		if p.HDR {
			args = append(args, "-vf", "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=hable:desat=0,zscale=p=bt709:t=bt709:m=bt709:r=full")
		}
		if p.Extension == ".png" {
			return append(args, "-c:v", "png")
		}
		return append(args, "-c:v", "mjpeg", "-q:v", "2", "-pix_fmt", "yuvj444p")
	}
	if p.Audio != nil {
		args = append(args, "-map", fmt.Sprintf("0:%d", p.Audio.Index), "-c:a", "aac", "-b:a", "192k", "-ac", "2")
	} else {
		args = append(args, "-an")
	}
	filters := []string{"bwdif=mode=send_frame:parity=auto:deint=interlaced"}
	if p.HDR {
		filters = append(filters, "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=hable:desat=0,zscale=p=bt709:t=bt709:m=bt709:r=tv")
	}
	// First materialize non-square pixels, then apply the orientation-aware cap.
	filters = append(filters, "scale=w='max(2,trunc(iw*sar/2)*2)':h='max(2,trunc(ih/2)*2)',setsar=1", "scale=w='if(gte(iw,ih),min(iw,2560),min(iw,1080))':h='if(gte(iw,ih),min(ih,1080),min(ih,2560))':force_original_aspect_ratio=decrease:force_divisible_by=2")
	args = append(args, "-vf", strings.Join(filters, ","), "-c:v", "libx265", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p10le", "-threads", "4", "-x265-params", "pools=4:frame-threads=2:log-level=error", "-tag:v", "hvc1", "-movflags", "+faststart")
	if p.HDR {
		args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}
	return args
}
func (s *Service) encodeMedia(ctx context.Context, input *os.File, output string, p mediaPlan, progress func(int64) error) error {
	if _, e := input.Seek(0, 0); e != nil {
		return e
	}
	if p.Copy && p.Image {
		f, e := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(f, input)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	c := s.config.Normalization.defaults()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-filter_threads", "2"}
	args = append(args, mediaInputArgs()...)
	args = append(args, "-i", "/proc/self/fd/3")
	args = append(args, encodeArgs(p)...)
	args = append(args, "-progress", "pipe:1", "-nostats", output)
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(child, c.FFmpeg, args...)
	command.ExtraFiles = []*os.File{input}
	command.Stderr = io.Discard
	pipe, e := command.StdoutPipe()
	if e != nil {
		return e
	}
	if e = command.Start(); e != nil {
		return e
	}
	scanner := bufio.NewScanner(pipe)
	var progressError error
	last := time.Time{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "out_time_us=") && time.Since(last) >= time.Second {
			value, _ := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
			if e = progress(value / 1000); e != nil {
				progressError = e
				cancel()
			}
			last = time.Now()
		}
	}
	waitError := command.Wait()
	if progressError != nil {
		return progressError
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanner.Err() != nil {
		return scanner.Err()
	}
	if waitError != nil {
		return fmt.Errorf("%w: conversion failed", ErrUnsupported)
	}
	return nil
}
func (s *Service) validateMedia(ctx context.Context, path string, p mediaPlan) error {
	input, e := os.Open(path)
	if e != nil {
		return e
	}
	defer input.Close()
	info, e := s.probeMedia(ctx, input, false)
	if e != nil {
		return e
	}
	output, e := makeMediaPlan(info, path)
	if e != nil {
		return e
	}
	if p.Image {
		if output.Video.Width != p.Video.Width || output.Video.Height != p.Video.Height {
			// Image orientation may exchange width and height.
			if output.Video.Width != p.Video.Height || output.Video.Height != p.Video.Width {
				return ErrConflict
			}
		}
		return s.decodeNormalizedMedia(ctx, path)
	}
	if output.Video.Codec != "hevc" || output.Video.Width <= 0 || output.Video.Height <= 0 {
		return ErrUnsupported
	}
	short, long := output.Video.Width, output.Video.Height
	if short > long {
		short, long = long, short
	}
	if short > 1080 || long > 2560 {
		return ErrUnsupported
	}
	delta := output.Duration - p.Duration
	if delta < -0.5 || delta > 0.5 {
		return ErrConflict
	}
	if (p.Audio != nil) != (output.Audio != nil) {
		return ErrConflict
	}
	if output.Audio != nil && (output.Audio.Codec != "aac" || output.Audio.Channels > 2) {
		return ErrUnsupported
	}
	if p.HDR && output.Video.Transfer != "bt709" {
		return ErrUnsupported
	}
	return s.decodeNormalizedMedia(ctx, path)
}

func (s *Service) decodeNormalizedMedia(ctx context.Context, path string) error {
	// Decode the entire output to catch truncation/corruption before publication.
	c := s.config.Normalization.defaults()
	args := []string{"-v", "error", "-nostdin", "-xerror", "-threads", "2"}
	args = append(args, mediaInputArgs()...)
	args = append(args, "-i", path, "-map", "0:v:0", "-map", "0:a:0?", "-f", "null", "-")
	command := exec.CommandContext(ctx, c.FFmpeg, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if e := command.Run(); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnsupported
	}
	return nil
}
