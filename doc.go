// Package work manages a pool of routines to perform work. It does so my providing
// a Do function that will block when the pool is busy. This also allows the pool
// to monitor and report pushback.
//
// Interface
//
// The Worker interface is how you can provide work to the pool.
//
//	// Worker must be implemented by types that want to use this worker processes.
//	type Worker interface {
//		Work(id int)
//	}
//
//
package work
