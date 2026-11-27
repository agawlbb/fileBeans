package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const mediaWorkerDataLimit = uint64(640 << 20)
const mediaPacketLimit = 64 << 10

// The protocol carries capabilities (open descriptors), never executable names
// or paths into storage. A connection owns exactly one bounded operation.
type mediaRequest struct {
	Operation  string
	Plan       mediaPlan
	Limit      int64
	Count      bool
	Recovering bool
}

type mediaResponse struct {
	Info  mediaInfo
	Ready bool
	Error string
	Code  string
}

type mediaWorkerError struct{ message string }

func (e *mediaWorkerError) Error() string { return e.message }

func (s *Service) archiveCanStart(ctx context.Context, recovering bool) bool {
	return s.archiveAdmission(ctx, recovering) == nil
}

func (s *Service) archiveAdmission(ctx context.Context, recovering bool) error {
	if s.config.ArchiveMediaSocket == "" {
		if s.archiveMemory.canStart(time.Now(), recovering) {
			return nil
		}
		return errArchiveMemory
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := s.mediaCall(probe, mediaRequest{Operation: "ready", Recovering: recovering})
	if err != nil {
		return err
	}
	if !response.Ready {
		return errArchiveMemory
	}
	return nil
}

func (s *Service) probeArchiveMedia(ctx context.Context, input *os.File, count bool) (mediaInfo, error) {
	if s.config.ArchiveMediaSocket == "" {
		return s.probeMedia(ctx, input, count)
	}
	response, err := s.mediaCall(ctx, mediaRequest{Operation: "probe", Count: count}, input)
	return response.Info, err
}

func (s *Service) mediaCall(ctx context.Context, request mediaRequest, files ...*os.File) (mediaResponse, error) {
	var response mediaResponse
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unixpacket", s.config.ArchiveMediaSocket)
	if err != nil {
		return response, &mediaWorkerError{"Media worker unavailable; waiting to retry"}
	}
	defer connection.Close()
	conn := connection.(*net.UnixConn)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	data, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	fds := make([]int, len(files))
	for i, file := range files {
		fds[i] = int(file.Fd())
	}
	var rights []byte
	if len(fds) > 0 {
		rights = unix.UnixRights(fds...)
	}
	if _, _, err = conn.WriteMsgUnix(data, rights, nil); err != nil {
		return response, err
	}
	packet := make([]byte, mediaPacketLimit)
	n, _, flags, _, err := conn.ReadMsgUnix(packet, nil)
	if ctx.Err() != nil {
		return response, context.Cause(ctx)
	}
	if err != nil {
		return response, &mediaWorkerError{"Media worker disconnected; waiting to retry"}
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return response, errors.New("media response too large")
	}
	if err = json.Unmarshal(packet[:n], &response); err != nil {
		return response, err
	}
	switch response.Code {
	case "memory":
		return response, errArchiveMemory
	case "conflict":
		return response, ErrConflict
	case "unsupported":
		return response, ErrUnsupported
	case "space":
		return response, errNormalizationSpace
	}
	if response.Error != "" {
		switch response.Error {
		case "Video encoder ran out of memory", "Video encoding interrupted; waiting to retry", "Video encoding timed out", "Video encoder crashed", "Video encoding failed":
			return response, &mediaWorkerError{response.Error}
		default:
			return response, &mediaWorkerError{"Media processing failed; waiting to retry"}
		}
	}
	return response, nil
}

// RunMediaWorker needs no service configuration, database, or storage mount.
func RunMediaWorker(ctx context.Context, socket string) error {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return errors.New("media socket must be absolute and clean")
	}
	s := &Service{mediaWorker: true}
	s.archiveMemory.required = mediaWorkerDataLimit
	return s.serveMedia(ctx, socket)
}

func (s *Service) serveMedia(ctx context.Context, socket string) error {
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("media socket directory must be private (0700)")
	}
	// A file lock protects stale-socket recovery and prevents two executors.
	lock, err := os.OpenFile(socket+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("media worker already running")
	}
	if info, statErr := os.Lstat(socket); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("media socket path is not a socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socket, Net: "unixpacket"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var handlers sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); handlers.Wait() }()
	active := make(chan struct{}, 1)
	connections := make(chan struct{}, 16)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case connections <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-connections }()
			defer conn.Close()
			s.handleMedia(ctx, conn, active)
		}()
	}
}

