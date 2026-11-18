//go:build windows

package main

import (
	"io"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const stdioCancellationDeadline = 3 * time.Second

func TestSynchronousStdioCloseBeforeRead(t *testing.T) {
	read, write := createAnonymousPipe(t)
	t.Cleanup(func() { _ = windows.CloseHandle(write) })
	stream, err := ownStandardPipe(read, false)
	if err != nil {
		t.Fatal(err)
	}
	input, ok := stream.(*synchronousStdio)
	if !ok {
		t.Fatalf("anonymous pipe stream type = %T; want synchronousStdio", stream)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Read(make([]byte, 1)); err != io.ErrClosedPipe {
		t.Fatalf("Read after Close error = %v; want %v", err, io.ErrClosedPipe)
	}
}

func TestSynchronousStdioCloseCancelsBlockedReadAndWrite(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		read, write := createAnonymousPipe(t)
		t.Cleanup(func() { _ = windows.CloseHandle(write) })
		stream, err := ownStandardPipe(read, false)
		if err != nil {
			t.Fatal(err)
		}
		input := stream.(*synchronousStdio)

		ioDone := make(chan error, 1)
		go func() {
			_, err := input.Read(make([]byte, 1))
			ioDone <- err
		}()
		time.Sleep(50 * time.Millisecond)
		closeAndCheckCancellation(t, input.Close, ioDone)
	})

	t.Run("write", func(t *testing.T) {
		read, write := createAnonymousPipe(t)
		t.Cleanup(func() { _ = windows.CloseHandle(read) })
		stream, err := ownStandardPipe(write, true)
		if err != nil {
			t.Fatal(err)
		}
		output := stream.(*synchronousStdio)

		ioDone := make(chan error, 1)
		go func() {
			_, err := output.Write(make([]byte, 8<<20))
			ioDone <- err
		}()
		time.Sleep(50 * time.Millisecond)
		closeAndCheckCancellation(t, output.Close, ioDone)
	})
}

func TestOverlappedStdioCloseCancelsBlockedReadAndWrite(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		server, _ := createOverlappedPipe(t, windows.PIPE_ACCESS_INBOUND, windows.GENERIC_WRITE)
		stream, err := ownStandardPipe(server, false)
		if err != nil {
			t.Fatal(err)
		}
		input, ok := stream.(*overlappedStdio)
		if !ok {
			t.Fatalf("named pipe stream type = %T; want overlappedStdio", stream)
		}

		ioDone := make(chan error, 1)
		go func() {
			_, err := input.Read(make([]byte, 1))
			ioDone <- err
		}()
		time.Sleep(50 * time.Millisecond)
		closeAndCheckCancellation(t, input.Close, ioDone)
	})

	t.Run("write", func(t *testing.T) {
		server, _ := createOverlappedPipe(t, windows.PIPE_ACCESS_OUTBOUND, windows.GENERIC_READ)
		stream, err := ownStandardPipe(server, true)
		if err != nil {
			t.Fatal(err)
		}
		output, ok := stream.(*overlappedStdio)
		if !ok {
			t.Fatalf("named pipe stream type = %T; want overlappedStdio", stream)
		}

		ioDone := make(chan error, 1)
		go func() {
			_, err := output.Write(make([]byte, 8<<20))
			ioDone <- err
		}()
		time.Sleep(50 * time.Millisecond)
		closeAndCheckCancellation(t, output.Close, ioDone)
	})
}

func createAnonymousPipe(t *testing.T) (windows.Handle, windows.Handle) {
	t.Helper()
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		t.Fatal(err)
	}
	return read, write
}

func createOverlappedPipe(t *testing.T, serverAccess, clientAccess uint32) (windows.Handle, windows.Handle) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(`\\.\pipe\syntra-stdio-test-` + time.Now().Format("150405.000000000"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := windows.CreateNamedPipe(name, serverAccess|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverTransferred := false
	defer func() {
		if !serverTransferred {
			_ = windows.CloseHandle(server)
		}
	}()

	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	connect := windows.Overlapped{HEvent: event}
	connectErr := windows.ConnectNamedPipe(server, &connect)
	if connectErr != nil && connectErr != windows.ERROR_IO_PENDING && connectErr != windows.ERROR_PIPE_CONNECTED {
		t.Fatal(connectErr)
	}

	peer, err := windows.CreateFile(name, clientAccess, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(peer) })

	if connectErr == windows.ERROR_IO_PENDING {
		result, err := windows.WaitForSingleObject(event, windows.INFINITE)
		if err != nil {
			t.Fatal(err)
		}
		if result != windows.WAIT_OBJECT_0 {
			t.Fatalf("ConnectNamedPipe wait result = %#x", result)
		}
		var count uint32
		if err := windows.GetOverlappedResult(server, &connect, &count, false); err != nil {
			t.Fatal(err)
		}
	}
	serverTransferred = true
	return server, peer
}

func closeAndCheckCancellation(t *testing.T, closeStream func() error, ioDone <-chan error) {
	t.Helper()
	closeDone := make(chan error, 1)
	go func() { closeDone <- closeStream() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(stdioCancellationDeadline):
		t.Fatal("Close did not cancel the blocked pipe operation")
	}
	select {
	case err := <-ioDone:
		if err == nil {
			t.Fatal("blocked operation succeeded after its stream was closed")
		}
	case <-time.After(stdioCancellationDeadline):
		t.Fatal("blocked operation did not return after Close")
	}
}
