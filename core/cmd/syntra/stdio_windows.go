//go:build windows

package main

import (
	"io"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var cancelSynchronousIO = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

const synchronousCancelPollInterval = 5 * time.Millisecond

func duplicateMCPStdin() (io.ReadCloser, error) {
	h, err := duplicateStandardHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	return ownStandardPipe(h, false)
}

func duplicateMCPStdout() (io.WriteCloser, error) {
	h, err := duplicateStandardHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	return ownStandardPipe(h, true)
}

func ownStandardPipe(handle windows.Handle, write bool) (io.ReadWriteCloser, error) {
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_PIPE {
		_ = windows.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
		return nil, windows.ERROR_INVALID_HANDLE
	}
	var mode uint32
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&mode)), uint32(unsafe.Sizeof(mode)), 16); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if mode&(windows.FILE_SYNCHRONOUS_IO_ALERT|windows.FILE_SYNCHRONOUS_IO_NONALERT) == 0 {
		// Keep overlapped I/O explicit. os.NewFile infers pollability with a
		// second best-effort mode query; if that query failed it could submit a
		// nil OVERLAPPED to a handle that requires one.
		return newOverlappedStdio(handle, write), nil
	}
	return newSynchronousStdio(handle, write)
}

func duplicateStandardHandle(which uint32) (windows.Handle, error) {
	source, err := windows.GetStdHandle(which)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if source == 0 || source == windows.InvalidHandle {
		return windows.InvalidHandle, windows.ERROR_INVALID_HANDLE
	}
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), source, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return windows.InvalidHandle, err
	}
	if duplicate == 0 || duplicate == windows.InvalidHandle {
		return windows.InvalidHandle, windows.ERROR_INVALID_HANDLE
	}
	return duplicate, nil
}

type synchronousIO struct {
	buffer []byte
	result chan ioResult
}

type ioResult struct {
	n   int
	err error
}

// synchronousStdio owns a non-inheritable duplicate of an inherited standard
// handle. Windows anonymous pipes do not support overlapped I/O, so a dedicated
// locked OS thread performs synchronous calls and Close interrupts that thread
// with CancelSynchronousIo before releasing either handle.
type synchronousStdio struct {
	handle windows.Handle
	thread windows.Handle
	write  bool

	requests chan synchronousIO
	closing  chan struct{}
	done     chan struct{}
	closeOne sync.Once
	closeErr error
}

