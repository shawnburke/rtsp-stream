package streaming

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/Roverr/hotstreak"
	"github.com/Roverr/rtsp-stream/core/config"
	"github.com/google/uuid"
	"github.com/natefinch/lumberjack"
	"github.com/sirupsen/logrus"
)

// IProcessor is an interface describing a processor service
type IProcessor interface {
	NewProcess(path, URI string) *exec.Cmd
	NewStream(URI string) (*Stream, string, string)
	Restart(stream *Stream, path string) error
}

// Processor is the main type for creating new processes
type Processor struct {
	storeDir    string
	keepFiles   bool
	audio       bool
	loggingOpts config.ProcessLogging
}

// Type check
var _ IProcessor = (*Processor)(nil)

// NewProcessor creates a new instance of a processor
func NewProcessor(
	storeDir string,
	keepFiles bool,
	audio bool,
	loggingOpts config.ProcessLogging,
) *Processor {
	return &Processor{storeDir, audio, keepFiles, loggingOpts}
}

// getHLSFlags are for getting the flags based on the config context
func (p Processor) getHLSFlags() string {
	if p.keepFiles {
		return "append_list"
	}
	return "delete_segments+append_list"
}

// NewProcess creates only the process for the stream
func (p Processor) NewProcess(path, URI string) *exec.Cmd {
	os.MkdirAll(path, os.ModePerm)
	processCommands := []string{
		"-y",
		"-fflags",
		"nobuffer",
		"-rtsp_transport",
		"tcp",
		"-i",
		URI,
		"-vsync",
		"0",
		"-copyts",
		"-vcodec",
		"copy",
		"-movflags",
		"frag_keyframe+empty_moov",
	}
	if p.audio {
		processCommands = append(processCommands, "-an")
	}
	processCommands = append(processCommands,
		"-hls_flags",
		p.getHLSFlags(),
		"-f",
		"hls",
		"-segment_list_flags",
		"live",
		"-hls_time",
		"1",
		"-hls_list_size",
		"3",
		"-hls_segment_filename",
		fmt.Sprintf("%s/%%d.ts", path),
		fmt.Sprintf("%s/index.m3u8", path),
	)
	cmd := exec.Command("ffmpeg", processCommands...)
	return cmd
}

// NewStream creates a new transcoding process for ffmpeg
func (p Processor) NewStream(URI string) (*Stream, string, string) {
	id := uuid.New().String()
	path := fmt.Sprintf("%s/%s", p.storeDir, id)
	err := os.MkdirAll(path, os.ModePerm)
	if err != nil {
		logrus.Error(err)
		return nil, "", ""
	}
	cmd := p.NewProcess(path, URI)

	// Create nil pointer in case logging is not enabled
	cmdLogger := (*lumberjack.Logger)(nil)
	// Create logger otherwise
	if p.loggingOpts.Enabled {
		cmdLogger = &lumberjack.Logger{
			Filename:   fmt.Sprintf("%s/%s.log", p.loggingOpts.Directory, id),
			MaxSize:    p.loggingOpts.MaxSize,
			MaxBackups: p.loggingOpts.MaxBackups,
			MaxAge:     p.loggingOpts.MaxAge,
			Compress:   p.loggingOpts.Compress,
		}
		cmd.Stderr = cmdLogger
		cmd.Stdout = cmdLogger
	}
	stream := Stream{
		CMD:       cmd,
		Mux:       &sync.RWMutex{},
		Path:      fmt.Sprintf("/%s/index.m3u8", filepath.Join("stream", id)),
		StorePath: path,
		Streak: hotstreak.New(hotstreak.Config{
			Limit:      10,
			HotWait:    time.Minute * 2,
			ActiveWait: time.Minute * 4,
		}).Activate(),
		OriginalURI: URI,
		KeepFiles:   p.keepFiles,
		Logger:      cmdLogger,
	}
	logrus.Debugf("Created stream with storepath %s", stream.StorePath)
	return &stream, fmt.Sprintf("%s/index.m3u8", path), id
}

// Restart uses the processor to restart a given stream
func (p Processor) Restart(stream *Stream, path string) error {
	stream.Mux.Lock()
	defer stream.Mux.Unlock()
	stream.CMD = p.NewProcess(stream.StorePath, stream.OriginalURI)
	if p.loggingOpts.Enabled {
		stream.CMD.Stderr = stream.Logger
		stream.CMD.Stdout = stream.Logger
	}
	stream.Streak.Activate()
	go p.spawnBackgroundRunProcess(stream.CMD)
	logrus.Infof("%s has been restarted", path)
	return nil
}

func (p Processor) spawnBackgroundRunProcess(cmd *exec.Cmd) {
	err := cmd.Run()
	if err != nil {
		logrus.Error(err)
	}
}
