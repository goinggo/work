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
	addRoutine    = 1
	removeRoutine = 2
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
	Work()
}

// routine maintains information about existing routines
// in the pool.
type routine struct {
	id      int
	lastRun time.Time
}

// Work provides a pool of routines that can execute any Worker
// tasks that are submitted.
type Work struct {
	minRoutines int             // Minumum number of routines always in the pool.
	idleTime    time.Duration   // Time to look for idle connections.
	statTime    time.Duration   // Time to display stats.
	counter     int             // counter maintains a running total number of routines ever created.
	tasks       chan Worker     // Unbuffered channel that work is sent into.
	control     chan int        // Unbuffered channel that work for routine management is send into.
	kill        chan struct{}   // Unbuffered channel to signal for a goroutine to die.
	killR       chan int        // Unbuffered channel to respond on a kill request.
	shutdown    chan struct{}   // Closed when the Work pool is being shutdown.
	wg          sync.WaitGroup  // Manages the number of routines for shutdown.
	routines    map[int]routine // Map of routines that currently exist in the pool.
	active      int64           // Active number of routines in the work pool.
	pending     int64           // Pending number of routines waiting to submit work.
}

// manager controls the map of routines and helps to remove
// routines not being used.
func (w *Work) manager() {
	w.wg.Add(1)

	go func() {
		log.Println("Work : manager : Started")
		idle := time.After(w.idleTime)
		stats := time.After(time.Second)

		for {
			select {
			case <-w.shutdown:
				log.Println("Work : manager : Started : Shutdown")
				l := len(w.routines)
				for i := 0; i < l; i++ {
					// Send a kill signal and wait for any goroutine
					// to get the signal to die. It will return its id.
					w.kill <- struct{}{}
					id := <-w.killR

					// Remove this routine from the map.
					delete(w.routines, id)
				}

				// Mark that we are done.
				w.wg.Done()
				log.Println("Work : manager : Completed : Shutdown")
				return

			case cmd := <-w.control:
				switch cmd {
				// Add new routine.
				case addRoutine:
					log.Println("Work : manager : Info : Add Routine")

					// Capture a unique id.
					w.counter++

					// Add a routine to the map.
					w.routines[w.counter] = routine{
						id:      w.counter,
						lastRun: time.Now(),
					}

					// Create the routine.
					go w.work(w.counter)

				// Remove routine.
				case removeRoutine:
					log.Println("Work : manager : Info : Remove Routine")

					// Are there routines to remove.
					if len(w.routines) <= w.minRoutines {
						log.Println("Work : manager : Info : Can't Remove, At Minimum")
						continue
					}

					// Send a kill signal and wait for any goroutine
					// to get the signal to die. It will return its id.
					w.kill <- struct{}{}
					id := <-w.killR

					// Remove this routine from the map.
					delete(w.routines, id)
				}

			case <-idle:
				log.Println("Work : manager : Started : Idle Routines")

				// Look for idle routines to remove.
				now := time.Now()

				for _, r := range w.routines {
					if now.Sub(r.lastRun) >= w.idleTime {
						// Send a kill signal and wait for any goroutine
						// to get the signal to die. It will return its id.
						w.kill <- struct{}{}
						id := <-w.killR

						// Remove this routine from the map.
						delete(w.routines, id)
					}
				}

				// Reset the clock.
				idle = time.After(w.idleTime)
				log.Println("Work : manager : Completed : Idle Routines")

			case <-stats:
				// Capture the numbers.
				pending := atomic.LoadInt64(&w.pending)
				active := atomic.LoadInt64(&w.active)

				// Display the stats.
				fmt.Printf("Work : manager : Stats : G[%d] P[%d] A[%d]\n", len(w.routines), pending, active)

				// Reset the clock.
				stats = time.After(time.Second)
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

	if statTime < time.Minute {
		return nil, ErrorInvalidStatTime
	}

	w := Work{
		minRoutines: minRoutines,
		idleTime:    idleTime,
		statTime:    statTime,
		tasks:       make(chan Worker),
		control:     make(chan int),
		kill:        make(chan struct{}),
		killR:       make(chan int),
		shutdown:    make(chan struct{}),
		routines:    make(map[int]routine),
	}

	// Start the manager.
	w.manager()

	// Add the routines.
	for i := 0; i < minRoutines; i++ {
		w.control <- addRoutine
	}

	return &w, nil
}

// Add creates routines to process work or sets a count for
// routines to terminate.
func (w *Work) Add(routines int) {
	if routines == 0 {
		return
	}

	// Determine if we are adding or removing.
	command := addRoutine
	if routines < 0 {
		routines = routines * -1
		command = removeRoutine
	}

	// Send the commands in.
	for i := 0; i < routines; i++ {
		w.control <- command
	}
}

// work performs the users work and keeps stats.
func (w *Work) work(id int) {
	log.Println("Work : gr : Started :", id)
done:
	for {
		select {
		case t := <-w.tasks:
			atomic.AddInt64(&w.active, 1)
			t.Work()
			atomic.AddInt64(&w.active, -1)

		case <-w.kill:
			w.killR <- id
			break done
		}
	}

	log.Println("Work : gr : Down :", id)
}

// Run wait for the goroutine pool to take the work
// to be executed.
func (w *Work) Run(work Worker) {
	atomic.AddInt64(&w.pending, 1)
	w.tasks <- work
	atomic.AddInt64(&w.pending, -1)
}

// Shutdown waits for all the workers to finish.
func (w *Work) Shutdown() {
	close(w.shutdown)
	w.wg.Wait()
}
