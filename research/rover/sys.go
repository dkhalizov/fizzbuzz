package main

import (
	"syscall"
	"unsafe"
)

func listenReusePort(port int) (int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	const soReusePort = 15
	for _, opt := range []int{syscall.SO_REUSEADDR, soReusePort} {
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, opt, 1); err != nil {
			return -1, err
		}
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: port}); err != nil {
		return -1, err
	}
	return fd, syscall.Listen(fd, 4096)
}

func allowedCPUs() []int {
	var mask [16]uint64
	_, _, e := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, unsafe.Sizeof(mask), uintptr(unsafe.Pointer(&mask)))
	if e != 0 {
		return nil
	}
	var cpus []int
	for i := range len(mask) * 64 {
		if mask[i/64]&(1<<(i%64)) != 0 {
			cpus = append(cpus, i)
		}
	}
	return cpus
}

// pinThread binds the calling OS thread to one CPU.
func pinThread(cpu int) {
	var mask [16]uint64
	mask[cpu/64] = 1 << (cpu % 64)
	_, _, _ = syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, unsafe.Sizeof(mask), uintptr(unsafe.Pointer(&mask)))
}

func setBlocking(fd int, timeout int64) {
	_ = syscall.SetNonblock(fd, false)
	tv := syscall.NsecToTimeval(timeout)
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, &tv)
}

// writeAll writes b to a blocking socket. SO_SNDTIMEO bounds each write, as
// the write deadline of the service bounds each chunk.
func writeAll(fd int, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := syscall.Write(fd, b[n:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return n, err
		}
		n += m
	}
	return n, nil
}

type fdWriter struct{ fd int }

func (w fdWriter) Write(b []byte) (int, error) { return writeAll(w.fd, b) }
