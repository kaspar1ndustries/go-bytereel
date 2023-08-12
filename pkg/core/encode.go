package core

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	cfg "github.com/1F47E/go-bytereel/pkg/config"
	"github.com/1F47E/go-bytereel/pkg/job"
	"github.com/1F47E/go-bytereel/pkg/logger"
	"github.com/1F47E/go-bytereel/pkg/meta"
	"github.com/1F47E/go-bytereel/pkg/tui"
	"github.com/1F47E/go-bytereel/pkg/video"
)

// 1. read file into buffer by chunks
// 2. encode chunks to images and write to files as png frames
// 3. encode frames into video
func (c *Core) Encode(path string) error {
	log := logger.Log.WithField("scope", "core encode")

	// open a file
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	// Estimate amount of frames by the file size
	// NOTE: read into buffer smaller then a frame to leave space for metadata
	readBuffer := make([]byte, cfg.SizeFrame-cfg.SizeMetadata)
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("error getting file info: %w", err)
	}
	size := fileInfo.Size()
	estimatedFrames := int(int(size) / len(readBuffer))
	log.Debug("Estimated frames:", estimatedFrames)

	// ===== START WORKERS

	jobs := make(chan job.JobEnc)
	numCpu := runtime.NumCPU()

	wg := sync.WaitGroup{}
	for i := 0; i <= numCpu; i++ {
		wg.Add(1)
		i := i
		go func() {
			c.worker.WorkerEncode(i, jobs)
			wg.Done()
		}()
	}

	// init metadata with filename and timestamp
	md := meta.New(path)
	frameCnt := 1

	// job object will be updated with copy of the buffer and send to the channel
	j := job.New(md, frameCnt)

	// read file into the buffer by chunks
loop:
	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
			n, err := file.Read(readBuffer)
			if err != nil {
				if err == io.EOF {
					log.Debug("EOF")
					break loop
				}
				return fmt.Errorf("error reading file: %w", err)
			}
			// copy the buffer to the job
			j.Update(readBuffer, n, frameCnt)
			log.Debugf("Sending job for frame %d: %s\n", frameCnt, j.Print())
			// this will block untill available worker pick it up
			log.Debug(j.Print())
			jobs <- j

			// update progress bar with % of frames processed
			percent := float64(frameCnt) / float64(estimatedFrames)
			c.eventsCh <- tui.NewEventBar(fmt.Sprintf("Encoding %d/%d frames", frameCnt, estimatedFrames), percent)

			frameCnt++
		}
	}

	// expected all the workers to finish and exit
	close(jobs)

	// wait for all the files to be processed
	wg.Wait()
	log.Debug("All workers done")

	// ====== VIDEO ENCODING

	// update TUI with spinner
	c.eventsCh <- tui.NewEventSpin("Saving video... ")

	// Call ffmpeg to encode frames into video
	err = video.EncodeFrames(c.ctx)
	if err != nil {
		return fmt.Errorf("error encoding frames into video: %w", err)
	}

	// clean up tmp/out dir
	err = os.RemoveAll("tmp/out")
	if err != nil {
		return fmt.Errorf("error removing tmp/out dir: %w", err)
	}
	log.Debug("\nVideo encoded")

	// update TUI
	c.eventsCh <- tui.NewEventText("Video encoded!")
	time.Sleep(1 * time.Second) // wait for TUI to update

	return nil
}
