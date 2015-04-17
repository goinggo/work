// Copyright 2014 Ardan Studios. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE handle.

package work

import (
	"errors"
	"fmt"
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

// ErrorInvalidStatTime is the error for the invalid stat time parameter.
var ErrorInvalidStatTime = errors.New("Invalid duration for stat time")

// Worker must be implemented by types that want to use
// this worker processes.
type Worker interface {
	Work(id int)
}

// Pool provides a pool of routines that can execute any Worker
// tasks that are submitted.
type Pool struct {
	minRoutines int            // Minumum number of routines always in the pool.
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

	logFunc func(message string) // Function called to providing logging support
}

// New creates a new Worker.
func New(minRoutines int, statTime time.Duration, logFunc func(message string)) (*Pool, error) {
	if minRoutines <= 0 {
		return nil, ErrorInvalidMinRoutines
	}

	if statTime < time.Millisecond {
		return nil, ErrorInvalidStatTime
	}

	p := Pool{
		minRoutines: minRoutines,
		statTime:    statTime,
		tasks:       make(chan Worker),
		control:     make(chan int),
		kill:        make(chan struct{}),
		shutdown:    make(chan struct{}),
		logFunc:     logFunc,
	}

	// Start the manager.
	p.manager()

	// Add the routines.
	p.Add(minRoutines)

	return &p, nil
}

// Add creates routines to process work or sets a count for
// routines to terminate.
func (p *Pool) Add(routines int) {
	if routines == 0 {
		return
	}

	cmd := addRoutine
	if routines < 0 {
		routines = routines * -1
		cmd = rmvRoutine
	}

	for i := 0; i < routines; i++ {
		p.control <- cmd
	}
}

// work performs the users work and keeps stats.
func (p *Pool) work(id int) {
done:
	for {
		select {
		case t := <-p.tasks:
			atomic.AddInt64(&p.active, 1)
			{
				// Perform the work.
				t.Work(id)
			}
			atomic.AddInt64(&p.active, -1)

		case <-p.kill:
			break done
		}
	}

	// Decrement the counts.
	atomic.AddInt64(&p.routines, -1)
	p.wg.Done()

	p.log("Worker : Shutting Down")
}

// Run wait for the goroutine pool to take the work
// to be executed.
func (p *Pool) Run(work Worker) {
	atomic.AddInt64(&p.pending, 1)
	{
		p.tasks <- work
	}
	atomic.AddInt64(&p.pending, -1)
}

// Shutdown waits for all the workers to finish.
func (p *Pool) Shutdown() {
	close(p.shutdown)
	p.wg.Wait()
}

// manager controls changes to the work pool including stats
// and shutting down.
func (p *Pool) manager() {
	p.wg.Add(1)

	go func() {
		p.log("Work Manager : Started")

		// Create a timer to run stats.
		timer := time.NewTimer(p.statTime)

		for {
			select {
			case <-p.shutdown:
				// Capture the current number of routines.
				routines := int(atomic.LoadInt64(&p.routines))

				// Send a kill to all the existing routines.
				for i := 0; i < routines; i++ {
					p.kill <- struct{}{}
				}

				// Decrement the waitgroup and kill the manager.
				p.wg.Done()
				return

			case c := <-p.control:
				switch c {
				case addRoutine:
					p.log("Work Manager : Add Routine")

					// Capture a unique id.
					p.counter++

					// Add to the counts.
					p.wg.Add(1)
					atomic.AddInt64(&p.routines, 1)

					// Create the routine.
					go p.work(p.counter)

				case rmvRoutine:
					p.log("Work Manager : Remove Routine")

					// Capture the number of routines.
					routines := int(atomic.LoadInt64(&p.routines))

					// Are there routines to remove.
					if routines <= p.minRoutines {
						p.log("Work Manager : Reached Minimum Can't Remove")
						break
					}

					// Send a kill signal to remove a routine.
					p.kill <- struct{}{}
				}

			case <-timer.C:
				// Capture the stats.
				routines := atomic.LoadInt64(&p.routines)
				pending := atomic.LoadInt64(&p.pending)
				active := atomic.LoadInt64(&p.active)

				// Display the stats.
				p.log(fmt.Sprintf("Work Manager : Stats : G[%d] P[%d] A[%d]", routines, pending, active))

				// Reset the clock.
				timer.Reset(p.statTime)
			}
		}
	}()
}

// log sending logging messages back to the client.
func (p *Pool) log(message string) {
	if p.logFunc != nil {
		p.logFunc(message)
	}
}
