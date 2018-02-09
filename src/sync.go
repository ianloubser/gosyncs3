package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/radovskyb/watcher"
)

type EventProcessPool struct {
	// store all the events queued for S3 sync to allow for buffering
	incomingEvent chan watcher.Event
	events        []watcher.Event
	delay         *time.Timer
	batchSize     int64
}

var eventPool EventProcessPool
var sync Sync

func configureLogging(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		fmt.Errorf("Could not initialise log file: %v", err)
	}
	return f, nil
}

func examineEventPool() {
	uploadEvents := []watcher.Event{}
	removeEvents := []watcher.Event{}

	// categories the events into batches for upload and remove tasks
	for _, event := range eventPool.events {
		if event.Op == watcher.Remove {
			// we need some more fanciness reacting on moved and renamed files
			removeEvents = append(removeEvents, event)
		} else if event.Op == watcher.Create || event.Op == watcher.Write {
			uploadEvents = append(uploadEvents, event)
		}
	}

	if len(uploadEvents) > 0 {
		// add the upload events to queue for s3 sync thread
		sync.queue = append(sync.queue, SyncTask{Create, uploadEvents})
	}

	if len(removeEvents) > 0 {
		// add the remove events for the s3 sync thread
		sync.queue = append(sync.queue, SyncTask{Delete, removeEvents})
	}
}

func main() {
	// configure logging and run script
	config := readConfig("conf.json")

	f, _ := configureLogging(config.LogFile)
	defer f.Close()
	log.SetOutput(f)

	// make sure the ouput gets set first. So the log writes to a file.
	log.Println()
	log.Println("Starting gosyncs3...")

	// initialize these entries so memory gets preserved
	eventPool.incomingEvent = make(chan watcher.Event)

	// file event found listener
	go func() {
		for {
			newEvent := <-eventPool.incomingEvent
			eventPool.events = append(eventPool.events, newEvent)

			// do this so we can reset the callback
			if eventPool.delay != nil {
				eventPool.delay.Stop()
				log.Println("Killed the previous sync delayed callback")
			}
			log.Println("Event received", newEvent)

			// when a new fileEvent is found that should be synced, check pool size whether max is reached
			if len(eventPool.events) == config.BatchSyncSize {
				examineEventPool()

				log.Println("Sync pool filled!!", newEvent)
				log.Println("Configured to batch sizes of", config.BatchSyncSize)
				if len(eventPool.events) > 0 {
					eventPool.events = eventPool.events[config.BatchSyncSize-1 : len(eventPool.events)-1]
				} else {
					eventPool.events = eventPool.events[0:0]
				}
			} else {
				eventPool.delay = time.AfterFunc(time.Second*4, func() {
					// TODO: make the delay configurable via the config file
					examineEventPool()
					eventPool.events = eventPool.events[0:0]
				})
			}
		}
	}()

	// this is the upload thread
	go func() {
		for {
			if len(sync.queue) > 0 {
				for _, task := range sync.queue {
					// pop the item from the queue
					sync.queue = sync.queue[1:]

					if task.taskType == Create {
						log.Printf("Do a batch create for %d items", len(task.eventBatch))
						uploadFiles(&config, task.eventBatch)
					}

					if task.taskType == Delete {
						log.Println("Do a batch delete")
						removeFiles(&config, task.eventBatch)
					}
				}
			}
		}
	}()

	filewatcher(&config)
}
