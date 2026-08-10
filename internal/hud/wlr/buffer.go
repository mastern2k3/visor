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
	// The shm buffer is exactly the image render.DrawTab produces. It used to be
	// tabOverflow px wider, with those columns synthesised by replicating the
	// rightmost rendered one, so that wobble/alert shifts revealed more tab
	// instead of an empty gap at the screen edge. render now owns that: the
	// capsule is drawn CapsuleDrawW = CapsuleW + MaxProtrusion wide and the
	// overhang lives inside BufW. Keeping both would double-count the overflow.
	bufW    = render.BufW
	bufH    = render.BufH
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

	// onRelease is called from each buffer's Release listener when the
	// compositor returns a buffer. May be nil. Used by layerSurface to retry
	// a repaint that was dropped because both buffers were in-flight.
	onRelease func()
}

// Buffer is one half of the double-buffered pool. Pix is the writable slice
// the renderer fills; Wl is the wl_buffer handed to the compositor.
//
// released is read in Acquire() and written by the wl_buffer.release
// listener. Both callers run on the single Wayland dispatch goroutine
// (display.Dispatch in dock.run), so no synchronization is needed.
// If Acquire is ever called from a different goroutine, this must
// become atomic or guarded by a mutex.
type Buffer struct {
	Wl       wl.Buffer
	Pix      []byte // length == bufSize; three-index slice keeps cap bounded to this buffer's region
	released bool
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
		off := i * bufSize
		end := off + bufSize
		wb := p.pool.CreateBuffer(int32(off), int32(bufW), int32(bufH), int32(bufStri), wl.ShmFormatArgb8888)
		buf := &Buffer{
			Wl:       wb,
			Pix:      mmap[off:end:end], // three-index: cap bounded to this buffer's region
			released: true,
		}
		// Mark released when the compositor finishes with the buffer.
		// Fire onRelease so a surface that set dirty=true can retry.
		buf.Wl.SetListener(wl.BufferListener{
			Release: func(_ any, _ wl.Buffer) error {
				buf.released = true
				if p.onRelease != nil {
					p.onRelease()
				}
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
	for _, b := range p.buffers {
		if b != nil {
			b.Wl.Destroy()
		}
	}
	p.pool.Destroy()
	if p.mmap != nil {
		_ = syscall.Munmap(p.mmap)
		p.mmap = nil
	}
	if p.fd >= 0 {
		_ = syscall.Close(p.fd)
		p.fd = -1
	}
}

// CopyRGBA copies an *image.RGBA (R,G,B,A byte order, bufW x bufH) into the
// destination buffer (same dimensions, BGRA byte order — what wl_shm ARGB8888
// expects on a little-endian host).
func (b *Buffer) CopyRGBA(img *image.RGBA) {
	src := img.Pix
	dst := b.Pix
	if img.Stride != bufStri {
		panic(fmt.Sprintf("wlr: render stride %d, expected %d", img.Stride, bufStri))
	}
	if len(dst) != bufSize {
		panic(fmt.Sprintf("wlr: dst size %d, expected %d", len(dst), bufSize))
	}
	for y := 0; y < bufH; y++ {
		row := y * bufStri
		for x := 0; x < bufW; x++ {
			si := row + x*4
			dst[si+0] = src[si+2] // B
			dst[si+1] = src[si+1] // G
			dst[si+2] = src[si+0] // R
			dst[si+3] = src[si+3] // A
		}
	}
}
