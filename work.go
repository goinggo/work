// Copyright 2014 Ardan Studios. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE handle.

// Package work manages a pool of routines to perform work.
package work

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const (
	addRoutine = 1
	rmvRoutine = 2
)

// ErrorInvalidMinRoutines is the error for the invalid minRoutine parameter.
var ErrorInvalidMinRoutines = errors.New("Invalid minimum number of routines")

// ErrorInvalidIdleTime is the error for the invalid idle time parameter.
var ErrorInvalidIdleTime = errors.New("Invalid duration for idle time")

// ErrorInvalidStatTime is the error for the invalid stat time parameter.
var ErrorInvalidStatTime = errors.New("Invalid duration for stat time")

// Worker must be implemented by types that want to use
// this worker processes.
type Worker interface {
	Work(id int)
}

// Work provides a pool of routines that can execute any Worker
// tasks that are submitted.
type Work struct {
	minRoutines int            // Minumum number of routines always in the pool.
	idleTime    time.Duration  // Time for routines to die on idle.
	statTime    time.Duration  // Time to display stats.
	counter     int            // Maintains a running total number of routines ever created.
	tasks       chan Worker    // Unbuffered channel that work is sent into.
	control     chan int       // Unbuffered channel that work for the manager is send into.
	kill        chan struct{}  // Unbuffered channel to signal for a goroutine to die.
	shutdown    chan struct{}  // Closed when the Work pool is being shutdown.
	wg          sync.WaitGroup // Manages the number of routines for shutdown.
	routines    int64          // Number of routines
	active      int64          // Active number of routines in the work pool.
	pending     int64          // Pending number of routines waiting to submit work.
}

// manager controls changes to the work pool including stats
// and shutting down.
func (w *Work) manager() {
	w.wg.Add(1)

	go func() {
		log.Println("Work : manager : Started")

		// Create a timer to run stats.
		var stats <-chan time.Time
		timer := time.NewTimer(w.statTime)
		stats = timer.C

		for {
			select {
			case <-w.shutdown:
				// Capture the current number of routines.
				routines := int(atomic.LoadInt64(&w.routines))

				// Send a kill to all the existing routines.
				for i := 0; i < routines; i++ {
					w.kill <- struct{}{}
				}

				// Decrement the waitgroup and kill the manager.
				w.wg.Done()
				return

			case c := <-w.control:
				switch c {
				case addRoutine:
					log.Println("Work : manager : Info : Add Routine")

					// Capture a unique id.
					w.counter++

					// Add to the counts.
					w.wg.Add(1)
					atomic.AddInt64(&w.routines, 1)

					// Create the routine.
					go w.work(w.counter)

				case rmvRoutine:
					log.Println("Work : manager : Info : Remove Routine")

					// Capture the number of routines.
					routines := int(atomic.LoadInt64(&w.routines))

					// Are there routines to remove.
					if routines <= w.minRoutines {
						log.Println("Work : manager : Info : Remove Routine Cancelled")
						break
					}

					// Send a kill signal to remove a routine.
					w.kill <- struct{}{}
				}

			case <-stats:
				// Capture the stats.
				routines := atomic.LoadInt64(&w.routines)
				pending := atomic.LoadInt64(&w.pending)
				active := atomic.LoadInt64(&w.active)

				// Display the stats.
				fmt.Printf("Work : manager : Stats : G[%d] P[%d] A[%d]\n", routines, pending, active)

				// Reset the clock.
				timer.Reset(w.statTime)
			}
		}
	}()
}

// New creates a new Worker.
func New(minRoutines int, idleTime time.Duration, statTime time.Duration) (*Work, error) {
	if minRoutines < 0 {
		return nil, ErrorInvalidMinRoutines
	}

	if idleTime < time.Minute {
		return nil, ErrorInvalidIdleTime
	}

	if statTime < time.Millisecond {
		return nil, ErrorInvalidStatTime
	}

	w := Work{
		minRoutines: minRoutines,
		idleTime:    idleTime,
		statTime:    statTime,
		tasks:       make(chan Worker),
		control:     make(chan int),
		kill:        make(chan struct{}),
		shutdown:    make(chan struct{}),
	}

	// Start the manager.
	w.manager()

	// Add the routines.
	w.Add(minRoutines)

	return &w, nil
}

// Add creates routines to process work or sets a count for
// routines to terminate.
func (w *Work) Add(routines int) {
	if routines == 0 {
		return
	}

	cmd := addRoutine
	if routines < 0 {
		routines = routines * -1
		cmd = rmvRoutine
	}

	for i := 0; i < routines; i++ {
		w.control <- cmd
	}
}

// work performs the users work and keeps stats.
func (w *Work) work(id int) {
	// Create a timer to track idle time.
	var idle <-chan time.Time
	var timer *time.Timer

	// Set the timer for routines about the min mark.
	if id > w.minRoutines {
		timer = time.NewTimer(w.idleTime)
		idle = timer.C
	}

done:
	for {
		select {
		case t := <-w.tasks:
			atomic.AddInt64(&w.active, 1)
			{
				// Perform the work.
				t.Work(id)
			}
			atomic.AddInt64(&w.active, -1)

		case <-w.kill:
			break done

		case <-idle:
			break done
		}

		// If this goroutine can die on idle time
		// then reset the timer.
		if timer != nil {
			timer.Reset(w.idleTime)
		}
	}

	// Decrement the counts.
	atomic.AddInt64(&w.routines, -1)
	w.wg.Done()

	log.Println("Work : gr : Info : Shutdown")
}

// Run wait for the goroutine pool to take the work
// to be executed.
func (w *Work) Run(work Worker) {
	atomic.AddInt64(&w.pending, 1)
	{
		w.tasks <- work
	}
	atomic.AddInt64(&w.pending, -1)
}

// Shutdown waits for all the workers to finish.
func (w *Work) Shutdown() {
	close(w.shutdown)
	w.wg.Wait()
}