func (s *Service) handleMedia(parent context.Context, conn *net.UnixConn, active chan struct{}) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	packet := make([]byte, mediaPacketLimit)
	oob := make([]byte, unix.CmsgSpace(8*4))
	n, controlN, flags, _, err := conn.ReadMsgUnix(packet, oob)
	if err != nil {
		return
	}
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	messages, err := unix.ParseSocketControlMessage(oob[:controlN])
	if err != nil {
		return
	}
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			return
		}
		for _, fd := range fds {
			unix.CloseOnExec(fd)
			files = append(files, os.NewFile(uintptr(fd), "media-capability"))
		}
	}
	respond := func(response mediaResponse) {
		data, err := json.Marshal(response)
		if err != nil || len(data) > mediaPacketLimit {
			data = []byte(`{"Error":"media response too large"}`)
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write(data)
	}
	var request mediaRequest
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || json.Unmarshal(packet[:n], &request) != nil {
		respond(mediaResponse{Error: "invalid media request"})
		return
	}
	if request.Operation == "ready" && len(files) == 0 {
		respond(mediaResponse{Ready: s.archiveMemory.canStart(time.Now(), request.Recovering)})
		return
	}
	want := 1
	if request.Operation == "encode" {
		want = 2
	}
	if len(files) != want || (request.Operation != "probe" && request.Operation != "encode" && request.Operation != "validate") {
		respond(mediaResponse{Error: "invalid media operation or descriptors"})
		return
	}
	for i, file := range files {
		info, err := file.Stat()
		if err != nil || (i == 0 && !info.Mode().IsRegular()) || (i == 1 && !info.IsDir()) {
			respond(mediaResponse{Error: "invalid media capability"})
			return
		}
	}
	if request.Operation != "probe" {
		switch request.Plan.Extension {
		case ".mp4", ".jpg", ".png", ".apng", ".webp", ".gif":
		default:
			respond(mediaResponse{Error: "invalid media extension"})
			return
		}
	}
	if request.Operation == "encode" && (request.Limit <= 0 || request.Limit > archiveOutputLimit) {
		respond(mediaResponse{Error: "invalid media output limit"})
		return
	}
	select {
	case active <- struct{}{}:
		defer func() { <-active }()
	default:
		respond(mediaResponse{Error: "media worker busy"})
		return
	}
	if !s.archiveMemory.canStart(time.Now(), false) {
		respond(mediaResponse{Code: "memory"})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	deadline, stop := context.WithTimeout(parent, 24*time.Hour)
	defer stop()
	ctx, cancel := context.WithCancelCause(deadline)
	defer cancel(nil)
	// EOF, cancellation, or unexpected further input revokes this operation.
	disconnected := make(chan struct{})
	go func() { defer close(disconnected); var b [1]byte; _, _ = conn.Read(b[:]); cancel(context.Canceled) }()
	defer func() { _ = conn.Close(); <-disconnected }()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.archiveMemory.cancelIfPressure(cancel) {
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-monitorDone }()
	var response mediaResponse
	switch request.Operation {
	case "probe":
		response.Info, err = s.probeMedia(ctx, files[0], request.Count)
	case "encode":
		err = s.encodeArchiveMedia(ctx, files[0], files[1], request.Plan, request.Limit)
	case "validate":
		err = s.validateArchiveFile(ctx, files[0], request.Plan)
	}
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	if err != nil {
		response.Error = "Media processing failed"
		var encoding *archiveEncodingError
		if errors.As(err, &encoding) {
			response.Error = encoding.publicMessage()
		}
		switch {
		case errors.Is(err, errArchiveMemory):
			response.Code = "memory"
		case errors.Is(err, ErrConflict):
			response.Code = "conflict"
		case errors.Is(err, ErrUnsupported):
			response.Code = "unsupported"
		case errors.Is(err, errNormalizationSpace):
			response.Code = "space"
		}
		if request.Operation == "encode" {
			_ = unix.Unlinkat(int(files[1].Fd()), "output"+request.Plan.Extension, 0)
		}
	}
	respond(response)
}
