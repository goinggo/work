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
	updRoutine = 2
	rmvRoutine = 3
	doaRoutine = 4
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

// command is the command the manager is asked to process.
type command struct {
	task   int // The tasks to perform.
	id     int // The id of the routine to act against.
	repeat int // The number of times to repeat the task.
}

// Work provides a pool of routines that can execute any Worker
// tasks that are submitted.
type Work struct {
	minRoutines int             // Minumum number of routines always in the pool.
	idleTime    time.Duration   // Time to look for idle connections.
	statTime    time.Duration   // Time to display stats.
	counter     int             // counter maintains a running total number of routines ever created.
	tasks       chan Worker     // Unbuffered channel that work is sent into.
	control     chan command    // Unbuffered channel that work for the manager is send into.
	kill        chan struct{}   // Unbuffered channel to signal for a goroutine to die.
	shutdown    chan struct{}   // Closed when the Work pool is being shutdown.
	wg          sync.WaitGroup  // Manages the number of routines for shutdown.
	routines    map[int]routine // Map of routines that currently exist in the pool.
	active      int64           // Active number of routines in the work pool.
	pending     int64           // Pending number of routines waiting to submit work.
}

// Checks if we are in shutdown mode.
func (w *Work) isShutdown() bool {
	select {
	case <-w.shutdown:
		return true
	default:
		return false
	}
}

// manager controls the map of routines and helps to remove
// routines not being used.
func (w *Work) manager() {
	w.wg.Add(1)

	go func() {
		log.Println("Work : manager : Started")
		idle := time.After(w.idleTime)
		stats := time.After(time.Second)
		shutdown := w.shutdown

		for {
			select {
			case <-shutdown:
				l := len(w.routines)
				for i := 0; i < l; i++ {
					// Send a kill to all the existing routines.
					go func() {
						w.kill <- struct{}{}
					}()
				}

				// This channel is now closed and we don't
				// want to process it again.
				shutdown = nil

			case c := <-w.control:
				switch c.task {
				// Add new routine.
				case addRoutine:
					log.Println("Work : manager : Info : Add Routine")

					// Increment the wait group count.
					w.wg.Add(c.repeat)

					for i := 0; i < c.repeat; i++ {
						// Capture a unique id.
						w.counter++

						// Add a routine to the map.
						w.routines[w.counter] = routine{
							id:      w.counter,
							lastRun: time.Now(),
						}

						// Create the routine.
						go w.work(w.counter)
					}

				// Update the specified routine.
				case updRoutine:
					r := w.routines[c.id]
					r.lastRun = time.Now()
					w.routines[c.id] = r

				// Remove a routine.
				case rmvRoutine:
					log.Println("Work : manager : Info : Remove Routine")

					t := len(w.routines)
					for i := 0; i < c.repeat; i++ {
						// Are there routines to remove.
						if t <= w.minRoutines {
							break
						}

						// Send a kill signal to remove a routine.
						go func() {
							w.kill <- struct{}{}
						}()

						// Remove a routine from the count.
						t--
					}

				// Routine reports dead.
				case doaRoutine:
					log.Println("Work : manager : Info : Routine Killed :", c.id)

					// Remove this routine from the map.
					delete(w.routines, c.id)

					// Mark this goroutine is gone.
					w.wg.Done()

					// Do we need to shutdown this manager.
					if len(w.routines) == 0 && w.isShutdown() {
						log.Println("Work : manager : Completed : Shutdown")
						w.wg.Done()
						return
					}
				}

			case <-idle:
				log.Println("Work : manager : Started : Idle Routines")

				// Look for idle routines to remove.
				now := time.Now()

				for _, r := range w.routines {
					if now.Sub(r.lastRun) >= w.idleTime {
						// Send a kill signal to remove some idle routines.
						// Not relevant which routines are choosen.
						go func() {
							w.kill <- struct{}{}
						}()
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
		control:     make(chan command),
		kill:        make(chan struct{}),
		shutdown:    make(chan struct{}),
		routines:    make(map[int]routine),
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

	// Determine if we are adding or removing.
	cmd := command{
		task:   addRoutine,
		repeat: routines,
	}
	if routines < 0 {
		cmd.task = rmvRoutine
		cmd.repeat = routines * -1
	}

	// Send the command
	w.control <- cmd
}

// work performs the users work and keeps stats.
func (w *Work) work(id int) {
done:
	for {
		select {
		case t := <-w.tasks:
			atomic.AddInt64(&w.active, 1)
			{
				// Perform the work.
				t.Work()

				// Update the lastRun time.
				w.control <- command{
					task: updRoutine,
					id:   id,
				}
			}
			atomic.AddInt64(&w.active, -1)

		case <-w.kill:
			// Report this routine is dead.
			w.control <- command{
				task: doaRoutine,
				id:   id,
			}
			break done
		}
	}
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