func newSynchronousStdio(handle windows.Handle, write bool) (*synchronousStdio, error) {
	if err := cancelSynchronousIO.Find(); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	p := &synchronousStdio{
		handle:   handle,
		write:    write,
		requests: make(chan synchronousIO),
		closing:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	ready := make(chan error, 1)
	go p.run(ready)
	if err := <-ready; err != nil {
		<-p.done
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return p, nil
}

func (p *synchronousStdio) run(ready chan<- error) {
	runtime.LockOSThread()
	// Let the runtime terminate this thread when the worker exits. Close uses
	// the saved thread handle to cancel synchronous I/O and must not race with
	// the runtime reusing the thread for unrelated work.
	defer close(p.done)

	id := windows.GetCurrentThreadId()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, id)
	if err != nil {
		ready <- err
		return
	}
	p.thread = thread
	ready <- nil

	for {
		select {
		case <-p.closing:
			return
		case request := <-p.requests:
			var count uint32
			if p.write {
				err = windows.WriteFile(p.handle, request.buffer, &count, nil)
			} else {
				err = windows.ReadFile(p.handle, request.buffer, &count, nil)
				if err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_HANDLE_EOF {
					err = io.EOF
				}
			}
			request.result <- ioResult{n: int(count), err: err}
		}
	}
}

func (p *synchronousStdio) Read(buffer []byte) (int, error) {
	if p.write {
		return 0, windows.ERROR_INVALID_FUNCTION
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	return p.execute(buffer)
}

func (p *synchronousStdio) Write(buffer []byte) (int, error) {
	if !p.write {
		return 0, windows.ERROR_INVALID_FUNCTION
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(buffer) {
		n, err := p.execute(buffer[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (p *synchronousStdio) execute(buffer []byte) (int, error) {
	request := synchronousIO{buffer: buffer, result: make(chan ioResult, 1)}
	select {
	case <-p.closing:
		return 0, io.ErrClosedPipe
	case <-p.done:
		return 0, io.ErrClosedPipe
	case p.requests <- request:
	}
	// Do not return while the worker can still access the caller's buffer. Close
	// cancels the syscall and joins this thread before closing the duplicated
	// handle, so every accepted request eventually produces a result.
	result := <-request.result
	if result.err == windows.ERROR_OPERATION_ABORTED {
		select {
		case <-p.closing:
			return result.n, io.ErrClosedPipe
		default:
		}
	}
	return result.n, result.err
}

func (p *synchronousStdio) Close() error {
	p.closeOne.Do(func() {
		close(p.closing)
		for {
			select {
			case <-p.done:
				if err := windows.CloseHandle(p.thread); p.closeErr == nil {
					p.closeErr = err
				}
				if err := windows.CloseHandle(p.handle); p.closeErr == nil {
					p.closeErr = err
				}
				return
			default:
			}

			// CancelSynchronousIo can return ERROR_NOT_FOUND if Close wins the
			// small interval before ReadFile/WriteFile starts. Retry until the
			// worker has stopped, keeping both handles alive throughout.
			result, _, cancelErr := cancelSynchronousIO.Call(uintptr(p.thread))
			if result == 0 && cancelErr != windows.ERROR_NOT_FOUND && p.closeErr == nil {
				if cancelErr == nil {
					cancelErr = windows.ERROR_INVALID_FUNCTION
				}
				p.closeErr = cancelErr
			}
			timer := time.NewTimer(synchronousCancelPollInterval)
			select {
			case <-p.done:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
			}
		}
	})
	return p.closeErr
}

// overlappedStdio owns an overlapped duplicate of an inherited pipe. Every
// operation carries its own event and OVERLAPPED structure, and Close cancels
// pending operations before releasing the handle.
type overlappedStdio struct {
	handle windows.Handle
	write  bool

	ioMu      sync.Mutex
	mu        sync.Mutex
	cond      *sync.Cond
	closed    bool
	active    int
	closeOne  sync.Once
	closeDone chan struct{}
	closeErr  error
}

func newOverlappedStdio(handle windows.Handle, write bool) *overlappedStdio {
	p := &overlappedStdio{
		handle:    handle,
		write:     write,
		closeDone: make(chan struct{}),
	}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *overlappedStdio) Read(buffer []byte) (int, error) {
	if p.write {
		return 0, windows.ERROR_INVALID_FUNCTION
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	return p.execute(buffer)
}

func (p *overlappedStdio) Write(buffer []byte) (int, error) {
	if !p.write {
		return 0, windows.ERROR_INVALID_FUNCTION
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(buffer) {
		n, err := p.execute(buffer[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (p *overlappedStdio) execute(buffer []byte) (int, error) {
	// This transport has one reader and one writer. Serializing calls on each
	// endpoint keeps byte-stream framing deterministic if the SDK writes from
	// more than one goroutine.
	p.ioMu.Lock()
	defer p.ioMu.Unlock()

	// Overlapped pipe I/O requires a manual-reset event so the completion stays
	// signaled until GetOverlappedResult consumes the operation state.
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)

	var operation windows.Overlapped
	// The low bit prevents a completion packet on a handle that was already
	// associated with an I/O completion port by its parent process.
	operation.HEvent = event | 1
	var pinner runtime.Pinner
	pinner.Pin(&operation)
	pinner.Pin(&buffer[0])
	defer pinner.Unpin()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	p.active++
	var count uint32
	if p.write {
		err = windows.WriteFile(p.handle, buffer, &count, &operation)
	} else {
		err = windows.ReadFile(p.handle, buffer, &count, &operation)
	}
	p.mu.Unlock()

	if err == windows.ERROR_IO_PENDING {
		result, waitErr := windows.WaitForSingleObject(event, windows.INFINITE)
		if waitErr != nil {
			err = waitErr
		} else if result != windows.WAIT_OBJECT_0 {
			err = windows.ERROR_INVALID_DATA
		} else {
			err = windows.GetOverlappedResult(p.handle, &operation, &count, false)
		}
	}

	if !p.write && (err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_HANDLE_EOF) {
		err = io.EOF
	}
	if err == windows.ERROR_OPERATION_ABORTED {
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			err = io.ErrClosedPipe
		}
	}

	p.mu.Lock()
	p.active--
	if p.active == 0 {
		p.cond.Broadcast()
	}
	p.mu.Unlock()
	return int(count), err
}

func (p *overlappedStdio) Close() error {
	p.closeOne.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()

		// CancelIoEx can report ERROR_NOT_FOUND when an operation completed
		// between the close request and cancellation. In that case the active
		// operation will still signal its event and leave the wait below.
		if err := windows.CancelIoEx(p.handle, nil); err != nil && err != windows.ERROR_NOT_FOUND {
			p.closeErr = err
		}

		p.mu.Lock()
		for p.active != 0 {
			p.cond.Wait()
		}
		p.mu.Unlock()

		if err := windows.CloseHandle(p.handle); p.closeErr == nil {
			p.closeErr = err
		}
		close(p.closeDone)
	})
	<-p.closeDone
	return p.closeErr
}
