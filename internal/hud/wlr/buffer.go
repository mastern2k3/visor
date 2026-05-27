package wlr

import (
	"fmt"
	"image"
	"syscall"

	"codeberg.org/tesselslate/wl"
	"golang.org/x/sys/unix"

	"github.com/nitzanz/visor/internal/hud/render"
)

const (
	bufW    = render.ExpandedW
	bufH    = render.TongueH
	bufStri = bufW * 4 // 4 bytes per pixel, ARGB8888
	bufSize = bufStri * bufH
)

// shmPool owns a single mmap'd memfd shared with the compositor. It holds two
// buffers; the dock picks whichever is currently released.
type shmPool struct {
	pool wl.ShmPool
	mmap []byte
	fd   int

	buffers [2]*Buffer
}

// Buffer is one half of the double-buffered pool. Pix is the writable slice
// the renderer fills; Wl is the wl_buffer handed to the compositor.
type Buffer struct {
	Wl       wl.Buffer
	Pix      []byte // length == bufSize
	released bool   // true if compositor has released the buffer
}

func newShmPool(shm *wl.Shm) (*shmPool, error) {
	// memfd_create avoids needing a /dev/shm path. MFD_CLOEXEC keeps the fd
	// from leaking into child processes (we don't fork, but be defensive).
	fd, err := unix.MemfdCreate("visor-wlr", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("memfd_create: %w", err)
	}
	if err := syscall.Ftruncate(fd, int64(bufSize*2)); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("ftruncate: %w", err)
	}
	mmap, err := syscall.Mmap(fd, 0, bufSize*2, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("mmap: %w", err)
	}

	pool := shm.CreatePool(fd, int32(bufSize*2))
	p := &shmPool{pool: pool, mmap: mmap, fd: fd}

	for i := 0; i < 2; i++ {
		off := int32(i * bufSize)
		wb := p.pool.CreateBuffer(off, int32(bufW), int32(bufH), int32(bufStri), wl.ShmFormatArgb8888)
		buf := &Buffer{
			Wl:       wb,
			Pix:      mmap[off : int(off)+bufSize],
			released: true,
		}
		// Mark released when the compositor finishes with the buffer.
		buf.Wl.SetListener(wl.BufferListener{
			Release: func(_ any, _ wl.Buffer) error {
				buf.released = true
				return nil
			},
		}, nil)
		p.buffers[i] = buf
	}
	return p, nil
}

// Acquire returns a released buffer ready for writing, or nil if both are
// still in-flight (in which case the caller should drop the frame).
func (p *shmPool) Acquire() *Buffer {
	for _, b := range p.buffers {
		if b.released {
			b.released = false
			return b
		}
	}
	return nil
}

func (p *shmPool) close() {
	if p.mmap != nil {
		_ = syscall.Munmap(p.mmap)
	}
	if p.fd > 0 {
		_ = syscall.Close(p.fd)
	}
}

// CopyRGBA copies an *image.RGBA (R,G,B,A byte order) into the buffer as
// ARGB8888 little-endian, which is what wl_shm expects on a little-endian
// host (bytes in memory are B, G, R, A — i.e. BGRA order).
func (b *Buffer) CopyRGBA(img *image.RGBA) {
	src := img.Pix
	dst := b.Pix
	// RGBA → BGRA byte swap; alpha preserved.
	for i := 0; i+3 < len(src) && i+3 < len(dst); i += 4 {
		dst[i+0] = src[i+2] // B
		dst[i+1] = src[i+1] // G
		dst[i+2] = src[i+0] // R
		dst[i+3] = src[i+3] // A
	}
}
